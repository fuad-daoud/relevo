package relevo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// remoteFetch is what the fetch half read from the server for one binding,
// without the state lock, for the apply half to act on under it.
type remoteFetch struct {
	Name    string             // b.Name at snapshot
	Server  string             // b.Builder.Server at snapshot
	Round   int                // b.Round at snapshot
	View    remote.BindingView // GetBinding's result; zero when Err != nil
	Err     error              // GetBinding's error, classified by the apply half exactly as today
	Log     *logMirror         // nil: nothing fetched to write (not running, legacy log, error, no growth)
	Legacy  bool               // the round's log is the legacy file form: apply runs the legacy mirror inline
	Drift   []byte             // nil: nothing fetched
	CatchUp *catchUpFetch      // nil: no catch-up was fetched
	// Settle is what the apply half still owes once the lock is released: the
	// closed round's ack, which must not run under the lock, and the report the
	// ack gates. The apply half writes it; the caller that held the lock runs it
	// (settleCatchUp, or settleCatchUpInline when it still holds the lock).
	Settle *catchUpAck
}

// release drops the catch-up's unrenamed temp files, if the fetch made one.
func (f *remoteFetch) release() {
	if f == nil {
		return
	}
	f.CatchUp.release()
}

// logMirror is one fetched update to the round's mirrored builder log row.
type logMirror struct {
	Path string // rt.Store.BuilderLogPath(name, round)
	Base int64  // local length the range read started from; the apply half writes only if the
	// row still has exactly this length. -1 means Body is a full replacement.
	Body []byte // bytes to append (Base >= 0) or the whole log (Base == -1); never empty when Log != nil
}

// fetchRemote reads everything the next apply needs from the server for one
// remote binding, without touching the state lock. A failed read leaves that
// field empty and logs exactly as the inline mirror paths did, so the apply
// half decides on a best-effort snapshot and never fails the tick on it.
func fetchRemote(ctx context.Context, rt Runtime, b store.Binding) remoteFetch {
	f := remoteFetch{Name: b.Name, Server: b.Builder.Server, Round: b.Round}
	if rt.Remote == nil {
		return f
	}

	view, err := rt.Remote.GetBinding(ctx, f.Server, f.Name)
	if err != nil {
		f.Err = err
		return f
	}
	f.View = view
	if view.RoundState == remote.RoundClosed && view.ClosedRound >= f.Round {
		f.CatchUp = fetchCatchUp(ctx, rt, b, view)
		return f
	}
	if view.RoundState == remote.RoundIdle && view.ClosedRound >= f.Round &&
		reportMissingForRound(rt, f.Name, f.Round) {
		f.CatchUp = fetchCatchUp(ctx, rt, b, view)
		return f
	}
	if view.RoundState != remote.RoundRunning {
		return f
	}

	f.Log, f.Legacy = fetchLogMirror(ctx, rt, f.Server, f.Name, f.Round)
	f.Drift = fetchDrift(ctx, rt, f.Server, f.Name, f.Round)
	return f
}

// reportMissingForRound is the fetch half's unlocked read of the log, used to
// decide whether an idle round still needs its report collected. A read error
// answers false: the locked check in the apply half has the final say.
func reportMissingForRound(rt Runtime, name string, round int) bool {
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		return false
	}
	return !HasEntry(entries, round, store.DirToPlanner, store.KindReport)
}

