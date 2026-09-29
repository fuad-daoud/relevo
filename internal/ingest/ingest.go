package ingest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// GitFacts is the slice of git identity ingest needs. It matches
// relevo.Git.RepoFacts structurally so a *relevo.Runtime's Git passes through
// Deps without ingest importing its caller.
type GitFacts interface {
	RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error)
}

// SessionLocator mirrors relevo.SessionLocator's signature, for the same reason
// GitFacts mirrors relevo.Git.
type SessionLocator func(kind, sessionID string) (path string, ok bool)

// Deps are Ingest's optional collaborators. The zero value is usable: no repo is
// resolved, no mastermind transcript is located, Now is time.Now and Logger is
// slog.Default().
type Deps struct {
	// Git resolves a live binding's repo identity when bind.json carries no
	// RepoRef. Nil means never resolve.
	Git GitFacts
	// Sessions locates a mastermind's own transcript file when bind.json carries
	// no locator. Nil means never locate.
	Sessions SessionLocator
	Now      func() time.Time
	Logger   *slog.Logger
}

func (d Deps) defaults() Deps {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return d
}

// Stats summarises what one Ingest call wrote.
type Stats struct {
	Bindings, Rounds, Events, Artifacts, TranscriptRecords, Skipped int
}

// Add returns the field-wise sum of s and o.
func (s Stats) Add(o Stats) Stats {
	return Stats{
		Bindings:          s.Bindings + o.Bindings,
		Rounds:            s.Rounds + o.Rounds,
		Events:            s.Events + o.Events,
		Artifacts:         s.Artifacts + o.Artifacts,
		TranscriptRecords: s.TranscriptRecords + o.TranscriptRecords,
		Skipped:           s.Skipped + o.Skipped,
	}
}

// memberStore is a throwaway *store.Store used only for its path helpers, so
// every member basename comes from those rather than being hard-coded.
var memberStore = store.New("/")

func donePathBase(round int) string { return filepath.Base(memberStore.DonePath("x", round)) }

// roundFromMember returns the round number in a round-scoped member's "NNN-"
// prefix, false when name carries none (bind.json, log.jsonl).
func roundFromMember(name string) (int, bool) {
	if len(name) < 4 || name[3] != '-' {
		return 0, false
	}
	n, err := strconv.Atoi(name[:3])
	if err != nil {
		return 0, false
	}
	return n, true
}

func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// repoRefFromGit resolves dir's repo identity via g, nil when dir is empty,
// RepoFacts errors, or the facts carry neither an origin nor a common dir.
func repoRefFromGit(ctx context.Context, g GitFacts, dir string) *store.RepoRef {
	if dir == "" {
		return nil
	}
	originURL, commonDir, err := g.RepoFacts(ctx, dir)
	if err != nil {
		return nil
	}
	normalised := git.NormalizeOriginURL(originURL)
	if normalised == "" && commonDir == "" {
		return nil
	}
	return &store.RepoRef{OriginURL: normalised, CommonDir: commonDir}
}

func sourceOpener(src Source, member string) (func() (io.ReadCloser, error), string) {
	return func() (io.ReadCloser, error) {
		rc, _, err := src.Open(member)
		return rc, err
	}, cursorSourceKey(src, member)
}

// Ingest reads src -- a binding's directory, live or archived -- and upserts every
// fact it holds into d in one transaction. Cursors for every member it touched are
// saved there too, so a later call with unchanged files writes nothing.
//
// It returns ErrSource unwrapped when src's bind.json is missing or invalid --
// nothing is written in that case -- and wraps a database error.
func Ingest(ctx context.Context, src Source, d *db.DB, deps Deps) (Stats, error) {
	deps = deps.defaults()

	b, err := src.Bind()
	if err != nil {
		return Stats{}, err
	}
	members, err := memberSet(src)
	if err != nil {
		return Stats{}, err
	}
	kind, _ := src.Origin()
	ref, locator := resolveRefs(ctx, deps, b, kind)

	run := &ingestRun{
		src: src, b: b, members: members, kind: kind,
		ref: ref, locator: locator, deps: deps, logger: deps.Logger,
	}
	var stats Stats
	err = d.Tx(func(tx *db.Tx) error {
		var terr error
		stats, terr = run.run(tx)
		return terr
	})
	if err != nil {
		return Stats{}, fmt.Errorf("ingest %s: %w", src.Name(), err)
	}
	return stats, nil
}

func memberSet(src Source) (map[string]bool, error) {
	list, err := src.List()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: list: %w", ErrSource, src.Name(), err)
	}
	members := make(map[string]bool, len(list))
	for _, m := range list {
		members[m] = true
	}
	return members, nil
}

// resolveRefs resolves git facts and the mastermind transcript locator before the
// write transaction opens: repoRefFromGit shells out to git and Sessions searches
// the disk, and holding the write lock across either starves every other writer.
func resolveRefs(ctx context.Context, deps Deps, b store.Binding, kind string) (*store.RepoRef, string) {
	ref := b.RepoRef
	if ref == nil && deps.Git != nil {
		ref = repoRefFromGit(ctx, deps.Git, b.CWD)
		if ref == nil && b.Repo != "" {
			ref = repoRefFromGit(ctx, deps.Git, b.Repo)
		}
	}
	locator := b.MasterMind.TranscriptLocator
	if locator == "" && deps.Sessions != nil && b.MasterMind.SessionID != "" && kind == "live" {
		if p, ok := deps.Sessions(b.MasterMind.Kind, b.MasterMind.SessionID); ok {
			locator = p
		}
	}
	return ref, locator
}

// ingestRun carries one Ingest call's inputs and accumulated stats.
type ingestRun struct {
	src     Source
	b       store.Binding
	members map[string]bool
	kind    string
	ref     *store.RepoRef
	locator string
	deps    Deps
	logger  *slog.Logger
	stats   Stats
}

func (r *ingestRun) run(tx *db.Tx) (Stats, error) {
	repoID, err := r.upsertRepo(tx)
	if err != nil {
		return Stats{}, err
	}
	mastermindID, err := r.upsertMasterMind(tx)
	if err != nil {
		return Stats{}, err
	}
	log, err := r.readLog(tx)
	if err != nil {
		return Stats{}, err
	}
	bindingID, err := r.upsertBinding(tx, repoID, mastermindID, log)
	if err != nil {
		return Stats{}, err
	}
	all, err := r.appendEvents(tx, bindingID, log)
	if err != nil {
		return Stats{}, err
	}
	roundIDs, err := r.upsertRounds(tx, bindingID, all)
	if err != nil {
		return Stats{}, err
	}
	if err := linkEventsToRounds(tx, bindingID, roundIDs); err != nil {
		return Stats{}, fmt.Errorf("link events to rounds: %w", err)
	}
	if err := r.appendMasterMindTranscript(tx, mastermindID); err != nil {
		return Stats{}, err
	}

	stats := r.stats
	if stats.Rounds > 0 || stats.Events > 0 || stats.Artifacts > 0 || stats.TranscriptRecords > 0 {
		stats.Bindings = 1
	}
	return stats, nil
}
