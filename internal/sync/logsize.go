package sync

// What the daemon measures for status: the history's storage in the bucket,
// summed from the segment listing.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

const (
	// KeyStorage is the storage last measured, as a Storage.
	KeyStorage = "sync.storage"

	storageEvery = 24 * time.Hour
	// measureTimeout bounds the bucket listing the storage measurement reads.
	measureTimeout = 2 * time.Minute
)

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

// Storage is what this machine last measured of the bucket's history. A zero
// At is a machine that has not measured yet.
type Storage struct {
	R2 int64     `json:"r2"`
	At time.Time `json:"at"`
}

// ReadStorage returns the last measurement, zero when there is none.
func ReadStorage(kv db.KV) (Storage, error) {
	var s Storage
	if err := marker(kv, KeyStorage, &s); err != nil {
		return Storage{}, err
	}
	return s, nil
}

// measureStorage records the bucket's segment bytes, at most once a day.
//
// The listing carries the figure: segmentTotals counts segment keys by shape,
// so no Turso read is needed. The driver reports no database size, only WAL
// sizes and network counters, so no Turso figure is stored.
func (r *Runner) measureStorage(now time.Time) {
	store := r.bucket()
	if store == nil {
		return
	}
	prior, err := ReadStorage(r.Local)
	if err == nil && now.Sub(prior.At) < storageEvery {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), measureTimeout)
	defer cancel()
	_, total, _, err := segmentTotals(ctx, store)
	if err != nil {
		slog.Warn("sync: measure the bucket", "err", err)
		return
	}
	r.put(KeyStorage, Storage{R2: total, At: now})
}

// FormatQuotas is the status clause for the month's Turso traffic -- this
// machine's counts, then the account's against the quota -- and the history's
// storage in R2 against its quota. Storage never measured reads "not measured
// yet", never zero.
func FormatQuotas(c ByteCounters, q QuotaReading, s Settings, st Storage) string {
	out := FormatBytes(c) + "; " + FormatAccount(q)
	if st.At.IsZero() {
		return out + "; storage: not measured yet"
	}
	return out + "; r2 history " + share(st.R2, s.R2StorageQuota())
}

// FormatAccount is the account clause of the month: every machine's published
// row plus this machine's counters against the shared quota. The pace part is
// dropped while the month is too young to project. A snapshot never read says
// so rather than a total that would read as a quiet month.
func FormatAccount(q QuotaReading) string {
	if q.Account.ReadAt.IsZero() {
		return "account: not read yet"
	}
	out := fmt.Sprintf("account: turso sync %s", humanBytes(q.Account.Total))
	if q.Quota > 0 {
		out += fmt.Sprintf(" of %s (%.1f%%)", humanBytes(q.Quota), q.UsedShare()*100)
	}
	out += " across " + Machines(q.Account.Machines)
	if q.Projectable && q.Projected > 0 {
		out += fmt.Sprintf(", on pace for %s", humanBytes(q.Projected))
	}
	return out + ", as of " + q.Account.ReadAt.UTC().Format("15:04") + "Z"
}

func share(used, quota int64) string {
	return fmt.Sprintf("%s of %s (%.1f%%)", humanBytes(used), humanBytes(quota), float64(used)*100/float64(quota))
}
