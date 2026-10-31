package relevo

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The daemon's release and remote legs: the release check Tick runs last and
// the prefetch the per-binding pass reads before it locks.

// releaseRetryAfter is how long a failed release fetch is left alone before the
// daemon tries again: an endpoint that is down is asked once an hour, not once
// a tick.
const releaseRetryAfter = time.Hour

// refreshRelease refreshes the day-cached answer to "is a newer relevo out?".
// It runs after everything else Tick does and never before it: no reconcile
// decision may wait on a release check.
//
// The common path is one small file read -- a fresh cache ends it there, so a
// 2s tick stays cheap. A stale cache costs one bracketed HTTP GET, and every
// failure of that GET is swallowed and logged at debug: an offline machine
// saves nothing, writes no wrong answer, and waits out a one-hour backoff
// before trying again.
func (d *Daemon) refreshRelease(ctx context.Context) {
	if d.rt.Fetcher == nil {
		return
	}

	now := time.Now
	if d.rt.Now != nil {
		now = d.rt.Now
	}

	// A failed fetch backs off for an hour, so a down endpoint is asked once
	// an hour instead of on every tick.
	if now().Before(d.releaseRetryAt) {
		return
	}

	// The cache lives in the machine database's kv row "release-check",
	// reached through the runtime's own store: for the daemon that is the one
	// shared handle it serves, so the release check adds no connection of its
	// own and no store.New per tick (D1).
	mdb, err := d.rt.Store.DB()
	if err != nil {
		slog.Debug("release check: no database", "err", err)
		return
	}

	// The release cache is this machine's last answer, so it lives in the
	// local file.
	mdb = mdb.LocalOrSelf()

	cached, ok, err := release.Load(mdb)
	if err != nil {
		slog.Debug("release check: read cache", "err", err)
		return
	}
	if !release.Stale(cached, ok, now(), release.TTL) {
		return
	}

	tag, err := d.rt.Fetcher.Latest(ctx)
	if err != nil {
		// Save nothing: a wrong or empty answer in the cache would read as
		// truth for a whole day. The next attempt waits out the backoff.
		d.releaseRetryAt = now().Add(releaseRetryAfter)
		slog.Debug("release check: fetch", "err", err)
		return
	}

	if err := release.Save(mdb, release.Cache{
		Latest:    tag,
		CheckedAt: now(),
		Source:    release.Source(),
	}); err != nil {
		slog.Debug("release check: save cache", "err", err)
		return
	}
	d.releaseRetryAt = time.Time{}
}

// prefetchRemote reads what a remote binding's next reconcile needs from the
// server, before tickOne takes the state lock. It returns nil when the binding
// cannot be loaded or is not a live remote binding; a failed server read
// travels in the fetch's Err and is classified by the apply half. It runs
// inside tickOne's deferred recover, so a panic in the fetch is contained
// there.
//
// The binding is the caller's tick snapshot rather than a fresh load: a
// concurrent pause or unbind of a remote binding costs one wasted fetch, which
// the apply half discards against the binding it reads under the lock, and the
// saved load was most of the tick's per-binding cost.
func (d *Daemon) prefetchRemote(ctx context.Context, b store.Binding) *remoteFetch {
	if d.rt.Remote == nil {
		return nil
	}
	if !b.Builder.Remote() || b.State == store.StateDone || b.State == store.StatePaused {
		return nil
	}
	if !store.KnownState(b.State) || b.Format > store.BindingFormat {
		return nil
	}
	if d.backoff.skip(b.Builder.Server) {
		return skippedFetch(b)
	}
	f := fetchRemoteWithin(ctx, d.rt, b, prefetchDeadline)
	d.backoff.record(b.Builder.Server, f.Err != nil && serverUnreachable(f.Err))
	return &f
}

// archivedMirrorOnce runs the archived-record mirror feed once per process,
// before the daemon's first tick.
var archivedMirrorOnce sync.Once

// mirrorArchived feeds every archived record the mirror has not seen into the
// database.
//
// Each record is keyed by the kv row "ingested.archive.<recordID>": the key is
// put only after a successful ingest, so a failure is logged and retried by
// the next process rather than lost.
func mirrorArchived(ctx context.Context, rt Runtime) {
	if rt.DB == nil {
		return
	}

	archived, err := rt.Store.ListArchived()
	if err != nil {
		slog.Warn("mirror: list archived", "err", err)
		return
	}

	// The "ingested.archive" namespace is machine-local: it lives in the local
	// file, where the split pass would otherwise move it on every start.
	marks := rt.DB.LocalOrSelf()
	deps := IngestDeps(rt)
	for _, a := range archived {
		key := "ingested.archive." + a.RecordID
		if _, ok, kerr := marks.KVGet(key); kerr != nil {
			slog.Warn("mirror: read archived cursor", "record", a.RecordID, "err", kerr)
			continue
		} else if ok {
			continue
		}

		stats, ierr := ingest.Ingest(ctx, ingest.ArchivedSource(rt.Store, a.RecordID), rt.DB, deps)
		if ierr != nil {
			slog.Warn("mirror: ingest archived", "binding", a.Binding.Name, "err", ierr)
			continue
		}
		if perr := marks.KVPut(key, []byte("true")); perr != nil {
			slog.Warn("mirror: record archived cursor", "record", a.RecordID, "err", perr)
			continue
		}
		if stats != (ingest.Stats{}) {
			slog.Info("ingest", "binding", a.Binding.Name, "archived", true,
				"rounds", stats.Rounds, "events", stats.Events,
				"artifacts", stats.Artifacts)
		}
	}
}
