package sync

// The join guard: a join reads every other origin's whole log through Turso, so
// it is estimated from the bucket before any worker exists, and refused when it
// would not fit in what is left of the month's Turso traffic.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// ErrJoinOverQuota reports a join estimated to cost more Turso traffic than the
// month has left.
var ErrJoinOverQuota = errors.New("sync: the join would use more Turso traffic than this month has left")

// maxSizeObject bounds a published log size. The object is a few dozen bytes; a
// larger one is not ours and is ignored rather than read.
const maxSizeObject = 4096

// LogSize is what one origin publishes about its log under blobstore.SizeKey.
type LogSize struct {
	Entries int       `json:"entries"`
	Bytes   int64     `json:"bytes"`
	At      time.Time `json:"at"`
}

// JoinEstimate is what a join is expected to cost, read from the bucket alone.
type JoinEstimate struct {
	// R2Bytes is every body in the bucket. R2 has no egress charge and a join
	// adds no storage, so it is reported and never refused on.
	R2Bytes int64
	// TursoBytes is the sum of every origin's published log size.
	TursoBytes int64
	// Known is whether any origin published a size. An estimate that is not
	// known proves nothing, so the guard lets the join proceed.
	Known bool
}

// bucketTotals lists the bucket once: the bytes held by bodies, and the keys of
// the published log sizes. Any other key is neither and is ignored.
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

// EstimateJoin totals the bucket's bodies and every origin's published log
// size.
func EstimateJoin(ctx context.Context, store blobstore.BlobStore) (JoinEstimate, error) {
	bodies, keys, err := bucketTotals(ctx, store)
	if err != nil {
		return JoinEstimate{}, err
	}
	est := JoinEstimate{R2Bytes: bodies}
	for _, key := range keys {
		var buf bytes.Buffer
		if _, err := store.Get(ctx, key, &buf); err != nil {
			return JoinEstimate{}, fmt.Errorf("sync: read %s: %w", key, err)
		}
		var size LogSize
		if err := json.Unmarshal(buf.Bytes(), &size); err != nil || size.Bytes < 0 {
			continue
		}
		est.TursoBytes += size.Bytes
		est.Known = true
	}
	return est, nil
}

// GuardJoin refuses a join whose known estimate is more than what is left of
// the quota. An estimate exactly equal to what is left fits.
func GuardJoin(est JoinEstimate, quota, usedThisMonth int64) error {
	left := quota - usedThisMonth
	if !est.Known || est.TursoBytes <= left {
		return nil
	}
	return fmt.Errorf("%w: it would read about %s from Turso, %s is left of this month's %s quota; pass --yes to join anyway",
		ErrJoinOverQuota, humanBytes(est.TursoBytes), humanBytes(max(left, 0)), humanBytes(quota))
}

// guard estimates the join and refuses it when it does not fit. It reports
// whether a bucket was consulted at all.
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

// guardTimeout bounds the bucket listing the guard makes.
const guardTimeout = 2 * time.Minute

// ownBodies lists the bodies the bucket holds under this machine's origin, so the
// join's reconcile can upload any its rows name that the bucket lost. A join
// that cannot list the bucket could not upload to it either, so a failure ends
// the enable here rather than halfway through.
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
