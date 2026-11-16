package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

func TestStatusShowsStorageAgainstQuotas(t *testing.T) {
	t.Parallel()
	settings := Settings{QuotaTursoSync: 1000, QuotaR2Storage: 4000}
	q := QuotaReading{
		Account:     Account{Total: 2220, Machines: 3, ReadAt: time.Date(2026, 10, 27, 14, 5, 0, 0, time.UTC)},
		Own:         250,
		Quota:       1000,
		Projected:   3010,
		Projectable: true,
	}
	got := FormatQuotas(ByteCounters{TursoPush: 100, TursoPull: 150}, q, settings,
		Storage{R2: 2000, At: time.Unix(0, 0).UTC()})
	for _, want := range []string{
		"this machine: turso sync 250 B",
		"account: turso sync 2.2 kB of 1.0 kB (222.0%) across 3 machines, on pace for 3.0 kB, as of 14:05Z",
		"r2 history 2.0 kB of 4.0 kB (50.0%)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "turso storage") {
		t.Errorf("%q still reports a Turso storage figure, want only the R2 history clause", got)
	}
}

func TestStatusSaysStorageIsNotMeasuredYet(t *testing.T) {
	t.Parallel()
	got := FormatQuotas(ByteCounters{TursoPush: 1}, QuotaReading{}, Settings{}, Storage{})
	if !strings.Contains(got, "not measured yet") || strings.Contains(got, "storage 0 B") {
		t.Errorf("%q should say storage is not measured yet", got)
	}
}

func TestQuotaLatchDoesNotRetry(t *testing.T) {
	t.Parallel()
	b, _ := breakerUnder(t)
	err := fmt.Errorf("push: %w", ErrQuotaReached)
	if !IsPermanentRefusal(err) {
		t.Fatal("a reached quota is not a permanent refusal")
	}
	if rerr := b.Refused(err); rerr != nil {
		t.Fatalf("Refused: %v", rerr)
	}
	if cause, _ := b.LatchCause(); cause != latchQuota {
		t.Fatalf("latch cause = %q, want %q", cause, latchQuota)
	}
	if berr := b.Begin("push"); !errors.Is(berr, ErrLatched) {
		t.Errorf("Begin after the latch = %v, want %v", berr, ErrLatched)
	}
	if due, _ := b.Due(); due {
		t.Error("a latched machine is due")
	}
}

// A steady attempt over a bucket holding segments stores exactly their byte
// total, ignoring body keys by shape, and publishes nothing under an origin's
// former size key. A later segment inside the day leaves the reading alone,
// because the measurement runs at most once a day.
func TestSteadyMeasuresHistoryStorageFromSegments(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))
	store := blobstore.NewMemBlobStore(nil)
	put := func(key string, size int) {
		t.Helper()
		if err := store.Put(context.Background(), key, strings.NewReader(strings.Repeat("x", size)), int64(size)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	put(blobstore.SegmentKey("m2", 0), 100)
	put(blobstore.SegmentKey("m2", 1), 200)
	put(blobstore.Key("m2", strings.Repeat("ab", 32)), 7)
	runner := &Runner{
		Client: synclog.NewMemTransport("m1"), Local: local,
		Blobs: func() blobstore.BlobStore { return store },
	}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil || res.Exported != 1 {
		t.Fatalf("SyncOnce = %+v", res)
	}
	st, err := ReadStorage(local)
	if err != nil || st.R2 != 300 || st.At.IsZero() {
		t.Fatalf("storage = %+v, %v, want exactly the segments' 300 bytes", st, err)
	}
	objects, err := store.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list the bucket: %v", err)
	}
	for _, o := range objects {
		if strings.HasSuffix(o.Key, "/log-size.json") {
			t.Errorf("a size object %q is published, want only segment and body keys", o.Key)
		}
	}
	put(blobstore.SegmentKey("m3", 0), 50)
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("second SyncOnce = %+v", res)
	}
	if again, err := ReadStorage(local); err != nil || again != st {
		t.Errorf("storage after a second attempt = %+v, %v, want the first reading %+v", again, err, st)
	}
}

// A stored reading from before the Turso figure went away still loads: the
// dead key is ignored while the R2 figure and the instant stand.
func TestReadStorageIgnoresTheRetiredTursoKey(t *testing.T) {
	t.Parallel()
	_, local := joinPair(t, "m1")
	at := time.Date(2026, 10, 27, 14, 5, 0, 0, time.UTC)
	if err := local.KVPut(KeyStorage, []byte(`{"turso":500,"r2":2000,"at":"2026-10-27T14:05:00Z"}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	st, err := ReadStorage(local)
	if err != nil || st.R2 != 2000 || !st.At.Equal(at) {
		t.Errorf("storage = %+v, %v, want R2 2000 at %v", st, err, at)
	}
}
