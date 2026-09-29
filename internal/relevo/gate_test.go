package relevo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestGateLineForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rec  store.GateRecord
		tail []string
		want string
	}{
		{
			name: "pass",
			rec:  store.GateRecord{Command: "make check", Result: "pass", ExitCode: 0, DurationMS: 100000, LogPath: "/p/001-gate.log"},
			want: "Gate: make check -- PASS (exit 0, 1m40s). Output: relevo show webshop --round 3 --gate",
		},
		{
			name: "fail",
			rec:  store.GateRecord{Command: "make check", Result: "fail", ExitCode: 2, DurationMS: 100000, LogPath: "/p/001-gate.log"},
			tail: []string{"line four", "line five"},
			want: "Gate: make check -- FAIL (exit 2, 1m40s). Output: relevo show webshop --round 3 --gate\n  line four\n  line five",
		},
		{
			name: "timeout",
			rec:  store.GateRecord{Command: "make check", Result: "timeout", DurationMS: (10 * time.Minute).Milliseconds(), LogPath: "/p/001-gate.log"},
			want: "Gate: make check -- TIMEOUT after 10m0s. Output: relevo show webshop --round 3 --gate",
		},
		{
			name: "error",
			rec:  store.GateRecord{Command: "make check", Result: "error", Note: "no runner"},
			want: "Gate: make check -- ERROR: no runner.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gateLine("webshop", 3, tt.rec, tt.tail); got != tt.want {
				t.Errorf("gateLine() =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestTailLines(t *testing.T) {
	t.Parallel()

	t.Run("last n non-empty lines", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gate.log")
		content := "one\n\ntwo\nthree\n\nfour\nfive\nsix\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		want := []string{"four", "five", "six"}
		got := tailLines(os.ReadFile, path, 3)
		if len(got) != len(want) {
			t.Fatalf("tailLines() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("tailLines() = %v, want %v", got, want)
			}
		}
	})

	t.Run("fewer lines than n returns all non-empty", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gate.log")
		if err := os.WriteFile(path, []byte("only\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := tailLines(os.ReadFile, path, 5)
		if len(got) != 1 || got[0] != "only" {
			t.Fatalf("tailLines() = %v, want [only]", got)
		}
	})

	t.Run("missing file returns nil", func(t *testing.T) {
		got := tailLines(os.ReadFile, filepath.Join(t.TempDir(), "absent.log"), 5)
		if got != nil {
			t.Fatalf("tailLines() = %v, want nil", got)
		}
	})

	t.Run("skips the rusage trailer line", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gate.log")
		content := "a\nb\n\nrelevo-rusage:cpu_usec=1 mem_peak=2\n\nrelevo-exit:2\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		want := []string{"a", "b", "relevo-exit:2"}
		got := tailLines(os.ReadFile, path, 3)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tailLines() = %v, want %v", got, want)
		}
	})

}

// TestGateStepScopesTheGate pins #313: the gate starts in its own
// relevo-gate-* scope, using the template's GateCPUQuota as its CPUQuota, and
// with no scope at all when the runtime has no template.
func TestGateStepScopesTheGate(t *testing.T) {
	t.Parallel()

	runGate := func(t *testing.T, rt Runtime, b store.Binding) {
		t.Helper()
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			_, _, _, err := gateStep(context.Background(), rt, tx, b)
			return err
		}); err != nil {
			t.Fatalf("gateStep: %v", err)
		}
	}

	t.Run("template with a gate quota", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentBinding(t)
		rt.Runner = fr
		rt.Scope = &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "150%", GateCPUQuota: "300%"}
		b.Gate = "make check"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		runGate(t, rt, b)

		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		spec := fr.specs[0]
		if spec.Scope == nil {
			t.Fatal("gate spec.Scope = nil, want a scope from the template")
		}
		wantUnit := "relevo-gate-local-" + b.Name + "-" + strconv.Itoa(b.Round)
		if spec.Scope.Unit != wantUnit {
			t.Errorf("Scope.Unit = %q, want %q", spec.Scope.Unit, wantUnit)
		}
		if spec.Scope.CPUQuota != "300%" {
			t.Errorf("Scope.CPUQuota = %q, want the gate quota 300%%", spec.Scope.CPUQuota)
		}
		if spec.Scope.GateCPUQuota != "" {
			t.Errorf("Scope.GateCPUQuota = %q, want it zeroed", spec.Scope.GateCPUQuota)
		}
	})

	t.Run("nil template", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentBinding(t)
		rt.Runner = fr
		b.Gate = "make check"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		runGate(t, rt, b)

		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		if fr.specs[0].Scope != nil {
			t.Errorf("Scope = %+v, want nil when rt.Scope is nil", fr.specs[0].Scope)
		}
	})

	// The gate runs on its round's core while the round is still open (#314).
	t.Run("template pool and a pinned round", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentBinding(t)
		rt.Runner = fr
		rt.Scope = &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "150%", AllowedCPUs: "0-3"}
		two := 2
		b.RoundCPU = &two
		b.Gate = "make check"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		runGate(t, rt, b)

		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		if got := fr.specs[0].Scope.AllowedCPUs; got != "2" {
			t.Errorf("gate Scope.AllowedCPUs = %q, want the round's core 2", got)
		}
	})
}

