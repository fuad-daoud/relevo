package relevo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

const unreachableGrace = 30 * time.Minute

// checkedOutWarned records the bindings whose "checked out" hint catchUp has
// already logged at Info in this process, so the hint does not repeat on every
// SyncRemote (#253). Process-local on purpose: the daemon and `relevo wait` are
// separate processes and each says it once.
var checkedOutWarned sync.Map // binding name -> struct{}

func writeTempAndRename(dest string, r io.Reader) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dest)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

const maxMirrorBytes = 16 << 20

func mirrorLog(ctx context.Context, rt Runtime, tx *store.Tx, server, name string, round int) {
	if legacyLog(rt, name, round) {
		path := rt.Store.BuilderLogPath(name, round)
		var local int64
		if fi, err := os.Stat(path); err == nil {
			local = fi.Size()
		}
		rc, fr, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", local)
		if err != nil || rc == nil {
			if err != nil {
				slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			}
			return
		}

		switch {
		case fr.Honored && fr.From == local && fr.Size >= local:
			defer rc.Close()
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				slog.Warn("write builder log failed", "path", path, "err", err)
				return
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				slog.Warn("write builder log failed", "path", path, "err", err)
				return
			}
			defer f.Close()
			if _, err := io.Copy(f, rc); err != nil {
				slog.Warn("write builder log failed", "path", path, "err", err)
			}
		case fr.Honored && fr.Size < local:
			_ = rc.Close()
			rc2, _, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", 0)
			if err != nil || rc2 == nil {
				if err != nil {
					slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
				}
				return
			}
			defer rc2.Close()
			if err := writeTempAndRename(path, rc2); err != nil {
				slog.Warn("write builder log failed", "path", path, "err", err)
			}
		default:
			defer rc.Close()
			if err := writeTempAndRename(path, rc); err != nil {
				slog.Warn("write builder log failed", "path", path, "err", err)
			}
		}
		return
	}

	m, _ := fetchLogMirror(ctx, rt, server, name, round)
	applyLogMirror(rt, tx, name, round, m)
}

var mirrorDriftAttempts sync.Map // "name/round" -> struct{}

// mirrorDriftOnce is the inline fetch-then-apply form of the drift mirror,
// kept for callers that hold their own lock across a one-off observation.
func mirrorDriftOnce(ctx context.Context, rt Runtime, tx *store.Tx, server, name string, round int) {
	applyDrift(rt, tx, name, round, fetchDrift(ctx, rt, server, name, round))
}

// applyRemote turns a fetched server view into the binding's new state under
// the caller's lock. It makes no network call: every value it acts on was read
// by fetchRemote before the lock was taken. deliver reports whether the caller
// should follow up with deliverAndSettle; it is false for every path that
// already returned on its own in the original function (a running mirror, a
// fresh halt, an unreachable/cert/other error).
//
// The split exists for SyncRemote: a read path must observe the server's state
// without ever calling deliverAndSettle, because a CLI one-shot has no business
// claiming a pending payload out from under the daemon.
func liveFactsOf(v *remote.LiveView) *store.LiveFacts {
	if v == nil {
		return nil
	}
	var tail []string
	if v.Tail != nil {
		tail = make([]string, len(v.Tail))
		copy(tail, v.Tail)
	}
	var u *usage.Usage
	if v.Usage != nil {
		copyU := *v.Usage
		u = &copyU
	}
	var diff *store.DiffFacts
	if v.Diff != nil {
		diff = &store.DiffFacts{
			Files:   v.Diff.Files,
			Added:   v.Diff.Added,
			Removed: v.Diff.Removed,
		}
	}
	return &store.LiveFacts{
		At:             v.At,
		PID:            v.PID,
		StartedAt:      v.StartedAt,
		ExitCode:       v.ExitCode,
		Tail:           tail,
		Usage:          u,
		PriorTokens:    v.PriorTokens,
		Diff:           diff,
		LastProgressAt: v.LastProgressAt,
		ExploringSince: v.ExploringSince,
		GatingSince:    v.GatingSince,
	}
}

