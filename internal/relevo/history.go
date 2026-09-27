package relevo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/histq"
)

// ErrNoDatabase is Bindings's error when rt.DB is nil: the caller (the ui's
// "all" scope, today) has no database to read past bindings from and must
// fall back to live-only, not fail.
var (
	ErrNoDatabase = errors.New("relevo: no database")

	// ErrBadSince is histq.ErrBadSince. ParseSince's body moved to
	// internal/histq so the query language can share it; the alias keeps
	// errors.Is(err, ErrBadSince) true for every caller written before.
	ErrBadSince = histq.ErrBadSince
)

// ParseSince turns "" (zero: no cut), "24h", "7d" or "2026-09-01" into
// the instant before which entries are ignored. The body lives in
// internal/histq now, shared with `relevo history -q`; this wrapper is what
// every existing caller and test in this package keeps using.
func ParseSince(s string, now time.Time) (time.Time, error) { return histq.ParseSince(s, now) }

// HistoryBinding is one binding, shaped for the ui's rail: enough to render
// a row (name, round count, last activity, feature, repo, final state,
// archived facts) without the caller touching internal/db directly. Live is
// left false here -- the ui sets it once it knows which names are also in
// the live report (docs/specs/2026-09-20-persistence-design.md §5.8).
type HistoryBinding struct {
	Name         string
	ID           string
	Rounds       int
	LastActivity time.Time
	Feature      string
	Repo         string
	FinalState   string
	Archived     bool
	ArchivedAt   time.Time
	Live         bool
}

// Bindings lists every binding rt.DB has recorded, newest activity first,
// optionally narrowed to the repo here belongs to. rt.DB == nil reports
// ErrNoDatabase. When here != "", it resolves through rt.Git.RepoFacts
// exactly as HistoryOptions.Filter does -- except here a cwd that is not a
// git repository (or a nil rt.Git) means no filter at all, not an error:
// the ui's "all" scope wants every binding when it cannot place the cwd in
// a repo, the way HistoryOptions.Filter's CLI caller instead wants to fail
// loud on an unresolvable --here.
func Bindings(ctx context.Context, rt Runtime, here string) ([]HistoryBinding, error) {
	if rt.DB == nil {
		return nil, ErrNoDatabase
	}

	f := db.Filter{}
	if here != "" && rt.Git != nil {
		if originURL, commonDir, err := rt.Git.RepoFacts(ctx, here); err == nil {
			normalised := git.NormalizeOriginURL(originURL)
			if normalised != "" {
				f.Repo = normalised
			} else {
				f.Repo = commonDir
			}
		}
	}

	rows, err := rt.DB.Bindings(f)
	if err != nil {
		return nil, err
	}

	out := make([]HistoryBinding, 0, len(rows))
	for _, r := range rows {
		hb := HistoryBinding{
			Name:         r.Name,
			ID:           r.ID,
			Rounds:       r.Rounds,
			LastActivity: r.LastActivity,
		}
		if r.Feature != nil {
			hb.Feature = *r.Feature
		}
		switch {
		case r.RepoOrigin != nil:
			hb.Repo = *r.RepoOrigin
		case r.RepoCommonDir != nil:
			hb.Repo = *r.RepoCommonDir
		}
		if r.FinalState != nil {
			hb.FinalState = *r.FinalState
		}
		if r.ArchivedAt != nil {
			hb.Archived = true
			hb.ArchivedAt = *r.ArchivedAt
		}
		out = append(out, hb)
	}
	return out, nil
}

// HistoryOptions is `relevo history`'s flags, translated to a db.Filter by
// Filter. The zero value asks for every round, newest first.
type HistoryOptions struct {
	// Here is a cwd to resolve into a repo filter; "" means no such
	// resolution is wanted (Repo, if set, is used as-is).
	Here                                                                       string
	Repo                                                                       string
	Feature, Binding, MasterMind, Harness, Provider, Model, Candidate, Outcome string
	// Since/Until are relevo.ParseSince forms: "", "24h", "7d", "YYYY-MM-DD".
	Since, Until string
	// Archived: nil means both; true archived only; false live only.
	Archived *bool
	// Limit: 0 means all rows; the CLI layer defaults it to 200.
	Limit int
	// Query is the -q text; Filter parses it before applying the flags.
	Query string
	// Names resolves a candidate filter given as a name to its canonical
	// token, so `history --candidate <name>` matches stored rounds (A1 §4.2).
	// A nil set leaves a name filter as typed.
	Names *candidate.Set
	// By is the --by axis; it overrides a by: in Query the same way every
	// other flag does.
	By string
	// parsed is what Filter made of Query, with the flag overrides applied,
	// so the caller can read the regroup axis off ParsedQuery.
	parsed histq.Query
}

