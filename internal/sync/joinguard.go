package sync

// The join guard: a join reads every other origin's whole log through Turso, so it
// refuses what the month's left Turso traffic would not fit. The segment half is
// estimated from the bucket before any worker exists; the Turso half is read from
// the open worker once the join has one.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrJoinOverQuota reports a join estimated past the month's left Turso traffic.
var ErrJoinOverQuota = errors.New("sync: the join would use more Turso traffic than this month has left")

// JoinEstimate is what a join is expected to cost: the segments the bucket holds,
// and what the worker pulled through Turso to learn them.
type JoinEstimate struct {
	// Segments is how many log segments the bucket holds.
	Segments int
	// SegmentBytes is the listed size of those segments.
	SegmentBytes int64
	// Latest is the newest segment's listing time, zero when the bucket holds
	// none: the time hint a joiner weighs against its own freshness.
	Latest time.Time
	// TursoBytes is what the worker pulled through Turso, the figure the guard
	// judges against the quota left.
	TursoBytes int64
	// Known is whether TursoBytes was read; unknown proves nothing, so the
	// guard lets the join proceed.
	Known bool
}

// segmentTotals lists the bucket once, counting segment keys; bodies, old size
// objects and every other key are ignored by key shape alone.
func segmentTotals(ctx context.Context, store blobstore.BlobStore) (segments int, total int64, latest time.Time, err error) {
	objects, err := store.List(ctx, "")
	if err != nil {
		return 0, 0, time.Time{}, fmt.Errorf("sync: list the bucket: %w", err)
	}
	for _, o := range objects {
		if !blobstore.IsSegmentKey(o.Key) {
			continue
		}
		segments++
		total += o.Size
		if o.Modified.After(latest) {
			latest = o.Modified
		}
	}
	return segments, total, latest, nil
}

// EstimateJoin counts the bucket's segments, fetching nothing: the listing
// carries every figure the R2 half needs. The Turso half stays unset, which the
// guard reads as unknown.
func EstimateJoin(ctx context.Context, store blobstore.BlobStore) (JoinEstimate, error) {
	segments, total, latest, err := segmentTotals(ctx, store)
	if err != nil {
		return JoinEstimate{}, err
	}
	return JoinEstimate{Segments: segments, SegmentBytes: total, Latest: latest}, nil
}

// GuardJoin refuses a join whose known estimate tops this machine's quota left;
// exactly equal fits. The remainder is this machine's alone: the account total
// is not visible on this path, and the refusal says whose figures they are.
func GuardJoin(est JoinEstimate, quota, usedThisMonth int64) error {
	left := quota - usedThisMonth
	if !est.Known || est.TursoBytes <= left {
		return nil
	}
	return fmt.Errorf("%w: it would read about %s from Turso, %s is left for this machine of this month's %s quota; pass --yes to join anyway",
		ErrJoinOverQuota, humanBytes(est.TursoBytes), humanBytes(max(left, 0)), humanBytes(quota))
}

// GuardJoinAccount refuses a join whose known estimate tops the account's quota
// left: quota minus the AccountTraffic fold over the transport's rows, with this
// machine's live counters standing in for its own unpublished row. Exactly equal
// fits, and an unknown estimate proves nothing, the rule JoinEstimate states.
func GuardJoinAccount(est JoinEstimate, quota int64, rows map[string]synclog.TrafficRow, own ByteCounters, origin string, now time.Time) error {
	if !est.Known {
		return nil
	}
	account := AccountTraffic(AccountSnapshot{Rows: rows}, own, origin, now)
	left := quota - account.Total
	if est.TursoBytes <= left {
		return nil
	}
	return fmt.Errorf("%w: it would read about %s from Turso, %s is left of this month's %s account quota across %s; pass --yes to join anyway",
		ErrJoinOverQuota, humanBytes(est.TursoBytes), humanBytes(max(left, 0)), humanBytes(quota), Machines(account.Machines))
}

// noAccountNote states plainly that the account's traffic cannot be seen, naming
// the newest local snapshot's month and age when one exists. The newest is the
// maximum Month over the snapshot's rows, which is chronological for the
// "2006-01" spelling; the age is now minus the snapshot's reading. A local
// remainder is never quoted as an account figure here: without the transport's
// rows there is no account to quote.
func noAccountNote(snap AccountSnapshot, now time.Time) string {
	newest := ""
	for _, row := range snap.Rows {
		if row.Month > newest {
			newest = row.Month
		}
	}
	if newest == "" {
		return "the account's traffic cannot be seen from this machine"
	}
	age := now.Sub(snap.ReadAt)
	if age < 0 {
		age = 0
	}
	return fmt.Sprintf("the account's traffic cannot be seen from this machine; newest local snapshot is %s, read %s ago", newest, age)
}

// withNoAccount carries a local refusal with the note that the account could not
// be seen. A snapshot that will not parse reads as none: it names no month
// either way, and the refusal already says the account is not visible.
func withNoAccount(local Local, refuse error, now time.Time) error {
	snap, serr := ReadAccount(local)
	if serr != nil {
		snap = AccountSnapshot{}
	}
	return fmt.Errorf("%w; %s", refuse, noAccountNote(snap, now))
}