// fetchLogMirror is the network half of the DB-branch builder-log mirror: it
// reads the row's current length, range-reads the bytes the server holds past
// it, and returns the update the apply half should write. legacy reports that
// the round keeps its log in the old on-disk form, which the apply half mirrors
// inline instead.
func fetchLogMirror(ctx context.Context, rt Runtime, server, name string, round int) (*logMirror, bool) {
	path := rt.Store.BuilderLogPath(name, round)
	if legacyLog(rt, name, round) {
		return nil, true
	}

	cur, err := rt.Store.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cur = nil
		} else {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			return nil, false
		}
	}
	local := int64(len(cur))

	rc, fr, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", local)
	if err != nil || rc == nil {
		if err != nil {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
		}
		return nil, false
	}

	readBounded := func(r io.Reader) ([]byte, bool) {
		body, err := io.ReadAll(io.LimitReader(r, maxMirrorBytes+1))
		if err != nil {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			return nil, false
		}
		if int64(len(body)) > maxMirrorBytes {
			slog.Warn("mirror builder log over cap; not stored", "server", server, "name", name, "round", round)
			return nil, false
		}
		return body, true
	}

	switch {
	case fr.Honored && fr.From == local && fr.Size >= local:
		defer rc.Close()
		body, ok := readBounded(rc)
		// An empty body means the row did not grow: rewriting it with the
		// same bytes would put the whole blob back for no change.
		if !ok || len(body) == 0 {
			return nil, false
		}
		if int64(len(cur))+int64(len(body)) > maxMirrorBytes {
			slog.Warn("mirror builder log over cap; not stored", "server", server, "name", name, "round", round)
			return nil, false
		}
		return &logMirror{Path: path, Base: local, Body: body}, false
	case fr.Honored && fr.Size < local:
		_ = rc.Close()
		rc2, _, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", 0)
		if err != nil || rc2 == nil {
			if err != nil {
				slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			}
			return nil, false
		}
		defer rc2.Close()
		body, ok := readBounded(rc2)
		if !ok {
			return nil, false
		}
		return &logMirror{Path: path, Base: -1, Body: body}, false
	default:
		defer rc.Close()
		body, ok := readBounded(rc)
		if !ok {
			return nil, false
		}
		return &logMirror{Path: path, Base: -1, Body: body}, false
	}
}

// fetchDrift is the network half of the drift mirror: it returns the round's
// drift bytes, or nil when there is nothing to fetch. Its guards match the
// inline version -- an already stored drift is not re-read, and only the first
// attempt of a round reaches the server.
func fetchDrift(ctx context.Context, rt Runtime, server, name string, round int) []byte {
	driftPath := rt.Store.DriftPath(name, round)
	if _, err := rt.Store.ReadFile(driftPath); err == nil {
		return nil
	}
	key := fmt.Sprintf("%s/%d", name, round)
	if _, loaded := mirrorDriftAttempts.LoadOrStore(key, struct{}{}); loaded {
		return nil
	}
	rc, err := rt.Remote.RoundFile(ctx, server, name, round, "drift")
	if err != nil || rc == nil {
		if err != nil {
			slog.Debug("mirror drift not available", "server", server, "name", name, "round", round, "err", err)
		}
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		slog.Warn("mirror drift read failed", "server", server, "name", name, "round", round, "err", err)
		return nil
	}
	return data
}

// matches reports whether b under the lock is still the binding f was fetched
// for: same identity and round, still remote, and in a known live state. A
// false answer means the fetch is stale and the apply must change nothing.
func (f remoteFetch) matches(b store.Binding) bool {
	return b.Name == f.Name &&
		b.Builder.Remote() &&
		b.Builder.Server == f.Server &&
		b.Round == f.Round &&
		b.State != store.StateDone &&
		b.State != store.StatePaused &&
		store.KnownState(b.State)
}

// applyLogMirror writes one fetched log update under tx. A Base of -1 is a full
// replacement; otherwise the row must still hold exactly Base bytes, or the row
// moved since the fetch and the next tick re-reads it.
func applyLogMirror(rt Runtime, tx *store.Tx, name string, round int, m *logMirror) {
	if m == nil {
		return
	}
	if m.Base < 0 {
		if err := tx.PutRoundFile(name, round, m.Path, m.Body); err != nil {
			slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		}
		return
	}

	cur, err := rt.Store.ReadFile(m.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
			return
		}
		cur = nil
	}
	if int64(len(cur)) != m.Base {
		slog.Debug("mirror builder log row moved since fetch; skipping", "name", name, "round", round)
		return
	}
	full := append(cur, m.Body...)
	if int64(len(full)) > maxMirrorBytes {
		slog.Warn("mirror builder log over cap; not stored", "name", name, "round", round)
		return
	}
	if err := tx.PutRoundFile(name, round, m.Path, full); err != nil {
		slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
	}
}

