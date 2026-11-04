package relevo

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
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
// The common path is one small file read, so a 2s tick stays cheap. Every
// failure is swallowed and logged at debug: an offline machine saves nothing,
// writes no wrong answer, and waits out a one-hour backoff before trying again.
func (d *Daemon) refreshRelease(ctx context.Context) {
	if d.rt.Fetcher == nil {
		return
	}

	now := time.Now
	if d.rt.Now != nil {
		now = d.rt.Now
	}

	if now().Before(d.releaseRetryAt) {
		return
	}

	// The cache lives in the machine database's kv row "release-check",
	// reached through the runtime's own store, so the release check adds no
	// connection of its own.
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
// the apply half discards against the binding it reads under the lock, and
// the saved load was most of the tick's per-binding cost.
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

// ArchiveCursor is the archived mirror's progress within a process. The first
// pass lists every archived record to heal what no daemon saw, and every
// later pass lists only records archived since highWater. seen holds the ids
// this process confirmed, so rows sharing highWater's stamp cost no second
// read; retry holds the records a read, ingest or mark write failed, so a
// later tick re-attempts them by id instead of holding the cursor back.
type ArchiveCursor struct {
	primed    bool
	highWater time.Time
	seen      map[string]struct{}
	retry     map[string]string
}

// archiveBackend is the archived mirror's collaborator: listing archived
// records, reading and writing ingested.archive.* marks, and ingesting one
// record. Tick wires the live store and database; tests wire a counting or
// failing fake around the same cursor.
type archiveBackend interface {
	listAll() ([]store.ArchivedBinding, error)
	listSince(since time.Time) ([]store.ArchivedRef, error)
	marked(recordID string) (bool, error)
	ingest(ctx context.Context, recordID, name string) (ingest.Stats, error)
	confirm(recordID string) error
}

// liveArchiveBackend is the production archiveBackend: the runtime's own
// store and database, with marks in the machine-local file, where the split
// pass would otherwise move them on every start.
type liveArchiveBackend struct {
	st     *store.Store
	marks  db.KV
	mirror *db.DB
	deps   ingest.Deps
}

func liveBackend(rt Runtime) *liveArchiveBackend {
	return &liveArchiveBackend{
		st:     rt.Store,
		marks:  rt.DB.LocalOrSelf(),
		mirror: rt.DB,
		deps:   IngestDeps(rt),
	}
}

func (b *liveArchiveBackend) listAll() ([]store.ArchivedBinding, error) {
	return b.st.ListArchived()
}

func (b *liveArchiveBackend) listSince(since time.Time) ([]store.ArchivedRef, error) {
	return b.st.ListArchivedSince(since)
}

func (b *liveArchiveBackend) marked(recordID string) (bool, error) {
	_, ok, err := b.marks.KVGet("ingested.archive." + recordID)
	return ok, err
}

func (b *liveArchiveBackend) ingest(ctx context.Context, recordID, _ string) (ingest.Stats, error) {
	return ingest.Ingest(ctx, ingest.ArchivedSource(b.st, recordID), b.mirror, b.deps)
}

func (b *liveArchiveBackend) confirm(recordID string) error {
	return b.marks.KVPut("ingested.archive."+recordID, []byte("true"))
}

// mirror runs one pass: a full pass the first time, then only the records
// archived since the last pass.
func (c *ArchiveCursor) mirror(ctx context.Context, b archiveBackend) {
	if c.seen == nil {
		c.seen = map[string]struct{}{}
	}
	if c.retry == nil {
		c.retry = map[string]string{}
	}
	if !c.primed {
		c.fullPass(ctx, b)
		return
	}
	c.incPass(ctx, b)
}

// fullPass mirrors every archived record, and only then primes the cursor: a
// listing failure retries the whole pass next tick, while a record failure
// queues just that record. The high water advances over failures too; the
// retry set, not the cursor, carries them.
func (c *ArchiveCursor) fullPass(ctx context.Context, b archiveBackend) {
	all, err := b.listAll()
	if err != nil {
		slog.Warn("mirror: list archived", "err", err)
		return
	}
	for _, a := range all {
		c.advance(a.ArchivedAt)
		if c.isSeen(a.RecordID) {
			continue
		}
		if c.mirrorOne(ctx, b, a.RecordID, a.Binding.Name) {
			c.noteSeen(a.RecordID)
		}
	}
	c.primed = true
}

// incPass re-attempts the queued records by id, then mirrors the records
// archived since the last pass. Seen and queued ids cost no mark read; the
// marks answer only for rows this process never confirmed.
func (c *ArchiveCursor) incPass(ctx context.Context, b archiveBackend) {
	for _, id := range c.retryIDs() {
		if c.mirrorOne(ctx, b, id, c.retry[id]) {
			c.noteSeen(id)
			delete(c.retry, id)
		}
	}
	refs, err := b.listSince(c.highWater)
	if err != nil {
		slog.Warn("mirror: list archived", "err", err)
		return
	}
	for _, r := range refs {
		c.advance(r.ArchivedAt)
		if c.isSeen(r.RecordID) {
			continue
		}
		if _, pending := c.retry[r.RecordID]; pending {
			continue
		}
		if c.mirrorOne(ctx, b, r.RecordID, r.Name) {
			c.noteSeen(r.RecordID)
		}
	}
}

// mirrorOne confirms one archived record: already marked, or ingested and
// marked now. A read, ingest or mark failure queues the record for a later
// tick rather than dropping it.
func (c *ArchiveCursor) mirrorOne(ctx context.Context, b archiveBackend, recordID, name string) bool {
	ok, err := b.marked(recordID)
	if err != nil {
		slog.Warn("mirror: read archived cursor", "record", recordID, "err", err)
		c.queueRetry(recordID, name)
		return false
	}
	if ok {
		return true
	}
	stats, err := b.ingest(ctx, recordID, name)
	if err != nil {
		slog.Warn("mirror: ingest archived", "binding", name, "err", err)
		c.queueRetry(recordID, name)
		return false
	}
	if err := b.confirm(recordID); err != nil {
		slog.Warn("mirror: record archived cursor", "record", recordID, "err", err)
		c.queueRetry(recordID, name)
		return false
	}
	if stats != (ingest.Stats{}) {
		slog.Info("ingest", "binding", name, "archived", true,
			"rounds", stats.Rounds, "events", stats.Events,
			"artifacts", stats.Artifacts)
	}
	return true
}

func (c *ArchiveCursor) advance(at time.Time) {
	if at.After(c.highWater) {
		c.highWater = at
	}
}

func (c *ArchiveCursor) isSeen(recordID string) bool {
	_, ok := c.seen[recordID]
	return ok
}

func (c *ArchiveCursor) noteSeen(recordID string) {
	c.seen[recordID] = struct{}{}
	delete(c.retry, recordID)
}

func (c *ArchiveCursor) queueRetry(recordID, name string) {
	c.retry[recordID] = name
}

// retryIDs orders the queued records by id, so a tick's retry order is stable
// instead of map-random.
func (c *ArchiveCursor) retryIDs() []string {
	ids := make([]string, 0, len(c.retry))
	for id := range c.retry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// mirrorArchived feeds every archived record the mirror has not seen into the
// database. It runs on every tick; the cursor keeps later passes to the
// records archived since the last one, and each record is keyed by the kv row
// "ingested.archive.<recordID>", put only after a successful ingest, so a
// failure is logged and retried on the next tick rather than lost.
func mirrorArchived(ctx context.Context, rt Runtime) {
	if rt.DB == nil {
		return
	}
	var c ArchiveCursor
	c.mirror(ctx, liveBackend(rt))
}

// mirrorArchived feeds every archived record the mirror has not seen into the
// database, through this daemon's cursor. The first tick's pass is full; a
// later tick costs only the records archived since the pass before.
func (d *Daemon) mirrorArchived(ctx context.Context) {
	if d.rt.DB == nil {
		return
	}
	b := d.archiveBackend
	if b == nil {
		b = liveBackend(d.rt)
	}
	d.archived.mirror(ctx, b)
}