// guard estimates the join from the bucket's segments, reporting whether a
// bucket was consulted at all. The Turso half stays unset: nothing Turso-side
// has been read on this path, and the guard must not open a worker or add Turso
// traffic to learn it. A plain enable therefore never refuses here; the join's
// guard refuses once its worker exists.
func (e *Enabler) guard(plan EnablePlan, now time.Time) (JoinEstimate, bool, error) {
	if e.Blobs == nil {
		return JoinEstimate{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), guardTimeout)
	defer cancel()
	est, err := EstimateJoin(ctx, e.Blobs(plan.R2))
	if err != nil {
		return JoinEstimate{}, true, fmt.Errorf("sync: enable: estimate the join: %w", err)
	}
	if e.Request.Yes {
		return est, true, nil
	}
	settings, err := ReadSettings(e.Request.Local)
	if err != nil {
		return est, true, err
	}
	used, err := ReadBytes(e.Request.Local, now)
	if err != nil {
		return est, true, err
	}
	if err := GuardJoin(est, settings.TursoSyncQuota(), used.TursoPush+used.TursoPull); err != nil {
		return est, true, withNoAccount(e.Request.Local, err, now)
	}
	return est, true, nil
}

// guardJoined is the guard a join runs after its document import, against the
// imported credentials rather than the command's: same rule and refusal on a later
// reading, since no bucket estimates before the secrets document applies. A machine
// with no bucket store consults nothing and is not guarded: no installed store means
// no bucket to overrun. The Turso half is read from the worker's Stats, what this
// worker pulled through Turso; the transport's rows then judge the same estimate
// against the account total, and when they name no account the local figures stand,
// with the note that the account could not be seen. The read publishes nothing:
// the account markers move only on a successful publish.
func (e *Enabler) guardJoined(transport synclog.LogTransport) (JoinEstimate, bool, error) {
	stored, err := ReadR2(e.Request.Local)
	if err != nil {
		return JoinEstimate{}, false, err
	}
	now := e.now()
	est, guarded, err := e.guard(EnablePlan{Remote: "", R2: stored}, now)
	if err != nil || !guarded {
		return est, guarded, err
	}
	// The Turso half arrives only once a worker exists: a readable zero is
	// known, while an unreadable one leaves the estimate unknown.
	if transport != nil {
		if stats, serr := transport.Stats(); serr == nil {
			est.TursoBytes = stats.TursoReceived
			est.Known = true
		}
	}
	// Yes joins past the guard's refusal, whichever figures it was read against.
	if e.Request.Yes {
		return est, guarded, nil
	}
	settings, err := ReadSettings(e.Request.Local)
	if err != nil {
		return est, guarded, err
	}
	used, err := ReadBytes(e.Request.Local, now)
	if err != nil {
		return est, guarded, err
	}
	rows, ok := readAccountRows(transport, now)
	if !ok {
		if err := GuardJoin(est, settings.TursoSyncQuota(), used.TursoPush+used.TursoPull); err != nil {
			return est, guarded, withNoAccount(e.Request.Local, err, now)
		}
		return est, guarded, nil
	}
	return est, guarded, GuardJoinAccount(est, settings.TursoSyncQuota(), rows, used, e.Request.Shared.Origin(), now)
}

// readAccountRows answers with the transport's traffic rows when they name the
// account: the month holding now must appear, because a joiner's own row is
// unpublished and an answer of one is no account at all. Anything less -- no
// transport, no reader, a read error -- is no account figure rather than a
// failure, the rule TrafficReader states.
func readAccountRows(transport synclog.LogTransport, now time.Time) (map[string]synclog.TrafficRow, bool) {
	if transport == nil {
		return nil, false
	}
	reader, ok := transport.(synclog.TrafficReader)
	if !ok {
		return nil, false
	}
	rows, err := readTrafficBounded(reader, guardTimeout)
	if err != nil {
		return nil, false
	}
	month := now.UTC().Format("2006-01")
	for _, row := range rows {
		if row.Month == month {
			return rows, true
		}
	}
	return nil, false
}

// readTrafficBounded runs one traffic read under a bound. The reader takes no
// context, so the wait is what is bounded; the channel is buffered so a read
// that outlives the bound still lands instead of stranding its sender.
func readTrafficBounded(reader synclog.TrafficReader, bound time.Duration) (map[string]synclog.TrafficRow, error) {
	type result struct {
		rows map[string]synclog.TrafficRow
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := reader.ReadTraffic()
		done <- result{rows: rows, err: err}
	}()
	select {
	case r := <-done:
		return r.rows, r.err
	case <-time.After(bound):
		return nil, errors.New("sync: enable: read the account traffic: timed out")
	}
}

// ownBodiesJoined is ownBodies for a join, against the document-brought credentials,
// after the import like the guard: before it, no bucket lists.
func (e *Enabler) ownBodiesJoined() (map[string]bool, error) {
	stored, err := ReadR2(e.Request.Local)
	if err != nil {
		return nil, err
	}
	return e.ownBodies(EnablePlan{Remote: "", R2: stored})
}

// guardTimeout bounds the guard's bucket listing.
const guardTimeout = 2 * time.Minute

// ownBodies lists the bodies the bucket holds under this machine's origin, so the
// reconcile uploads what rows name that the bucket lost. A join that cannot list
// could not upload either, so failure ends the enable here, not halfway through.
func (e *Enabler) ownBodies(plan EnablePlan) (map[string]bool, error) {
	if e.Blobs == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), guardTimeout)
	defer cancel()
	objects, err := e.Blobs(plan.R2).List(ctx, e.Request.Shared.Origin()+"/")
	if err != nil {
		return nil, fmt.Errorf("sync: enable: list this machine's bodies: %w", err)
	}
	present := make(map[string]bool, len(objects))
	for _, o := range objects {
		if blobstore.IsBodyKey(o.Key) {
			present[o.Key] = true
		}
	}
	return present, nil
}
