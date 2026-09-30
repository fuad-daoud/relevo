package relevo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The idle-tick budget guard. A tick of a running daemon, over a store whose
// builders are all still working, is allowed almost nothing: no git call (so
// no subprocess) and a bounded read of each stream. Both budgets are stated
// per tick, so the guard reads them per tick.
//
// The first tick after a daemon starts is a different tick: it mirrors every
// binding once and opens the mirror's cursors. Every assertion below warms
// the daemon with one tick and measures the ones after it.

// idleBudgetRoundBytes is how much each fixture round has already streamed.
// It is far past the tail a drained stream is read from, so a tick that reads
// whole streams pays this and a bounded tick never comes near it.
const idleBudgetRoundBytes = 16 << 20

// idleBudgetBytes is what one idle tick may read over the whole tick: two
// bounded tails with room to spare, and far below one round's stream.
const idleBudgetBytes = 512 << 10

// idleBudgetTicks is how many idle ticks each assertion measures: enough for
// a per-tick read to show up as a multiple, few enough to stay cheap.
const idleBudgetTicks = 3

// idleTickFixture builds the world an idle tick runs over: two live headless
// rounds, each with idleBudgetRoundBytes of stream already behind the drain's
// cursor and neither holding its completion marker, one DONE binding, and a
// real sqlite mirror. Both live bindings are escape-check applicable, so a
// tick that read a tree would pay git for it; both carry a progress sample
// taken inside the fixed clock's interval, so the progress read is not due.
//
// It returns the live bindings' names. The caller warms the daemon with one
// tick, which is allowed first-tick work, before measuring.
func idleTickFixture(t *testing.T) (Runtime, *fakeGit, []string) {
	t.Helper()

	rt := newRuntime(t)
	rt.Runner = newFakeRunner()
	fg := &fakeGit{}
	rt.Git = fg

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	rt.DB = d

	live := []string{"alpha", "beta"}
	for _, name := range live {
		if _, err := Bind(context.Background(), rt, BindOptions{
			Name: name, Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo-" + name,
		}); err != nil {
			t.Fatalf("Bind(%s): %v", name, err)
		}
		if _, err := Send(context.Background(), rt, name, writePlan(t, "do it"), SendOptions{}); err != nil {
			t.Fatalf("Send(%s): %v", name, err)
		}
		b, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("Load(%s): %v", name, err)
		}
		if err := os.WriteFile(rt.Store.StreamPath(name, b.Builder.StreamRound),
			make([]byte, idleBudgetRoundBytes), 0o644); err != nil {
			t.Fatalf("write stream(%s): %v", name, err)
		}
		// The drain has consumed the whole stream, so the next tick reads at
		// most the bounded tail StreamDrained reasons over.
		b.Builder.StreamOffset = idleBudgetRoundBytes
		// Escape-check applicable: a tick that read the tree would pay git.
		b.Repo = "/src/" + name
		b.RoundBaselineTree = "tree-" + name
		// A sample inside the fixed clock's interval: the progress read is not
		// due, so an idle tick takes no tree fingerprint.
		b.Progress = &store.Progress{SampledAt: baseTime}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
	}

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "finished", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo-finished",
	}); err != nil {
		t.Fatalf("Bind(finished): %v", err)
	}
	done, err := rt.Store.Load("finished")
	if err != nil {
		t.Fatalf("Load(finished): %v", err)
	}
	done.State = store.StateDone
	if err := rt.Store.Save(done); err != nil {
		t.Fatalf("Save(finished): %v", err)
	}

	return rt, fg, live
}

// mirrorCursorKey is the source an ingest cursor row is keyed on: the
// binding's mirror log.
func mirrorCursorKey(rt Runtime, name string) string {
	return filepath.Join(rt.Store.Dir(name), "log.jsonl")
}

