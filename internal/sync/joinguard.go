package sync

// The join guard: a join reads every other origin's whole log through Turso, so it
// estimates from the bucket before any worker exists, refusing what the month's
// left Turso traffic would not fit.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// ErrJoinOverQuota reports a join estimated past the month's left Turso traffic.
var ErrJoinOverQuota = errors.New("sync: the join would use more Turso traffic than this month has left")

// maxSizeObject bounds a published log size object, which is really a few dozen
// bytes; a larger one is not ours and is ignored rather than read.
const maxSizeObject = 4096

// The bucket is written by untrusted other machines, so the estimate bounds both
// takes: how many size objects read, and how large a size believed. Past maxLogSize
// is no log this design produces; bounding each keeps the sum from overflowing past
// the guard.
const (
	maxSizeKeys = 1000
	maxLogSize  = int64(1) << 40
)

// LogSize is what one origin publishes about its log under blobstore.SizeKey.
type LogSize struct {
	Entries int       `json:"entries"`
	Bytes   int64     `json:"bytes"`
	At      time.Time `json:"at"`
}

// JoinEstimate is what a join is expected to cost, read from the bucket alone.
type JoinEstimate struct {
	// R2Bytes is every body in the bucket, reported never refused: no egress charge,
	// no added storage.
	R2Bytes int64
	// TursoBytes is the sum of every origin's published log size.
	TursoBytes int64
	// Known is whether any origin published a size; unknown proves nothing, so the
	// guard lets the join proceed.
	Known bool
}

// bucketTotals lists the bucket once: body bytes and published size keys; any other
// key is neither and is ignored.
func bucketTotals(ctx context.Context, store blobstore.BlobStore) (int64, []string, error) {
	objects, err := store.List(ctx, "")
	if err != nil {
		return 0, nil, fmt.Errorf("sync: list the bucket: %w", err)
	}
	var bodies int64
	var sizes []string
	for _, o := range objects {
		if blobstore.IsBodyKey(o.Key) {
			bodies += o.Size
		} else if _, ok := blobstore.SizeOrigin(o.Key); ok && o.Size <= maxSizeObject {
			sizes = append(sizes, o.Key)
		}
	}
	return bodies, sizes, nil
}

// EstimateJoin totals the bucket's bodies and every origin's published log size.
func EstimateJoin(ctx context.Context, store blobstore.BlobStore) (JoinEstimate, error) {
	bodies, keys, err := bucketTotals(ctx, store)
	if err != nil {
		return JoinEstimate{}, err
	}
	est := JoinEstimate{R2Bytes: bodies}
	if len(keys) > maxSizeKeys {
		keys = keys[:maxSizeKeys]
	}
	for _, key := range keys {
		size, ok, err := readLogSize(ctx, store, key)
		if err != nil {
			return JoinEstimate{}, err
		}
		if !ok {
			continue
		}
		est.TursoBytes += size
		est.Known = true
	}
	return est, nil
}

// readLogSize reads one published size, false for a non-size: over-long, non-JSON,
// or a size no log has. The read stops past maxSizeObject whatever the listing said.
func readLogSize(ctx context.Context, store blobstore.BlobStore, key string) (int64, bool, error) {
	var buf bytes.Buffer
	if _, err := store.Get(ctx, key, &limitedWriter{w: &buf, left: maxSizeObject + 1}); err != nil {
		if errors.Is(err, errObjectTooLarge) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("sync: read %s: %w", key, err)
	}
	var size LogSize
	if err := json.Unmarshal(buf.Bytes(), &size); err != nil || size.Bytes < 0 || size.Bytes > maxLogSize {
		return 0, false, nil
	}
	return size.Bytes, true, nil
}

// errObjectTooLarge stops a read past what the object may hold.
var errObjectTooLarge = errors.New("sync: the object is larger than a size object")

// limitedWriter takes at most left bytes, failing the write that would pass it to
// end the download rather than buffer it.
type limitedWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) >= l.left {
		return 0, errObjectTooLarge
	}
	l.left -= len(p)
	return l.w.Write(p)
}

// GuardJoin refuses a join whose known estimate tops the quota left; exactly equal
// fits.
func GuardJoin(est JoinEstimate, quota, usedThisMonth int64) error {
	left := quota - usedThisMonth
	if !est.Known || est.TursoBytes <= left {
		return nil
	}
	return fmt.Errorf("%w: it would read about %s from Turso, %s is left of this month's %s quota; pass --yes to join anyway",
		ErrJoinOverQuota, humanBytes(est.TursoBytes), humanBytes(max(left, 0)), humanBytes(quota))
}

// guard estimates the join, refusing when it does not fit, reporting whether a
// bucket was consulted at all.
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
	return est, true, GuardJoin(est, settings.TursoSyncQuota(), used.TursoPush+used.TursoPull)
}

// guardJoined is the guard a join runs after its document import, against the
// imported credentials rather than the command's: same rule and refusal on a later
// reading, since no bucket estimates before the secrets document applies. A machine
// with no bucket store consults nothing and is not guarded: no installed store means
// no bucket to overrun.
func (e *Enabler) guardJoined() (JoinEstimate, bool, error) {
	stored, err := ReadR2(e.Request.Local)
	if err != nil {
		return JoinEstimate{}, false, err
	}
	return e.guard(EnablePlan{Remote: "", R2: stored}, e.now())
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