// TestGateStepRestartsAGateLostToRestart pins #370, spec §4.4: a gate whose
// last run predates the daemon and that this daemon never saw alive, found
// exited with no exit trailer, is started once more as Attempt 1 -- through the
// same spec as the first run -- and the re-run is logged.
//
// Mutation check: drop the `Attempt == 0` guard (or the lostToRestart call)
// and this fails on Result "error" with no second Start.
func TestGateStepRestartsAGateLostToRestart(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.StartedAt = baseTime
	rt.Watched = NewWatched()
	b.Gate = "make check"
	b.GateRun = &store.GateRun{
		PID:       9001,
		StartedAt: baseTime.Add(-time.Minute).Unix(),
		Round:     b.Round,
		Command:   "make check",
		Attempt:   0,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(9001, false) // exited; no exit() set: no trailer

	var got store.Binding
	var done bool
	var rec *store.GateRecord
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, done, rec, err = gateStep(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("gateStep: %v", err)
	}

	if rec != nil {
		t.Fatalf("rec = %+v, want none: a lost gate is re-run, not reported", rec)
	}
	if done {
		t.Error("done = true, want false while the re-run runs")
	}
	if len(fr.specs) != 1 {
		t.Fatalf("Start calls = %d, want the one re-run", len(fr.specs))
	}
	if want := []string{"sh", "-c", "make check 2>&1"}; !reflect.DeepEqual(fr.specs[0].Argv, want) {
		t.Errorf("re-run Argv = %v, want the first run's spec %v", fr.specs[0].Argv, want)
	}
	if got.GateRun == nil {
		t.Fatal("GateRun = nil after the re-run")
	}
	if got.GateRun.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", got.GateRun.Attempt)
	}
	if got.GateRun.PID != fr.handles[0].PID || got.GateRun.StartedAt != fr.handles[0].StartedAt.Unix() {
		t.Errorf("GateRun = %+v, want the re-run's handle %+v", got.GateRun, fr.handles[0])
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	var gates []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindGate {
			gates = append(gates, e)
		}
	}
	if len(gates) != 1 || gates[0].Note != "gate restarted (lost to a daemon restart): make check" {
		t.Fatalf("KindGate entries = %+v, want one 'gate restarted (lost to a daemon restart)' entry", gates)
	}
}

// TestGateStepSecondLossIsReportedNotRerun pins §4.4's one-re-run bound: a
// gate already at Attempt 1 that is lost again is reported as an error, not
// restarted a second time.
func TestGateStepSecondLossIsReportedNotRerun(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.StartedAt = baseTime
	rt.Watched = NewWatched()
	b.Gate = "make check"
	b.GateRun = &store.GateRun{
		PID:       9001,
		StartedAt: baseTime.Add(-time.Minute).Unix(),
		Round:     b.Round,
		Command:   "make check",
		Attempt:   1,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(9001, false) // exited; no exit() set: no trailer

	var rec *store.GateRecord
	var done bool
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		_, done, rec, err = gateStep(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("gateStep: %v", err)
	}

	if !done {
		t.Error("done = false, want true: the second loss is terminal")
	}
	if rec == nil || rec.Result != "error" || rec.Note != "no exit trailer" {
		t.Fatalf("rec = %+v, want Result \"error\" with Note \"no exit trailer\"", rec)
	}
	if len(fr.specs) != 0 {
		t.Errorf("Start calls = %d, want none: Attempt 1 gets no second re-run", len(fr.specs))
	}
}

// TestGateStepSeenAliveThenNoTrailerIsAnError pins the opposite case: a gate
// this daemon saw alive and that later dies without a trailer is the gate's
// own failure, not the daemon's, so it is reported and never re-run.
//
// Mutation check: drop the Seen clause from lostToRestart and this fails with
// a second Start.
func TestGateStepSeenAliveThenNoTrailerIsAnError(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.StartedAt = baseTime
	rt.Watched = NewWatched()
	b.Gate = "make check"
	pid, started := 9001, baseTime.Add(-time.Minute).Unix()
	b.GateRun = &store.GateRun{PID: pid, StartedAt: started, Round: b.Round, Command: "make check"}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	runGate := func() (*store.GateRecord, bool) {
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

	fr.script(pid, true) // this tick: alive
	if rec, done := runGate(); rec != nil || done {
		t.Fatalf("alive gate: rec = %+v, done = %v, want none and false", rec, done)
	}
	if !rt.Watched.Seen(pid, started) {
		t.Fatal("an alive gate the daemon observed must be marked seen")
	}

	fr.script(pid, false) // next tick: exited, no trailer
	rec, done := runGate()
	if !done {
		t.Error("done = false, want true")
	}
	if rec == nil || rec.Result != "error" || rec.Note != "no exit trailer" {
		t.Fatalf("rec = %+v, want Result \"error\" with Note \"no exit trailer\"", rec)
	}
	if len(fr.specs) != 0 {
		t.Errorf("Start calls = %d, want none: a seen gate's death is not a restart", len(fr.specs))
	}
}

func TestGateTimeoutFor(t *testing.T) {
	t.Parallel()

	t.Run("binding override wins", func(t *testing.T) {
		b := store.Binding{GateTimeoutMS: 5000}
		pol := policy.Policy{}
		if got := gateTimeoutFor(b, pol); got != 5*time.Second {
			t.Fatalf("gateTimeoutFor() = %v, want %v", got, 5*time.Second)
		}
	})

	t.Run("falls back to policy default", func(t *testing.T) {
		b := store.Binding{}
		pol := policy.Policy{}
		if got := gateTimeoutFor(b, pol); got != policy.DefaultGateTimeout {
			t.Fatalf("gateTimeoutFor() = %v, want %v", got, policy.DefaultGateTimeout)
		}
	})

	t.Run("falls back to policy override", func(t *testing.T) {
		ms := 90000
		b := store.Binding{}
		pol := policy.Policy{Gate: &policy.GatePolicy{TimeoutMS: &ms}}
		if got := gateTimeoutFor(b, pol); got != 90*time.Second {
			t.Fatalf("gateTimeoutFor() = %v, want %v", got, 90*time.Second)
		}
	})
}