func applyRemote(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, f *remoteFetch) (store.Binding, bool, error) {
	if rt.Remote == nil {
		slog.Warn("remote client not configured", "binding", b.Name)
		return b, false, nil
	}
	now := rt.Now().UTC()
	if f.Err != nil {
		return applyRemoteErr(ctx, rt, tx, b, f.Err, now)
	}
	return applyRemoteView(ctx, rt, tx, b, f, now)
}

// applyRemoteErr classifies a failed fetch exactly as the inline observe did:
// a revoked key halts at once, every other 401 gets its grace, a 404 or a
// round unreachable past its budget halts, a server this machine's config does
// not name only updates the reported status -- the round may be running fine
// there -- and anything else does the same.
func applyRemoteErr(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, err error, now time.Time) (store.Binding, bool, error) {
	server := b.Builder.Server
	name := b.Name
	var httpErr *client.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Status == 401 {
			// A revoked key is permanent: halt at once, as it always did.
			if httpErr.Body.Code == remote.CodeRevoked {
				b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s: %s", name, server, httpErr.Body.Message))
				return b, false, err
			}
			// Every other 401 -- stale, bad_signature, not_enrolled -- may
			// clear on its own: a reboot's clock skew is the common case.
			// Show it, warn once, and halt only after the grace. A CLI
			// one-shot (nil AuthGrace) never halts.
			first := rt.AuthGrace.Note(name, now)
			if first.Equal(now) {
				slog.Warn("transient auth error", "server", server, "binding", name, "code", string(httpErr.Body.Code))
			}
			b.Builder.RemoteStatus = "auth: " + string(httpErr.Body.Code)
			if rt.AuthGrace.Expired(name, now, authGraceLimit) {
				dur := now.Sub(first).Truncate(time.Second)
				b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s: %s for %s -- check this machine's clock and relevo config server list",
					name, server, httpErr.Body.Code, dur))
				return b, false, err
			}
			return b, false, nil
		}
		if httpErr.Status == 404 {
			b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s: binding removed by the server admin", name, server))
			return b, false, err
		}
	}
	if errors.Is(err, client.ErrUnreachable) {
		if b.RemoteUnreachableSince.IsZero() {
			b.RemoteUnreachableSince = now
			slog.Warn(fmt.Sprintf("%s unreachable", server), "server", server, "binding", name)
		}
		b.Builder.RemoteStatus = "unreachable"

		entries, rerr := tx.ReadLog(name)
		roundOpen := rerr == nil &&
			HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
			!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport)

		dur := now.Sub(b.RemoteUnreachableSince).Truncate(time.Second)
		if roundOpen && now.Sub(b.RemoteUnreachableSince) > roundBudget(b)+unreachableGrace {
			b, err := haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s unreachable for %s; round %d may still be running there",
				name, server, dur, b.Round))
			return b, false, err
		}
		return b, false, nil
	}
	if errors.Is(err, client.ErrCertChanged) {
		if b.Builder.RemoteStatus != "cert" {
			slog.Warn("server certificate changed", "server", server, "binding", name)
		}
		b.Builder.RemoteStatus = "cert"
		return b, false, nil
	}
	if errors.Is(err, client.ErrUnknownServer) {
		b.Builder.RemoteStatus = "unknown server"
		warnOnce(name, "unknown-server",
			name+": server "+server+" is not in this machine's config; run relevo config server list",
			"server", server, "binding", name)
		return b, false, nil
	}
	slog.Warn("remote get binding failed", "server", server, "binding", name, "err", err)
	return b, false, nil
}