// applyDrift writes one fetched drift under tx; a nil data means the fetch
// found nothing to store.
func applyDrift(rt Runtime, tx *store.Tx, name string, round int, data []byte) {
	if data == nil {
		return
	}
	driftPath := rt.Store.DriftPath(name, round)
	if err := tx.PutRoundFile(name, round, driftPath, data); err != nil {
		slog.Warn("write drift round file failed", "path", driftPath, "err", err)
	}
}

// catchUpFetch is the round-close download a fetch made without the lock: the
// report, diff, log and stream files, and the bundle's outcome. The apply half
// installs it under the lock with the steps the inline catch-up used to run.
type catchUpFetch struct {
	Round         int    // view.ClosedRound
	Abort         bool   // a non-404 read failed: the apply returns b unchanged (retry next tick)
	ReportMissing bool   // RoundFile(report) answered 404
	ReportTemp    string // temp file holding the report, "" if none
	Diff          []byte // nil: none (404, or over cap -- logged at fetch as today)
	Log           []byte // DB-form log body; nil: none
	LogTemp       string // legacy-form log in a temp file, "" if none
	StreamTemp    string // temp file holding the stream, "" if none
	CheckedOut    bool   // absorb or adopted-branch update hit "checked out": quiet retry
	AbsorbErr     error  // other absorb / UpdateRef failure: apply counts it (halt at 10)
	Fatal         error  // RefSHA failure after absorb: apply returns it as today
}

// release removes the fetch's temp files that no rename consumed. It is
// nil-safe and idempotent, so a caller can defer it before knowing whether a
// catch-up was fetched at all.
func (cf *catchUpFetch) release() {
	if cf == nil {
		return
	}
	for _, path := range []string{cf.ReportTemp, cf.LogTemp, cf.StreamTemp} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

// is404 reports whether err is the server's answer that a round file does not
// exist, which every read in the catch-up takes as "nothing to fetch".
func is404(err error) bool {
	var httpErr *client.HTTPError
	return errors.As(err, &httpErr) && httpErr.Status == 404
}

// downloadTemp writes r into a new temp file beside final and returns its
// path, leaving the rename into place to the apply half. A discarded fetch
// removes it, so the final name never holds a file the apply did not accept.
func downloadTemp(final string, r io.ReadCloser) (string, error) {
	defer r.Close()
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(final)+".fetch.*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return tmpName, nil
}

// checkedOut reports whether err is git's refusal to touch a branch that is
// checked out somewhere, which the catch-up answers with a quiet retry.
func checkedOut(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "checked out")
}

// warnCheckedOut logs the checkout hint once per process, then only at Debug.
func warnCheckedOut(name, branch string) {
	if _, seen := checkedOutWarned.LoadOrStore(name, struct{}{}); !seen {
		slog.Info("checkout another branch, then relevo wait", "binding", name, "branch", branch)
	} else {
		slog.Debug("still checked out", "binding", name, "branch", branch)
	}
}

// fetchCatchUp runs the round-close catch-up's downloads and absorb without
// the state lock. It writes temp files or memory and changes no binding: the
// apply half installs what came back and owns the report, ack and bookkeeping.
func fetchCatchUp(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView) *catchUpFetch {
	cf := &catchUpFetch{Round: view.ClosedRound}
	if !fetchCatchUpFiles(ctx, rt, b, view, cf) {
		return cf
	}
	fetchCatchUpBundle(ctx, rt, b, view, cf)
	return cf
}

