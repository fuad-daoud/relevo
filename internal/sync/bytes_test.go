package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// readings is a transport whose Stats reports a scripted sequence of cumulative
// totals, one per call, as a worker that restarts between two of them would.
type readings struct {
	*synclog.MemTransport
	totals []synclog.Stats
}

func (r *readings) Stats() (synclog.Stats, error) {
	if len(r.totals) == 0 {
		return synclog.Stats{}, errors.New("no more readings")
	}
	s := r.totals[0]
	r.totals = r.totals[1:]
	return s, nil
}

// A worker that restarts counts from zero again; the new total is what it moved,
// not a negative delta, so nothing it moved is lost and nothing is subtracted.
func TestBytesAccumulateAcrossWorkerRestart(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	client := &readings{MemTransport: synclog.NewMemTransport("m1"), totals: []synclog.Stats{
		{TursoSent: 1000, TursoReceived: 200, R2Put: 50, R2Get: 5},
		{TursoSent: 1500, TursoReceived: 300, R2Put: 80, R2Get: 5},
		{TursoSent: 100, TursoReceived: 40, R2Put: 7, R2Get: 3},
	}}
	runner := &Runner{Client: client, Local: local, Timeout: time.Second}
	for range 3 {
		if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
			t.Fatalf("SyncOnce: %v", res.Err)
		}
	}
	got, err := ReadBytes(local, time.Now())
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	want := ByteCounters{TursoPush: 1600, TursoPull: 340, R2Put: 87, R2Get: 8}
	if got != want {
		t.Fatalf("counters = %+v, want %+v", got, want)
	}
}

func TestBytesKeyIsTheUTCMonth(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("far", 14*3600)
	at := time.Date(2026, 11, 1, 3, 0, 0, 0, zone) // still October in UTC
	if got := BytesKey(at); got != "sync.bytes.2026-10" {
		t.Fatalf("BytesKey = %q", got)
	}
}

func TestStatusShowsBytesAgainstQuota(t *testing.T) {
	t.Parallel()
	got := FormatBytes(ByteCounters{TursoPush: 1_500_000_000, TursoPull: 500_000_000, R2Put: 2_000, R2Get: 12}, Settings{}.TursoSyncQuota())
	for _, want := range []string{"turso push 1.5 GB", "pull 500.0 MB", "r2 put 2.0 kB", "get 12 B", "turso sync 2.0 GB of 10.0 GB (20.0%)"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if q := (Settings{QuotaTursoSync: 5}).TursoSyncQuota(); q != 5 {
		t.Errorf("explicit quota = %d", q)
	}
	st := Settings{}
	if st.TursoStorageQuota() != 9_000_000_000 || st.R2StorageQuota() != 10_000_000_000 {
		t.Errorf("defaults = %d, %d", st.TursoStorageQuota(), st.R2StorageQuota())
	}
	if _, err := ParseSettings([]byte(`{"quota_r2_storage": -1}`)); err == nil {
		t.Error("a negative quota was accepted")
	}
}

func TestMissingBlobIsAPermanentRefusal(t *testing.T) {
	t.Parallel()
	if !IsPermanentRefusal(fmt.Errorf("import: %w", synclog.ErrBlobMissing)) {
		t.Fatal("a missing body is not a permanent refusal")
	}
}