// applyRemoteView applies a successful fetch: it refreshes the candidate and
// the state-dependent facts, then either mirrors the running round or hands a
// closed one to catchUp. deliver reports whether a payload waits for
// deliverAndSettle.
func applyRemoteView(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, f *remoteFetch, now time.Time) (store.Binding, bool, error) {
	server := b.Builder.Server
	name := b.Name
	view := f.View

	// A poll that got through means any transient auth error is over, so the
	// next one starts its own grace.
	rt.AuthGrace.Clear(name)

	b.RemoteUnreachableSince = time.Time{}
	b.Builder.RemoteStatus = string(view.RoundState)
	// RemoteQueue and RemoteLive are set only in their respective cases below;
	// every other state clears them.
	b.Builder.RemoteQueue = nil
	b.Builder.RemoteLive = nil

	// A server-side switch: the candidate that actually ran differs from what
	// this binding last recorded. Refresh the token and the harness kind, and
	// log it the same way a local mid-round switch does, so usage and status
	// name the builder that actually ran.
	if view.Candidate != "" && view.Candidate != b.BuilderCandidate {
		prev := b.BuilderCandidate
		b.BuilderCandidate = view.Candidate
		kind := ""
		if ref, err := candidate.ParseRef(view.Candidate); err == nil {
			kind = ref.Harness
		}
		b.Builder.Kind = kind
		if err := tx.AppendLog(name, store.LogEntry{
			TS: now, Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindSwitch,
			Note:      fmt.Sprintf("switched on %s: %s -> %s", server, prev, view.Candidate),
			Confirmed: true,
		}); err != nil {
			return b, false, err
		}
	}

	switch view.RoundState {
	case remote.RoundQueued:
		// A queued round has no process and no clocks: it behaves like
		// RoundRunning minus the stall copy -- no halt, no catch-up, and any
		// stall stamp from an earlier running round no longer applies.
		b.StalledSince = time.Time{}
		if view.Queue != nil {
			b.Builder.RemoteQueue = &store.QueueFacts{
				Position: view.Queue.Position,
				Ahead:    view.Queue.Ahead,
				Running:  view.Queue.Running,
				Cap:      view.Queue.Cap,
				Since:    view.Queue.Since,
			}
		}
		return b, false, nil

	case remote.RoundRunning:
		// The server is the only place that can see the builder's stream; a
		// running round carries its stall stamp across so the client shows
		// the same "stalled <age>".
		b.StalledSince = view.StalledSince
		b.Builder.RemoteLive = liveFactsOf(view.Live)
		if f.Legacy {
			mirrorLog(ctx, rt, tx, server, name, b.Round)
		} else {
			applyLogMirror(rt, tx, name, b.Round, f.Log)
		}
		applyDrift(rt, tx, name, b.Round, f.Drift)
		return b, false, nil

	case remote.RoundNeedsYou:
		b.StalledSince = view.StalledSince
		b, err := haltBinding(ctx, rt, b, name+": "+view.Halt)
		return b, false, err

	case remote.RoundClosed:
		if view.ClosedRound >= b.Round {
			if f.CatchUp != nil && f.CatchUp.Round == view.ClosedRound {
				next, a, err := applyCatchUp(ctx, rt, tx, b, view, f.CatchUp)
				f.Settle = a
				return next, true, err
			}
			next, err := catchUp(ctx, rt, tx, b, view)
			return next, true, err
		}
		return b, true, nil

	case remote.RoundIdle:
		if b.State == store.StateBroken {
			b.State = store.StateActive
		}
		// A lost reply to /ack leaves the server Idle with this round closed
		// while the client never recorded the report. A repeat ack is
		// idempotent on the server, so catching up again is safe.
		entries, rerr := tx.ReadLog(name)
		if rerr == nil && view.ClosedRound >= b.Round &&
			!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport) {
			if f.CatchUp != nil && f.CatchUp.Round == view.ClosedRound {
				next, a, err := applyCatchUp(ctx, rt, tx, b, view, f.CatchUp)
				f.Settle = a
				return next, true, err
			}
			next, err := catchUp(ctx, rt, tx, b, view)
			return next, true, err
		}
		return b, true, nil

	default:
		return b, true, nil
	}
}

// observeRemote fetches for b and applies the result inline, under whatever
// lock the caller already holds. The daemon and SyncRemote fetch before their
// lock and apply through applyRemote; this stays for the one-off stop path.
func observeRemote(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, error) {
	f := fetchRemote(ctx, rt, b)
	next, deliver, err := applyRemote(ctx, rt, tx, b, &f)
	if f.Settle != nil {
		next, err = settleCatchUpInline(ctx, rt, tx, next, f.Settle)
	}
	return next, deliver, err
}

