package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/remote/client"
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
func (d *Daemon) prefetchRemote(ctx context.Context, b store.Binding, gen uint64) *remoteFetch {
	if d.rt.Remote == nil {
		return nil
	}
	if !b.Builder.Remote() || b.State == store.StateDone || b.State == store.StatePaused {
		return nil
	}
	if !store.KnownState(b.State) || b.Format > store.BindingFormat {
		return nil
	}
	server := b.Builder.Server
	if d.backoff.skip(server) {
		f := skippedFetch(b)
		// A skip means an armed episode, so the backoff holds the start the
		// apply half stamps.
		f.EpisodeStart = d.backoff.episodeStart(server)
		return f
	}
	if d.backoff.countedIn(server, gen) {
		// This generation already counted this server's failure: later
		// bindings reuse it without dialling, so one slow tick counts once.
		f := skippedFetch(b)
		f.Unconfirmed = true
		return f
	}
	started := time.Now()
	f := fetchRemoteWithin(ctx, d.rt, b, prefetchDeadline)
	elapsed := time.Since(started)
	if f.Err == nil || !serverUnreachable(f.Err) {
		// A live answer -- a view or a per-binding refusal -- proves the
		// server is up, so it clears the consecutive-failure memory.
		if rep := d.backoff.recordSuccess(server); rep.ended {
			slog.Info(fmt.Sprintf("%s reachable", server), "server", server,
				"duration", rep.end.Sub(rep.start).Truncate(time.Second), "err", rep.opening)
		}
		return &f
	}
	rep := d.backoff.recordFailure(server, f.Err, gen)
	switch {
	case rep.confirmed:
		// Count and span confirmed the episode: one WARN per server, while
		// every binding's own line drops to the apply half's DEBUG.
		f.EpisodeStart = rep.start
		slog.Warn(fmt.Sprintf("%s unreachable", server), "server", server, "err", rep.opening)
	case rep.inEpisode:
		// A probe that finds the episode still armed rejoins it: the binding
		// keeps the episode's start, so the halt still fires for the prober.
		f.EpisodeStart = rep.start
		slog.Debug(fmt.Sprintf("%s unreachable", server),
			"server", server, "binding", b.Name, "duration", elapsed, "err", f.Err)
	default:
		f.Unconfirmed = true
		slog.Info("remote prefetch failed", "server", server, "binding", b.Name,
			"duration", elapsed, "http_response", client.GotResponse(f.Err), "err", f.Err)
	}
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

// archiveLookback is how far below the high water each later pass reads. An
// archive stamps its time before it commits, so a record stamped just before
// another can land after the pass that saw the other, and a stepped clock can
// stamp below the high water; the seen set absorbs the overlap.
const archiveLookback = time.Minute

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

// archiveMarkVersion is the value an ingested archived record's mark holds,
// stored as a JSON string. Only the current value counts as ingested: a
// record marked anything else still owes one ingest, so its rows are
// re-derived from the stored log.
const archiveMarkVersion = "v2"

func (b *liveArchiveBackend) marked(recordID string) (bool, error) {
	v, ok, err := b.marks.KVGet("ingested.archive." + recordID)
	if err != nil || !ok {
		return ok, err
	}
	return string(v) == strconv.Quote(archiveMarkVersion), nil
}

func (b *liveArchiveBackend) ingest(ctx context.Context, recordID, _ string) (ingest.Stats, error) {
	return ingest.Ingest(ctx, ingest.ArchivedSource(b.st, recordID), b.mirror, b.deps)
}

func (b *liveArchiveBackend) confirm(recordID string) error {
	return b.marks.KVPut("ingested.archive."+recordID, []byte(strconv.Quote(archiveMarkVersion)))
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
	refs, err := b.listSince(c.highWater.Add(-archiveLookback))
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
