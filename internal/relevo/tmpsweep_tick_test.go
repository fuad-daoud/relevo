package relevo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/housekeep"
)

// The tick hook tests stay on the daemon: the sweep itself moved to
// housekeep, but the pacing and the empty-root skip belong to the tick.

// tickSweepSetup is a runtime naming an empty tmp root -- the root the sweep
// walks but the store never made, which is exactly what a crashed process
// leaves for it.
func tickSweepSetup(t *testing.T) Runtime {
	t.Helper()
	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	return rt
}

func tickTmpMkDir(t *testing.T, path string, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

func tickTmpRoundDir(t *testing.T, rt Runtime, name string, round int, mod time.Time) string {
	t.Helper()
	return tickTmpMkDir(t, filepath.Join(rt.TmpRoot, housekeep.RoundTmpRel(name, round)), mod)
}

// TestTickRunsTmpSweep pins the hook: the sweep runs on a daemon's first tick,
// before Tick's no-bindings early return, off the tick's own binding list.
func TestTickRunsTmpSweep(t *testing.T) {
	t.Parallel()

	rt := tickSweepSetup(t)
	ghost := tickTmpRoundDir(t, rt, "ghost", 1, baseTime.Add(-time.Hour))

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustBeGone(t, ghost)
}

// TestTickPacesTmpSweep pins the sweep's own clock: the first tick of a
// process walks the root, later ticks wait out the interval on the runtime's
// clock rather than paying for the walk every tick, and the next tick past the
// interval walks again.
func TestTickPacesTmpSweep(t *testing.T) {
	t.Parallel()

	rt := tickSweepSetup(t)
	now := baseTime
	rt.Now = func() time.Time { return now }
	d := NewDaemon(rt, time.Second)

	old := baseTime.Add(-time.Hour)
	ghost := tickTmpRoundDir(t, rt, "ghost", 1, old)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustBeGone(t, ghost)

	// A second entry inside the interval: the tick that must not sweep also
	// must not remove.
	kept := tickTmpRoundDir(t, rt, "ghost", 1, old)
	now = baseTime.Add(housekeep.SweepEvery - time.Minute)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustExist(t, kept)

	now = baseTime.Add(housekeep.SweepEvery)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustBeGone(t, kept)
}

// TestTickSkipsTmpSweepWithEmptyRoot pins the empty-root skip: a runtime with
// no tmp root has nothing to sweep, and the tick touches nothing -- not even
// a same-named tree under its own working directory.
func TestTickSkipsTmpSweepWithEmptyRoot(t *testing.T) {
	sandbox := t.TempDir()
	decoy := tickTmpMkDir(t, filepath.Join(sandbox, "rounds", "here-001"), baseTime.Add(-time.Hour))

	rt := newRuntime(t)
	if rt.TmpRoot != "" {
		t.Fatalf("TmpRoot = %q, want the empty value a test runtime names", rt.TmpRoot)
	}
	t.Chdir(sandbox)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustExist(t, decoy)
	if removed, err := housekeep.SweepTmp(context.Background(), housekeepDeps(rt), nil, baseTime); removed != nil || err != nil {
		t.Errorf("SweepTmp with no tmp root = (%v, %v), want (nil, nil)", removed, err)
	}
}