// reconcileRemote is the daemon tick's entry point for a remote binding: it
// applies the prefetched server view and, unless the apply already returned
// (a halt, a running mirror, an error), delivers any pending payload. A nil
// pre is the inline path: fetch and apply under the caller's lock.
func reconcileRemote(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, pre *remoteFetch) (store.Binding, error) {
	var (
		next    store.Binding
		deliver bool
		err     error
	)
	switch {
	case pre == nil:
		next, deliver, err = observeRemote(ctx, rt, tx, b)
	case !pre.matches(b):
		// The binding changed while the fetch ran unlocked, so the snapshot
		// is stale: change nothing and let the next tick fetch afresh.
		slog.Debug("remote fetch discarded: binding changed", "binding", b.Name)
		return b, nil
	default:
		next, deliver, err = applyRemote(ctx, rt, tx, b, pre)
	}
	if err != nil || !deliver {
		return next, err
	}
	if pre != nil && pre.Settle != nil {
		return next, nil
	}
	return deliverAndSettle(ctx, rt, tx, next)
}

// settleCatchUp finishes a catch-up after the lock was released: the ack, then
// the report under a fresh lock, guarded against a binding that moved on.
// reconcile is true for the daemon's tick, which also delivers the queued
// payload and emits the tick's mutation events; the read verbs collect
// without either.
func settleCatchUp(ctx context.Context, rt Runtime, a *catchUpAck, reconcile bool) error {
	if err := ackCatchUp(ctx, rt, a); err != nil {
		return nil
	}
	return rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(a.Name)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !a.matches(cur) {
			return nil
		}
		next, err := applyCatchUpReport(ctx, rt, tx, cur, a)
		if err != nil {
			return err
		}
		if reconcile {
			next, err = deliverAndSettle(ctx, rt, tx, next)
			if err != nil {
				return err
			}
			emitMutations(ctx, rt, cur, next)
		}
		if store.SameBinding(next, cur) {
			return nil
		}
		return tx.Save(next)
	})
}

// SyncRemote runs one read-only observe pass over every remote binding that
// is still relaying (not store.StateDone), so `relevo status`, `relevo wait`
// and each `relevo wait` iteration collect a closed round without the daemon
// running (spec §2.2). It never delivers: it calls observeRemote directly,
// not reconcileRemote, so a payload stays pending for the daemon, the channel
// or `relevo wait` to take.
//
// synced counts bindings whose stored state actually changed under the
// pass; per-binding errors are joined into one returned error rather than
// aborting, so one unreachable server does not stop another binding's sync.
// A halt observeRemote decides on (a 401, a 404, an unreachable round past
// its budget) still fires: only delivery is skipped here, not the same
// observations the daemon would make.
func SyncRemote(ctx context.Context, rt Runtime) (int, error) {
	if rt.Remote == nil {
		return 0, nil
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return 0, err
	}

	synced := 0
	var errs []error
	for _, b := range bindings {
		// A paused binding is not being relayed, and the daemon's Reconcile
		// skips it too, so this pass has nothing to collect for it.
		if !b.Builder.Remote() || b.State == store.StateDone || b.State == store.StatePaused {
			continue
		}
		name := b.Name
		f := fetchRemote(ctx, rt, b)
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			fresh, err := tx.Load(name)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if !f.matches(fresh) {
				return nil
			}
			next, _, err := applyRemote(ctx, rt, tx, fresh, &f)
			if err != nil {
				return err
			}
			if store.SameBinding(next, fresh) {
				return nil
			}
			synced++
			return tx.Save(next)
		})
		f.release()
		if err == nil && f.Settle != nil {
			err = settleCatchUp(ctx, rt, f.Settle, false)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	return synced, errors.Join(errs...)
}

// SyncRemoteUnlessDaemon is SyncRemote for the read verbs (`relevo status`,
// `relevo wait`), which must not duplicate the daemon's own sync: when a
// daemon holds the lock, that daemon is already syncing every binding this
// pass would, so nothing is run -- no network call, no state lock taken.
//
// A probe that fails is not proof of a daemon, so that case syncs: a missed
// skip only costs contention, while a missed sync could leave wait blind with
// no daemon running. skipped reports whether SyncRemote was left unrun.
func SyncRemoteUnlessDaemon(ctx context.Context, rt Runtime) (synced int, skipped bool, err error) {
	if rt.Remote == nil {
		return 0, false, nil
	}
	running, perr := rt.Store.DaemonRunning()
	if perr == nil && running {
		return 0, true, nil
	}
	synced, err = SyncRemote(ctx, rt)
	return synced, false, err
}
