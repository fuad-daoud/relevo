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
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/housekeep"
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
	// Unconfirmed marks a failure the tick's prefetch counted as the first in
	// a row: one failed read is the noise of a redeploy or a tunnel flap, so
	// the apply half returns the binding unchanged. A confirming failure
	// instead carries EpisodeStart, and an in-episode probe rejoins it too; the
	// inline fetch sets neither.
	Unconfirmed bool
	// EpisodeStart is the outage episode's start -- the first failure's time --
	// set by the tick's prefetch for a confirming failure, an in-episode probe
	// and a skipped fetch; the apply half stamps it. Zero for an unconfirmed
	// failure and for
	// the inline fetch, which stamps the time it read instead.
	EpisodeStart time.Time
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

	f.Observe = chain.ServerMemberActive(rt.Store, b.Name)

	headCtx, cancel := boundedWait(ctx, head)
	view, err := rt.Remote.GetBinding(headCtx, f.Server, f.Name)
	cancel()
	if err != nil {
		f.Err = err
		return f
	}
	f.View = view
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

// warnClose closes c and warns when that fails: a fetch body already read to
// its end, whose close error the fetch cannot return since the bytes already
// decided the outcome.
func warnClose(c io.Closer, msg string, args ...any) {
	if err := c.Close(); err != nil {
		slog.Warn(msg, append(args, "err", err)...)
	}
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
		// The server may hold no bytes past the local mirror yet, so a
		// missing log is the normal answer on a sync, not an error.
		if err != nil && !remoteNotFound(err) {
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
		defer warnClose(rc, "mirror builder log close failed", "server", server, "name", name, "round", round)
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
		defer warnClose(rc2, "mirror builder log close failed", "server", server, "name", name, "round", round)
		body, ok := readBounded(rc2)
		if !ok {
			return nil
		}
		return &logMirror{Path: path, Base: -1, Body: body}
	default:
		defer warnClose(rc, "mirror builder log close failed", "server", server, "name", name, "round", round)
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
	defer warnClose(rc, "mirror drift close failed", "server", server, "name", name, "round", round)
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
	if err := housekeep.EnsureRealDir(root, filepath.Dir(m.Path)); err != nil {
		slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		return
	}
	f, err := housekeep.OpenAppendRegular(m.Path)
	if err != nil {
		slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		return
	}
	defer warnClose(f, "mirror builder log close failed", "name", name, "round", round)
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