// TestIdleTickRunsNoGitAndNoMirror pins the idle tick's two cheap bounds at
// once: it makes no git call at all, and it rewrites neither the mirror's
// cursor nor the binding record. A tick that did either would run a git
// subprocess and a mirror transaction every two seconds on a machine whose
// builders are all still working.
func TestIdleTickRunsNoGitAndNoMirror(t *testing.T) {
	rt, fg, live := idleTickFixture(t)
	d := NewDaemon(rt, time.Second)
	ctx := context.Background()

	if err := d.Tick(ctx); err != nil {
		t.Fatalf("warm tick: %v", err)
	}

	cursors := make(map[string]time.Time, len(live))
	records := make(map[string]time.Time, len(live))
	for _, name := range live {
		c, found, err := rt.DB.Cursor(mirrorCursorKey(rt, name))
		if err != nil || !found {
			t.Fatalf("mirror cursor(%s) after the warm tick: found=%v err=%v", name, found, err)
		}
		cursors[name] = c.UpdatedAt
		b, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("Load(%s): %v", name, err)
		}
		records[name] = b.UpdatedAt
	}

	idleFrom := fg.calls
	for i := 0; i < idleBudgetTicks; i++ {
		if err := d.Tick(ctx); err != nil {
			t.Fatalf("idle tick %d: %v", i+1, err)
		}
	}
	if got := fg.calls - idleFrom; got != 0 {
		t.Errorf("%d idle ticks made %d git calls, want none", idleBudgetTicks, got)
	}

	for _, name := range live {
		c, _, err := rt.DB.Cursor(mirrorCursorKey(rt, name))
		if err != nil {
			t.Fatalf("mirror cursor(%s) after the idle ticks: %v", name, err)
		}
		if !c.UpdatedAt.Equal(cursors[name]) {
			t.Errorf("an idle tick rewrote the mirror cursor at %v, want it untouched at %v", c.UpdatedAt, cursors[name])
		}
		b, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("Load(%s) after the idle ticks: %v", name, err)
		}
		if !b.UpdatedAt.Equal(records[name]) {
			t.Errorf("an idle tick rewrote the binding record at %v, want it untouched at %v", b.UpdatedAt, records[name])
		}
	}

	// Positive control, so the zero above is not a dead counter: the
	// completion marker closes the round, and the close pays git for the
	// escape answer.
	name := live[0]
	touch(t, rt.Store.DonePath(name, 1))
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("marker tick: %v", err)
	}
	if fg.calls == idleFrom {
		t.Error("the marker close paid no git call at all; the counter may be dead")
	}
	closed, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("Load(%s) after the marker: %v", name, err)
	}
	if closed.Round != 2 {
		t.Errorf("round after the marker close = %d, want 2", closed.Round)
	}
}

// TestIdleTickReadsUnderBudget pins the idle tick's read volume: with each
// live round's stream idleBudgetRoundBytes past the drain's cursor, a tick
// that still reads whole streams pays megabytes where the budget allows a
// bounded tail. The kernel's own rchar is the counter, so no buffer in the
// test can be mistaken for the read.
func TestIdleTickReadsUnderBudget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/self/io, which is Linux-only")
	}

	rt, _, _ := idleTickFixture(t)
	d := NewDaemon(rt, time.Second)
	ctx := context.Background()

	if err := d.Tick(ctx); err != nil {
		t.Fatalf("warm tick: %v", err)
	}

	before := readRchar(t)
	start := time.Now()
	for i := 0; i < idleBudgetTicks; i++ {
		if err := d.Tick(ctx); err != nil {
			t.Fatalf("idle tick %d: %v", i+1, err)
		}
	}
	elapsed := time.Since(start)
	perTick := (readRchar(t) - before) / idleBudgetTicks

	t.Logf("an idle tick read %d bytes of a %d-byte stream per round in ~%s",
		perTick, int64(idleBudgetRoundBytes), (elapsed / idleBudgetTicks).Round(100*time.Microsecond))

	if perTick >= idleBudgetBytes {
		t.Errorf("an idle tick read %d bytes; the budget is %d, and one fixture round has streamed %d",
			perTick, idleBudgetBytes, idleBudgetRoundBytes)
	}
}

// readRchar is this process's /proc/self/io rchar: every byte the kernel has
// handed it through a read, the protocol file itself included.
func readRchar(t *testing.T) int64 {
	t.Helper()

	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatalf("read /proc/self/io: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		value, ok := strings.CutPrefix(line, "rchar: ")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			t.Fatalf("parse rchar %q: %v", value, err)
		}
		return n
	}
	t.Fatal("no rchar line in /proc/self/io")
	return 0
}
