package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestSilenceVerdictNeedsGapAndIdleCPU(t *testing.T) {
	t.Parallel()

	now := baseTime
	after := 8 * time.Minute
	for _, tc := range []struct {
		name string
		gap  time.Duration
		busy bool
		want bool
	}{
		{"gap under", after - time.Second, false, false},
		{"gap exactly", after, false, true},
		{"gap over", after + time.Minute, false, true},
		{"gap over but busy", after + time.Minute, true, false},
	} {
		got, quiet := silenceVerdict(now.Add(-tc.gap), now, after, tc.busy, true)
		if got != tc.want || quiet != tc.gap {
			t.Errorf("%s: silent=%v quiet=%s, want %v %s", tc.name, got, quiet, tc.want, tc.gap)
		}
	}
}

func TestSilenceVerdictNeverFiresWithUnknownCPU(t *testing.T) {
	t.Parallel()

	if got, _ := silenceVerdict(baseTime.Add(-time.Hour), baseTime, time.Minute, false, false); got {
		t.Error("silent with unknown CPU")
	}
}

func TestSilenceIgnoresOtherHarnesses(t *testing.T) {
	t.Parallel()

	for kind, want := range map[string]bool{"opencode": true, "claude": false, "agy": false} {
		b := store.Binding{RoundStartedAt: baseTime, Builder: store.Endpoint{Kind: kind, PID: 7}}
		if got := silenceDue(b); got != want {
			t.Errorf("silenceDue(%s) = %v, want %v", kind, got, want)
		}
	}
	if silenceDue(store.Binding{Builder: store.Endpoint{Kind: "opencode", PID: 7}}) {
		t.Error("silenceDue with no open round")
	}
	if silenceDue(store.Binding{RoundStartedAt: baseTime, Builder: store.Endpoint{Kind: "opencode"}}) {
		t.Error("silenceDue with no process")
	}
}

// silentFixture is an opencode round 30 minutes in whose stream last moved
// quiet ago and whose tree last burned CPU cpuAgo ago, with the progress
// sample just taken so a tick reads no /proc.
func silentFixture(t *testing.T, quiet, cpuAgo time.Duration) (Runtime, store.Binding) {
	t.Helper()
	rt, b := opencodeSent(t, newFakeRunner())
	now := baseTime.Add(30 * time.Minute)
	rt = at(rt, 30*time.Minute)
	b.RoundStartedAt = now.Add(-30 * time.Minute)
	b.Progress = &store.Progress{
		SampledAt: now, TreeAt: b.RoundStartedAt, OutputAt: now.Add(-quiet),
		CPUTicks: 5, CPUTicksAt: now.Add(-cpuAgo),
	}
	stream := rt.Store.RunnerStreamPath(b.Name, b.Round)
	if err := os.WriteFile(stream, []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stream, now.Add(-quiet), now.Add(-quiet)); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return rt, b
}

func stallNotes(t *testing.T, rt Runtime) int {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == store.KindSwitch && strings.HasPrefix(e.Note, providerStallNotePrefix) {
			n++
		}
	}
	return n
}

// noteStall runs the daemon tick's stall note against the stored log. The tick
// itself is not driven because it would sample the real /proc.
func noteStall(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		return noteProviderStall(context.Background(), rt, tx, b, entries, rt.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHeadlessStatusShowsModelSilentForMinutes(t *testing.T) {
	t.Parallel()

	rt, b := silentFixture(t, 12*time.Minute+30*time.Second, 20*time.Minute)
	got, _ := headlessStatus(context.Background(), rt, b, false, nil)
	if got != "model silent for 12 min" {
		t.Errorf("status = %q, want %q", got, "model silent for 12 min")
	}

	rt, b = silentFixture(t, 12*time.Minute, time.Minute)
	if got, _ := headlessStatus(context.Background(), rt, b, false, nil); got == "model silent for 12 min" {
		t.Errorf("status = %q while the CPU is busy", got)
	}
}

func TestProviderStallNotedOncePerEpisode(t *testing.T) {
	t.Parallel()

	rt, b := silentFixture(t, 12*time.Minute, 20*time.Minute)
	noteStall(t, rt, b)
	noteStall(t, rt, b)
	noteStall(t, rt, b)
	if n := stallNotes(t, rt); n != 1 {
		t.Errorf("provider stall notes after three silent ticks = %d, want 1", n)
	}
}

func TestProviderStallNotNotedWhileCPUBusy(t *testing.T) {
	t.Parallel()

	rt, b := silentFixture(t, 12*time.Minute, time.Minute)
	noteStall(t, rt, b)
	if n := stallNotes(t, rt); n != 0 {
		t.Errorf("provider stall notes with a busy CPU = %d, want 0", n)
	}
}

func TestProgressStepTracksCPUBurn(t *testing.T) {
	t.Parallel()

	rt := progressRt()
	b := store.Binding{Name: "w", Round: 1, RoundStartedAt: progressStart}
	t0 := progressStart.Add(time.Minute)
	b = progressStep(rt, b, t0, signals{cpuTicks: 1000, cpuOK: true})
	idle := t0.Add(time.Minute)
	b = progressStep(rt, b, idle, signals{cpuTicks: 1000 + silenceCPUFloor, cpuOK: true})
	if !b.Progress.CPUTicksAt.Equal(t0) {
		t.Errorf("CPUTicksAt = %s after an idle sample, want %s", b.Progress.CPUTicksAt, t0)
	}
	busy := idle.Add(time.Minute)
	b = progressStep(rt, b, busy, signals{cpuTicks: 1000 + 2*silenceCPUFloor + 1, cpuOK: true})
	if !b.Progress.CPUTicksAt.Equal(busy) {
		t.Errorf("CPUTicksAt = %s after a busy sample, want %s", b.Progress.CPUTicksAt, busy)
	}
	b = progressStep(rt, b, busy.Add(time.Minute), signals{})
	if !b.Progress.CPUTicksAt.IsZero() {
		t.Errorf("CPUTicksAt = %s after an unreadable sample, want zero", b.Progress.CPUTicksAt)
	}
}

func TestProcessTreeCPUTicksSumsDescendants(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	put := func(pid, ppid, ut, st int, comm string) {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		line := strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) +
			" 0 0 0 0 0 0 0 0 0 " + strconv.Itoa(ut) + " " + strconv.Itoa(st) + " 0 0 20 0 1 0 1\n"
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(10, 1, 1, 2, "supervisor")
	put(11, 10, 10, 20, "open code) x")
	put(12, 11, 100, 200, "go")
	put(13, 1, 1000, 1000, "unrelated")

	got, ok := processTreeCPUTicksIn(root, 10)
	if !ok || got != 333 {
		t.Errorf("ticks = %d ok=%v, want 333 true", got, ok)
	}
	if _, ok := processTreeCPUTicksIn(root, 99); ok {
		t.Error("a missing pid reported ok")
	}
}
