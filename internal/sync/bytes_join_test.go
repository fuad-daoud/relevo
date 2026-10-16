package sync

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// cumulative is a transport whose worker has moved a fixed total by the time the
// join reads it: every Stats call reports the same cumulative totals, as a
// worker that has finished uploading and sits idle does.
type cumulative struct {
	*synclog.MemTransport
	total synclog.Stats
}

func (c *cumulative) Stats() (synclog.Stats, error) { return c.total, nil }

// TestJoinThenSteadyCountsTheWorkersTotalsOnce pins that the bytes a join
// counted are not counted again by the first steady attempt over the same
// worker: the join's reading is carried in its result and seeds the runner, so
// both baselines start from the same totals. A result that drops the reading
// seeds zero, and the runner then folds the whole cumulative total into the
// month a second time, which reads as every byte moved twice.
func TestJoinThenSteadyCountsTheWorkersTotalsOnce(t *testing.T) {
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	total := synclog.Stats{TursoSent: 4000, TursoReceived: 900, R2Put: 7000, R2Get: 300}
	log := &cumulative{MemTransport: synclog.NewMemTransport("m1"), total: total}

	res, err := runEnable(t, shared, local, []byte(joinToken), log)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	runner.SeedStats(res.Stats)
	if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("SyncOnce: %v", out.Err)
	}

	got, err := ReadBytes(local, time.Now())
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	want := ByteCounters{TursoPush: total.TursoSent, TursoPull: total.TursoReceived, R2Put: total.R2Put, R2Get: total.R2Get}
	if got != want {
		t.Fatalf("month counters = %+v, want the worker's totals once: %+v", got, want)
	}
}
