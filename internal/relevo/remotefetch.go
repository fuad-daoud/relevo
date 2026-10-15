package relevo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// remoteFetch is what the fetch half read from the server for one binding,
// without the state lock, for the apply half to act on under it.
type remoteFetch struct {
	Name   string             // b.Name at snapshot
	Server string             // b.Builder.Server at snapshot
	Round  int                // b.Round at snapshot
	View   remote.BindingView // GetBinding's result; zero when Err != nil
	Err    error              // GetBinding's error, classified by the apply half exactly as today
	// Observe is set for a member of a chain that runs on a server. Such a
	// member observes the live round only: the fetch reads no catch-up, whose
	// install and ack the chain pull owns, and the apply half writes the status
	// word and stops short of the halt and settle a close would drive.
	Observe bool
	Log     *logMirror    // nil: nothing fetched to write (not running, error, no growth)
	Drift   []byte        // nil: nothing fetched
	CatchUp *catchUpFetch // nil: no catch-up was fetched
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

// logMirror is one fetched update to the running round's mirrored builder log.
//
// The mirror of a running round is a file in the binding directory, not a
// round_file row: a row is re-encoded whole on every write, so appending to one
// costs the whole log per tick, and every rewrite is exported to sync as a new
// full body. The round's close stores the log as a row once and removes the
// file; a log the close never fetches is left for the seal pass.
type logMirror struct {
	Path string // rt.Store.BuilderLogPath(name, round)
	Base int64  // file length the range read started from; the apply half writes only if the
	// file still has exactly this length. -1 means Body is a full replacement.
	Body []byte // bytes to append (Base >= 0) or the whole log (Base == -1); never empty when Log != nil
}

// fetchRemote reads everything the next apply needs from the server for one
// remote binding, without touching the state lock. A failed read leaves that
// field empty and logs exactly as the inline mirror paths did, so the apply
// half decides on a best-effort snapshot and never fails the tick on it.
func fetchRemote(ctx context.Context, rt Runtime, b store.Binding) remoteFetch {
	return fetchRemoteWithin(ctx, rt, b, 0)
}

// fetchRemoteWithin is fetchRemote with the first read of the binding bounded
// by head; zero means unbounded.
func fetchRemoteWithin(ctx context.Context, rt Runtime, b store.Binding, head time.Duration) remoteFetch {
	f := remoteFetch{Name: b.Name, Server: b.Builder.Server, Round: b.Round}
	if rt.Remote == nil {
		return f
	}

	f.Observe = serverChainMemberActive(rt.Store, b.Name)

	headCtx, cancel := boundedWait(ctx, head)
	view, err := rt.Remote.GetBinding(headCtx, f.Server, f.Name)
	cancel()
	if err != nil {
		f.Err = err
		return f
	}
	f.View = view
	// An observing member never fetches a catch-up: the chain pull is the one
	// collector that installs its closed rounds and acks them.
	//
	// A close is collected by the fact that the server named a ClosedRound at
	// or past this round, not by the state word that rides with it. The words
	// are not exclusive: a round that closed and then halted again is both, and
	// a needs_you or idle word on top of a close must not cost the client the
	// round's report. A running or queued round has no close to collect, since
	// ClosedRound only moves on a close.
	if !f.Observe && view.ClosedRound >= f.Round &&
		view.RoundState != remote.RoundRunning && view.RoundState != remote.RoundQueued &&
		reportMissingForRound(rt, f.Name, f.Round) {
		f.CatchUp = fetchCatchUp(ctx, rt, b, view)
		return f
	}
	if view.RoundState != remote.RoundRunning {
		return f
	}

	f.Log = fetchLogMirror(ctx, rt, f.Server, f.Name, f.Round)
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
	return !HasEntry(entries, round, store.DirToMasterMind, store.KindReport)
}

// fetchLogMirror is the network half of the builder-log mirror: it reads the
// mirror file's length, range-reads the bytes the server holds past it, and
// returns the update the apply half should write.
func fetchLogMirror(ctx context.Context, rt Runtime, server, name string, round int) *logMirror {
	path := rt.Store.BuilderLogPath(name, round)
	var local int64
	if fi, err := os.Stat(path); err == nil {
		local = fi.Size()
	} else if !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
		return nil
	}

	rc, fr, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", local)
	if err != nil || rc == nil {
		if err != nil {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
		}
		return nil
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
		if !ok || len(body) == 0 {
			return nil
		}
		if local+int64(len(body)) > maxMirrorBytes {
			slog.Warn("mirror builder log over cap; not stored", "server", server, "name", name, "round", round)
			return nil
		}
		return &logMirror{Path: path, Base: local, Body: body}
	case fr.Honored && fr.Size < local:
		_ = rc.Close()
		rc2, _, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", 0)
		if err != nil || rc2 == nil {
			if err != nil {
				slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			}
			return nil
		}
		defer rc2.Close()
		body, ok := readBounded(rc2)
		if !ok {
			return nil
		}
		return &logMirror{Path: path, Base: -1, Body: body}
	default:
		defer rc.Close()
		body, ok := readBounded(rc)
		if !ok {
			return nil
		}
		return &logMirror{Path: path, Base: -1, Body: body}
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

// applyLogMirror writes one fetched log update to the mirror file; the caller
// holds the state lock, which orders it against the close that replaces the
// file with a row. A Base of -1 is a full replacement; otherwise the file must
// still hold exactly Base bytes, or it moved since the fetch and the next tick
// re-reads it.
func applyLogMirror(rt Runtime, name string, round int, m *logMirror) {
	if m == nil {
		return
	}
	root := rt.Store.Dir(name)
	if m.Base < 0 {
		if err := writeTempAndRename(root, m.Path, bytes.NewReader(m.Body)); err != nil {
			slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		}
		return
	}

	var cur int64
	if fi, err := os.Lstat(m.Path); err == nil {
		cur = fi.Size()
	} else if !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		return
	}
	if cur != m.Base {
		slog.Debug("mirror builder log moved since fetch; skipping", "name", name, "round", round)
		return
	}
	if err := ensureRealDir(root, filepath.Dir(m.Path)); err != nil {
		slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		return
	}
	f, err := openAppendRegular(m.Path)
	if err != nil {
		slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(m.Body); err != nil {
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
// report, diff, log and stream files -- a reader's artifacts in place of the
// report and diff -- and the bundle's outcome. The apply half installs it
// under the lock with the steps the inline catch-up used to run.
type catchUpFetch struct {
	Round         int               // view.ClosedRound
	Abort         bool              // a read failed and the whole-branch retry did too: the apply returns b unchanged (retry next tick)
	BundleErr     error             // set with Abort when the bundle request itself failed: the apply counts it (halt at the budget)
	ReportMissing bool              // RoundFile(report), or the listing's output, was absent
	ReportTemp    string            // temp file holding the report or the reader's output, "" if none
	Diff          []byte            // nil: none (404, or over cap -- logged at fetch as today)
	Artifacts     []fetchedArtifact // reader artifacts awaiting the apply's rename
	ArtifactCap   string            // non-empty: the listing was over the cap; the apply halts
	Log           []byte            // log body; nil: none
	StreamTemp    string            // temp file holding the stream, "" if none
	GateTemp      string            // temp file holding the acceptance check's log, "" if none
	GatePath      string            // the path that log is installed at, "" if none was fetched
	CheckedOut    bool              // git refuses to move the branch (checked out somewhere): quiet retry
	AbsorbErr     error             // any other reset, absorb or UpdateRef failure: apply counts it (halt at 10)
	Fatal         error             // RefSHA failure after absorb: apply returns it as today
}

// release removes the fetch's temp files that no rename consumed. It is
// nil-safe and idempotent, so a caller can defer it before knowing whether a
// catch-up was fetched at all.
func (cf *catchUpFetch) release() {
	if cf == nil {
		return
	}
	for _, path := range []string{cf.ReportTemp, cf.StreamTemp, cf.GateTemp} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
	for _, a := range cf.Artifacts {
		if a.Temp != "" {
			_ = os.Remove(a.Temp)
		}
	}
}

// checkedOut reports whether err is git's refusal to touch a branch that is
// checked out somewhere, which the catch-up answers with a quiet retry. A
// branch held by any worktree counts: the reset's branch delete refuses it with
// git's own "used by worktree" wording, not "checked out".
func checkedOut(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "checked out") || strings.Contains(msg, "used by worktree")
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

// fetchCatchUpFiles downloads the round's report, diff, log and stream -- a
// reader's artifacts instead of the report and diff. It reports whether the
// fetch should go on: false when a non-404 read failed (Abort set, temps
// dropped) or when a missing report means the apply halts.
func fetchCatchUpFiles(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	// A reader round has no report file and no diff: the round's output and
	// every other file come from the artifact routes instead.
	if b.Shape == store.ShapeReader {
		if !fetchCatchUpArtifacts(ctx, rt, b, view, cf) ||
			!fetchCatchUpLog(ctx, rt, b, view, cf) {
			return false
		}
		return fetchCatchUpStream(ctx, rt, b, view, cf)
	}
	if !fetchCatchUpReport(ctx, rt, b, view, cf) ||
		!fetchCatchUpDiff(ctx, rt, b, view, cf) ||
		!fetchCatchUpLog(ctx, rt, b, view, cf) {
		return false
	}
	if !fetchCatchUpStream(ctx, rt, b, view, cf) {
		return false
	}
	return fetchCatchUpGate(ctx, rt, b, view, cf)
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
		cf.ReportTemp, err = downloadTemp(rt.Store.Dir(name), path, rc)
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

// fetchCatchUpLog fetches the round's whole log into memory, or nothing when it
// is over the mirror cap.
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
		path := rt.Store.StreamPath(name, n)
		cf.StreamTemp, err = downloadTemp(rt.Store.Dir(name), path, rc)
		if err != nil {
			slog.Warn("write stream failed", "path", path, "err", err)
			cf.Abort = true
			cf.release()
			return false
		}
		return true
	}
}

// fetchCatchUpGate fetches the closed round's acceptance-check log into a temp
// file, so the apply half can rename it into the gate log path the chain and
// the reviewer seed read. A round whose server ran no check has no gate file:
// 404 is fine and leaves no temp. The path the log is installed at is recorded
// on the fetch, so the gate record names a file that arrived rather than one
// this client may not have written.
func fetchCatchUpGate(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	server, name, n := b.Builder.Server, b.Name, view.ClosedRound
	rc, err := rt.Remote.RoundFile(ctx, server, name, n, "gate")
	switch {
	case is404(err):
		return true
	case err != nil:
		slog.Warn("fetch gate failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return false
	default:
		path := rt.Store.GateLogPath(name, n)
		cf.GatePath = path
		cf.GateTemp, err = downloadTemp(rt.Store.Dir(name), path, rc)
		if err != nil {
			slog.Warn("write gate failed", "path", path, "err", err)
			cf.Abort = true
			cf.release()
			return false
		}
		return true
	}
}

// applyCatchUp installs a fetched catch-up under tx in the order the inline
// catch-up ran: abort, a missing report's halt, the downloaded files, the
// absorb outcome, then the settle the caller owes once it gives the lock up.
func applyCatchUp(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (store.Binding, *catchUpAck, error) {
	if cf.Abort {
		if cf.BundleErr != nil {
			return applyCatchUpBundleFailure(ctx, rt, tx, b, view, cf)
		}
		return b, nil, nil
	}
	// A reader's listing was over the cap: nothing was downloaded, so the
	// round asks for a human exactly as the client-side close does.
	if cf.ArtifactCap != "" {
		next, err := haltAndSettle(ctx, rt, tx, b, cf.ArtifactCap)
		return next, nil, err
	}
	if cf.ReportMissing && view.Stopped == "" {
		missing := "without a report file"
		if b.Shape == store.ShapeReader {
			missing = fmt.Sprintf("without its %s file", readerOutputLabel(rt, b))
		}
		next, err := haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: %s closed round %d %s", b.Name, b.Builder.Server, view.ClosedRound, missing))
		return next, nil, err
	}
	if !applyCatchUpFiles(rt, tx, b, view, cf) {
		return b, nil, nil
	}
	next, stop, err := applyCatchUpAbsorb(ctx, rt, tx, b, view, cf)
	if stop {
		return next, nil, err
	}
	b = next
	b.Builder.LastKnown = view.ResultCommit
	// The pulled result commit is the round's closed tree. It is the commit the
	// absorb just brought home, and the chain's cumulative plan diff reads it
	// where a local binding's close would carry its snapshot's tree.
	b.RoundClosedTree = view.ResultCommit
	b.RemoteAbsorbFailures = 0
	b.RemoteBundleFailures = 0
	return b, &catchUpAck{
		Server:       b.Builder.Server,
		Name:         b.Name,
		Round:        view.ClosedRound,
		BindingRound: b.Round,
		View:         view,
		HaveReport:   cf.ReportTemp != "",
		HaveDiff:     cf.Diff != nil,
		GatePath:     cf.GatePath,
	}, nil
}