// ParsedQuery returns the histq.Query Filter built from Query, with the
// flag overrides applied -- the regroup axis lives there. It is the zero
// Query until Filter has run.
func (o HistoryOptions) ParsedQuery() histq.Query { return o.parsed }

// Filter resolves o into a db.Filter, merging the -q query with the flags.
// The query is parsed first; every explicit flag (non-empty, or a non-nil
// Archived) then sets the field it names, and a flag that disagrees with a
// value the query set appends `note: --<flag> overrides <key>:<value> from
// -q` to the returned notes, which the CLI prints to stderr. Here, when set,
// is resolved via rt.Git.RepoFacts into the repo's normalised origin URL
// when it has a remote, else its common dir -- errors when Here is not a git
// repository. Since/Until go through ParseSince. Newest is always true:
// `relevo history` prints newest first.
func (o *HistoryOptions) Filter(ctx context.Context, rt Runtime, now time.Time) (db.Filter, []string, error) {
	q := histq.Query{By: histq.AxisNone}
	if strings.TrimSpace(o.Query) != "" {
		parsed, err := histq.ParseAt(o.Query, now)
		if err != nil {
			return db.Filter{}, nil, err
		}
		q = parsed
	}
	o.parsed = q

	f := q.Filter
	f.Newest = true

	var notes []string
	note := func(flag, queryValue string) {
		notes = append(notes, fmt.Sprintf("note: --%s overrides %s:%s from -q", flag, flag, queryValue))
	}
	set := func(flag, queryValue, flagValue string, dst *string) {
		if flagValue == "" {
			return
		}
		if queryValue != "" && queryValue != flagValue {
			note(flag, queryValue)
		}
		*dst = flagValue
	}

	set("repo", q.Filter.Repo, o.Repo, &f.Repo)
	set("feature", q.Filter.Feature, o.Feature, &f.Feature)
	set("binding", q.Filter.Binding, o.Binding, &f.Binding)
	set("mastermind", q.Filter.MasterMind, o.MasterMind, &f.MasterMind)
	set("harness", q.Filter.Harness, o.Harness, &f.Harness)
	set("provider", q.Filter.Provider, o.Provider, &f.Provider)
	set("model", q.Filter.Model, o.Model, &f.Model)
	set("candidate", q.Filter.Candidate, o.Candidate, &f.Candidate)
	set("outcome", q.Filter.Outcome, o.Outcome, &f.Outcome)

	if o.By != "" {
		a, ok := histq.ParseAxis(o.By)
		if !ok {
			return db.Filter{}, nil, fmt.Errorf("--by %q: want one of %s", o.By, axisList())
		}
		if q.By != histq.AxisNone && string(q.By) != o.By {
			note("by", string(q.By))
		}
		q.By = a
		o.parsed = q
	}

	if o.Since != "" {
		if q.Since != "" && q.Since != o.Since {
			note("since", q.Since)
		}
		ts, err := ParseSince(o.Since, now)
		if err != nil {
			return db.Filter{}, nil, err
		}
		f.Since = ts
	}
	if o.Until != "" {
		if q.Until != "" && q.Until != o.Until {
			note("until", q.Until)
		}
		ts, err := ParseSince(o.Until, now)
		if err != nil {
			return db.Filter{}, nil, err
		}
		f.Until = ts
	}
	if o.Archived != nil {
		if q.Filter.Archived != nil && *q.Filter.Archived != *o.Archived {
			note("archived", strconv.FormatBool(*q.Filter.Archived))
		}
		f.Archived = o.Archived
	}

	if o.Here != "" {
		if rt.Git == nil {
			return db.Filter{}, nil, fmt.Errorf("--here: %s: not a git repository", o.Here)
		}
		originURL, commonDir, err := rt.Git.RepoFacts(ctx, o.Here)
		if err != nil {
			return db.Filter{}, nil, fmt.Errorf("--here: %s: not a git repository: %w", o.Here, err)
		}
		normalised := git.NormalizeOriginURL(originURL)
		if normalised != "" {
			f.Repo = normalised
		} else {
			f.Repo = commonDir
		}
	}

	// A candidate filter given as a name resolves to its canonical token, so
	// the stored rounds match; an unresolved value is left as typed, and
	// matches no rounds, as today (A1 §4.2).
	if o.Names != nil && f.Candidate != "" && !strings.Contains(f.Candidate, "/") {
		if c, err := o.Names.Resolve(f.Candidate); err == nil {
			f.Candidate = c.Ref().String()
		}
	}

	f.Limit = o.Limit
	return f, notes, nil
}

// axisList is histq's ten axes as one comma-separated list, for --by errors.
func axisList() string {
	axes := histq.Axes()
	names := make([]string, len(axes))
	for i, a := range axes {
		names[i] = string(a)
	}
	return strings.Join(names, ", ")
}