// fetchCatchUpFiles downloads the round's report, diff, log and stream. It
// reports whether the fetch should go on: false when a non-404 read failed
// (Abort set, temps dropped) or when a missing report means the apply halts.
func fetchCatchUpFiles(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	if !fetchCatchUpReport(ctx, rt, b, view, cf) ||
		!fetchCatchUpDiff(ctx, rt, b, view, cf) ||
		!fetchCatchUpLog(ctx, rt, b, view, cf) {
		return false
	}
	return fetchCatchUpStream(ctx, rt, b, view, cf)
}

// fetchCatchUpReport fetches the report into a temp file. A 404 on a stopped
// round continues without one (the apply writes the stopped payload); on any
// other close it stops the fetch so the apply can halt.
func fetchCatchUpReport(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	server, name, n := b.Builder.Server, b.Name, view.ClosedRound
	rc, err := rt.Remote.RoundFile(ctx, server, name, n, "report")
	switch {
	case is404(err):
		cf.ReportMissing = true
		return view.Stopped != ""
	case err != nil:
		slog.Warn("fetch report failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return false
	default:
		path := rt.Store.ReportPath(name, n)
		cf.ReportTemp, err = downloadTemp(path, rc)
		if err != nil {
			slog.Warn("write report failed", "path", path, "err", err)
			cf.Abort = true
			cf.release()
			return false
		}
		return true
	}
}

// fetchCatchUpDiff fetches the round's patch into memory, unless it is over
// the patch cap, which is logged and stored as nothing exactly as before.
func fetchCatchUpDiff(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	server, name, n := b.Builder.Server, b.Name, view.ClosedRound
	rc, err := rt.Remote.RoundFile(ctx, server, name, n, "diff")
	switch {
	case is404(err):
		return true
	case err != nil:
		slog.Warn("fetch diff failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return false
	default:
		body, rerr := io.ReadAll(io.LimitReader(rc, git.DefaultMaxPatchBytes+1))
		_ = rc.Close()
		if rerr != nil {
			slog.Warn("read diff failed", "server", server, "name", name, "round", n, "err", rerr)
			cf.Abort = true
			cf.release()
			return false
		}
		if int64(len(body)) > git.DefaultMaxPatchBytes {
			slog.Warn("remote diff over cap; not stored", "server", server, "name", name, "round", n)
			return true
		}
		cf.Diff = body
		return true
	}
}

// fetchCatchUpLog fetches the round's log: into a temp file for the legacy
// on-disk form, into memory for the DB form, over the mirror cap into neither.
func fetchCatchUpLog(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	server, name, n := b.Builder.Server, b.Name, view.ClosedRound
	rc, err := rt.Remote.RoundFile(ctx, server, name, n, "log")
	switch {
	case is404(err):
		return true
	case err != nil:
		slog.Warn("fetch log failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return false
	default:
		path := rt.Store.BuilderLogPath(name, n)
		if legacyLog(rt, name, n) {
			cf.LogTemp, err = downloadTemp(path, rc)
			if err != nil {
				slog.Warn("write log failed", "path", path, "err", err)
				cf.Abort = true
				cf.release()
				return false
			}
			return true
		}
		body, rerr := io.ReadAll(io.LimitReader(rc, maxMirrorBytes+1))
		_ = rc.Close()
		if rerr != nil {
			slog.Warn("write log failed", "path", path, "err", rerr)
			cf.Abort = true
			cf.release()
			return false
		}
		if int64(len(body)) > maxMirrorBytes {
			slog.Warn("remote log over cap; not stored", "server", server, "name", name, "round", n)
			return true
		}
		cf.Log = body
		return true
	}
}

// fetchCatchUpStream fetches the harness's own round record into a temp file.
// A server that serves no stream file answers 404, which is fine.
func fetchCatchUpStream(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	server, name, n := b.Builder.Server, b.Name, view.ClosedRound
	rc, err := rt.Remote.RoundFile(ctx, server, name, n, "stream")
	switch {
	case is404(err):
		return true
	case err != nil:
		slog.Warn("fetch stream failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return false
	default:
		path := rt.Store.BuilderStreamPath(name, n)
		cf.StreamTemp, err = downloadTemp(path, rc)
		if err != nil {
			slog.Warn("write stream failed", "path", path, "err", err)
			cf.Abort = true
			cf.release()
			return false
		}
		return true
	}
}

// fetchCatchUpBundle absorbs the round bundle and brings an adopted branch to
// the absorbed result, both without the lock. A discarded fetch leaves
// LastKnown unchanged, so the next catch-up asks for the same bundle again.
func fetchCatchUpBundle(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) {
	n := view.ClosedRound
	server, name := b.Builder.Server, b.Name
	rcBundle, err := rt.Remote.RoundBundle(ctx, server, name, n, b.Builder.LastKnown)
	if err != nil {
		slog.Warn("fetch round bundle failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return
	}
	if rcBundle == nil {
		return
	}
	defer rcBundle.Close()

	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	// The server always cuts its own relevo/<name> branch and ships that, so a
	// binding that adopted a branch keeps the adopted name locally: the
	// allow-list names the server's ref, and the adopted branch is
	// fast-forwarded to the absorbed result below.
	serverRef := "refs/heads/relevo/" + name
	refs := []string{serverRef}
	if view.DirtyCommit != "" {
		refs = append(refs, fmt.Sprintf("refs/relevo/%s/round-%d", name, n))
	}
	if _, err := rt.Transport.Absorb(ctx, b.Repo, remote.ContentTypeGitBundle, rcBundle, refs); err != nil {
		if checkedOut(err) {
			warnCheckedOut(name, b.Branch)
			cf.CheckedOut = true
			return
		}
		cf.AbsorbErr = fmt.Errorf("%s: cannot absorb round %d from %s: %s", name, n, server, err.Error())
		return
	}
	checkedOutWarned.Delete(name)

	if serverRef == branchRef {
		return
	}
	sha, ok, err := rt.Git.RefSHA(ctx, b.Repo, serverRef)
	if err != nil {
		cf.Fatal = fmt.Errorf("resolve server branch %s after absorb: %w", serverRef, err)
		return
	}
	if !ok {
		cf.Fatal = fmt.Errorf("server branch %s missing after absorb", serverRef)
		return
	}
	old, _, _ := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err := rt.Git.UpdateRef(ctx, b.Repo, branchRef, sha, old); err != nil {
		if checkedOut(err) {
			warnCheckedOut(name, b.Branch)
			cf.CheckedOut = true
			return
		}
		cf.AbsorbErr = fmt.Errorf("%s: cannot fast-forward %s to round %d from %s: %s", name, b.Branch, n, server, err.Error())
		return
	}
}

// applyCatchUp installs a fetched catch-up under tx in the order the inline
// catch-up ran: abort, a missing report's halt, the downloaded files, the
// absorb outcome, then the settle the caller owes once it gives the lock up.
func applyCatchUp(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (store.Binding, *catchUpAck, error) {
	if cf.Abort {
		return b, nil, nil
	}
	if cf.ReportMissing && view.Stopped == "" {
		next, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s closed round %d without a report file", b.Name, b.Builder.Server, view.ClosedRound))
		return next, nil, err
	}
	if !applyCatchUpFiles(rt, tx, b, view, cf) {
		return b, nil, nil
	}
	if next, stop, err := applyCatchUpAbsorb(ctx, rt, b, cf); stop {
		return next, nil, err
	}
	b.Builder.LastKnown = view.ResultCommit
	b.RemoteAbsorbFailures = 0
	return b, &catchUpAck{
		Server:       b.Builder.Server,
		Name:         b.Name,
		Round:        view.ClosedRound,
		BindingRound: b.Round,
		View:         view,
		HaveReport:   cf.ReportTemp != "",
		HaveDiff:     cf.Diff != nil,
	}, nil
}
