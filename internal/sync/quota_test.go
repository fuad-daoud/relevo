package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

func TestStatusShowsStorageAgainstQuotas(t *testing.T) {
	t.Parallel()
	settings := Settings{QuotaTursoSync: 1000, QuotaTursoStorage: 2000, QuotaR2Storage: 4000}
	q := QuotaReading{
		Account:     Account{Total: 2220, Machines: 3, ReadAt: time.Date(2026, 10, 27, 14, 5, 0, 0, time.UTC)},
		Own:         250,
		Quota:       1000,
		Projected:   3010,
		Projectable: true,
	}
	got := FormatQuotas(ByteCounters{TursoPush: 100, TursoPull: 150}, q, settings,
		Storage{Turso: 500, R2: 2000, At: time.Unix(0, 0).UTC()})
	for _, want := range []string{
		"this machine: turso sync 250 B",
		"account: turso sync 2.2 kB of 1.0 kB (222.0%) across 3 machines, on pace for 3.0 kB, as of 14:05Z",
		"turso storage 500 B of 2.0 kB (25.0%)",
		"r2 storage 2.0 kB of 4.0 kB (50.0%)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
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

func TestSteadyPublishesLogSizeAndMeasuresStorage(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))
	replica := filepath.Join(t.TempDir(), "replica.db")
	if err := os.WriteFile(replica, make([]byte, 321), 0o600); err != nil {
		t.Fatal(err)
	}
	store := blobstore.NewMemBlobStore(nil)
	runner := &Runner{
		Client: synclog.NewMemTransport("m1"), Local: local,
		Blobs: func() blobstore.BlobStore { return store }, ReplicaPath: replica,
	}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil || res.Exported != 1 {
		t.Fatalf("SyncOnce = %+v", res)
	}
	objects, err := store.List(context.Background(), blobstore.SizeKey("m1"))
	if err != nil || len(objects) != 1 {
		t.Fatalf("size objects = %v, %v, want m1's log size", objects, err)
	}
	var size LogSize
	var buf bytes.Buffer
	if _, err := store.Get(context.Background(), objects[0].Key, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &size); err != nil || size.Entries != 1 || size.Bytes <= synclog.LogRowOverhead {
		t.Errorf("published size = %+v, %v, want one entry with its body and overhead", size, err)
	}
	st, err := ReadStorage(local)
	if err != nil || st.Turso != size.Bytes || st.At.IsZero() {
		t.Errorf("storage = %+v, %v, want the remote log's %d bytes and not the replica file's 321", st, err, size.Bytes)
	}

	again := runner.SyncOnce(context.Background(), shared)
	if again.Err != nil || store.Puts != 1 {
		t.Errorf("a second attempt published again: %d puts, %v", store.Puts, again.Err)
	}
}
