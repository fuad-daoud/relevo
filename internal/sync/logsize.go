package sync

// What the daemon measures for other machines and for status: this machine's
// log size, published to the bucket for the join guard, and the storage in use,
// kept for status.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

const (
	// KeySizePublished is when this machine last published its log size.
	KeySizePublished = "sync.size.published"
	// KeyStorage is the storage last measured, as a Storage.
	KeyStorage = "sync.storage"

	sizeEvery    = 24 * time.Hour
	storageEvery = 24 * time.Hour
	// measureTimeout bounds one bucket round trip of either measurement.
	measureTimeout = 2 * time.Minute
)

// NewR2Store builds the in-process bucket store from the machine's credentials.
func NewR2Store(r R2Secrets) blobstore.BlobStore {
	return blobstore.NewS3Store(blobstore.S3Config{
		Endpoint: r.Endpoint, Bucket: r.Bucket, KeyID: r.KeyID, Secret: r.Secret,
	})
}

// StoreFromLocal builds the store from the stored credentials, or returns nil
// when the machine has none.
func StoreFromLocal(l Local) blobstore.BlobStore {
	r, err := ReadR2(l)
	if err != nil || !r.Complete() {
		return nil
	}
	return NewR2Store(r)
}

func (r *Runner) bucket() blobstore.BlobStore {
	if r.Blobs == nil {
		return nil
	}
	return r.Blobs()
}

// publishSize writes this machine's log size to the bucket after a compaction,
// and otherwise at most once a day after an export that appended something.
// Another machine's join guard reads it before it opens a worker. A failure is
// logged: the size is advice and the next tick tries again.
func (r *Runner) publishSize(origin string, t *StopTransport, compacted, appended bool, now time.Time) {
	store := r.bucket()
	if store == nil || (!compacted && !appended) {
		return
	}
	var last time.Time
	if err := marker(r.Local, KeySizePublished, &last); err != nil {
		slog.Warn("sync: read the size marker", "err", err)
	}
	if !compacted && now.Sub(last) < sizeEvery {
		return
	}
	state, err := t.ReadMarks()
	if err != nil {
		slog.Warn("sync: read the log size", "err", err)
		return
	}
	body, err := json.Marshal(LogSize{Entries: state.Entries[origin], Bytes: state.Sizes[origin], At: now})
	if err != nil {
		slog.Warn("sync: encode the log size", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), measureTimeout)
	defer cancel()
	if err := store.Put(ctx, blobstore.SizeKey(origin), bytes.NewReader(body), int64(len(body))); err != nil {
		slog.Warn("sync: publish the log size", "err", err)
		return
	}
	r.put(KeySizePublished, now)
}

// Storage is what this machine last measured of the two stores. A zero At is a
// machine that has not measured yet.
type Storage struct {
	Turso int64     `json:"turso"`
	R2    int64     `json:"r2"`
	At    time.Time `json:"at"`
}

// ReadStorage returns the last measurement, zero when there is none.
func ReadStorage(kv db.KV) (Storage, error) {
	var s Storage
	if err := marker(kv, KeyStorage, &s); err != nil {
		return Storage{}, err
	}
	return s, nil
}

// measureStorage records what the remote log holds and the bucket's total, at
// most once a day.
//
// Turso is measured as the sum of every origin's log size the remote reports,
// the same figure the join guard compares against the quota. The local replica
// file is not that: it is a partial copy plus the driver's write-ahead files, so
// its size says nothing about what the cloud database bills.
func (r *Runner) measureStorage(t *StopTransport, now time.Time) {
	store := r.bucket()
	if store == nil {
		return
	}
	prior, err := ReadStorage(r.Local)
	if err == nil && now.Sub(prior.At) < storageEvery {
		return
	}
	state, err := t.ReadMarks()
	if err != nil {
		slog.Warn("sync: measure the log", "err", err)
		return
	}
	var logBytes int64
	for _, size := range state.Sizes {
		logBytes += size
	}
	ctx, cancel := context.WithTimeout(context.Background(), measureTimeout)
	defer cancel()
	bodies, _, err := bucketTotals(ctx, store)
	if err != nil {
		slog.Warn("sync: measure the bucket", "err", err)
		return
	}
	r.put(KeyStorage, Storage{Turso: logBytes, R2: bodies, At: now})
}

// FormatQuotas is the status clause for the month's Turso traffic against its
// quota, and the two stores' storage against theirs. Storage never measured
// reads "not measured yet", never zero.
func FormatQuotas(c ByteCounters, s Settings, st Storage) string {
	out := FormatBytes(c, s.TursoSyncQuota())
	if st.At.IsZero() {
		return out + "; storage: not measured yet"
	}
	return out + fmt.Sprintf("; turso storage %s; r2 storage %s",
		share(st.Turso, s.TursoStorageQuota()), share(st.R2, s.R2StorageQuota()))
}

func share(used, quota int64) string {
	return fmt.Sprintf("%s of %s (%.1f%%)", humanBytes(used), humanBytes(quota), float64(used)*100/float64(quota))
}
