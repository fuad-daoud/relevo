package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestGateAdvanceCarriesTheRusageTrailer pins that a settled gate record
// carries the run's cgroup measurement: a present trailer joins pass, fail
// and error records alike, while a missing trailer -- and a timed-out run,
// which never reads one -- leaves the record nil. The supervisor's output
// is unchanged: the read only fills the record.
func TestGateAdvanceCarriesTheRusageTrailer(t *testing.T) {
	t.Parallel()

	base, burst := int64(100<<20), int64(300<<20)
	trailer := spawn.ProcRusage{CPUMS: 1200, BaseBytes: &base, BurstBytes: &burst}
	pass, fail := 0, 2

	tests := []struct {
		name       string
		exit       *int
		trailer    *spawn.ProcRusage
		timedOut   bool
		wantResult string
		wantRusage bool
	}{
		{name: "pass carries the trailer", exit: &pass, trailer: &trailer, wantResult: "pass", wantRusage: true},
		{name: "fail carries the trailer", exit: &fail, trailer: &trailer, wantResult: "fail", wantRusage: true},
		{name: "error with a trailer carries it", trailer: &trailer, wantResult: "error", wantRusage: true},
		{name: "missing trailer records nil", wantResult: "error", wantRusage: false},
		{name: "timeout records nil", timedOut: true, wantResult: "timeout", wantRusage: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fr := newFakeRunner()
			rt, b := sentBinding(t)
			rt.Runner = fr
			rt.StartedAt = baseTime
			rt.Watched = NewWatched()
			b.Gate = "make check"
			pid, started := 9001, baseTime.Add(-time.Minute).Unix()
			b.GateRun = &store.GateRun{PID: pid, StartedAt: started, Round: b.Round, Command: "make check"}
			if tc.timedOut {
				b.GateTimeoutMS = 1
			}
			if err := rt.Store.Save(b); err != nil {
				t.Fatal(err)
			}
			advance := func() (*store.GateRecord, bool) {
				t.Helper()
				var rec *store.GateRecord
				var done bool
				if err := rt.Store.WithLock(func(tx *store.Tx) error {
					var err error
					_, done, rec, err = gateStep(context.Background(), rt, tx, b)
					return err
				}); err != nil {
					t.Fatalf("gateStep: %v", err)
				}
				return rec, done
			}

			if tc.timedOut {
				fr.script(pid, true)
			} else {
				// A first tick sees the run alive, so its later death
				// without an exit trailer is the gate's own failure.
				fr.script(pid, true)
				if rec, done := advance(); rec != nil || done {
					t.Fatalf("alive gate: rec = %+v, done = %v, want none and false", rec, done)
				}
				fr.script(pid, false)
				if tc.exit != nil {
					fr.exit(pid, *tc.exit)
				}
			}
			if tc.trailer != nil {
				fr.setRusage(pid, *tc.trailer)
			}
			rec, done := advance()
			if !done {
				t.Fatal("done = false, want the run settled")
			}
			if rec == nil || rec.Result != tc.wantResult {
				t.Fatalf("rec = %+v, want Result %q", rec, tc.wantResult)
			}
			if tc.wantRusage {
				if rec.Rusage == nil {
					t.Fatalf("rec.Rusage = nil, want the trailer %+v", *tc.trailer)
				}
				got := *rec.Rusage
				if got.CPUMS != tc.trailer.CPUMS || got.BaseBytes == nil || *got.BaseBytes != base || got.BurstBytes == nil || *got.BurstBytes != burst {
					t.Errorf("rec.Rusage = %+v, want the trailer's measurement", got)
				}
			} else if rec.Rusage != nil {
				t.Errorf("rec.Rusage = %+v, want nil", *rec.Rusage)
			}
		})
	}
}
