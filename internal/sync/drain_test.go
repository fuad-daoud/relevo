package sync

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// drainPage is the page the worker's pull reads at, mirrored here so a fake can
// hand back one page per call and a drain loop has a second call to make.
const drainPage = 1024

// pagingLog hands back at most one page per Pull, like the worker's read does
// per origin, so a run that imported once would leave the rest of the origin
// behind.
type pagingLog struct {
	*synclog.MemTransport
	page int
}

func (l *pagingLog) Pull(marks map[string]int) ([]synclog.Entry, error) {
	all, err := l.MemTransport.Pull(marks)
	if err != nil || len(all) <= l.page {
		return all, err
	}
	return all[:l.page], nil
}

// seedFarMany writes total entries of one other origin into the log.
func seedFarMany(t *testing.T, log *synclog.MemTransport, origin string, total, schema int) {
	t.Helper()
	for i := 0; i < total; i++ {
		seedFar(t, log, origin, fmt.Sprintf("b%05d", i), schema)
	}
}

// TestJoinDrainsEveryPageOfAnOrigin pins the join's import loop: a backlog of
// more than one page is read to the end in one join because the loop runs
// Import again while a run moved a mark. The mutation is a single Import, which
// stops after the first page and leaves the rest behind.
func TestJoinDrainsEveryPageOfAnOrigin(t *testing.T) {
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	log := synclog.NewMemTransport("m1")
	_, known := shared.SchemaVersions()
	const total = drainPage + 1
	seedFarMany(t, log, "m2", total, known)

	paged := &pagingLog{MemTransport: log, page: drainPage}
	res, err := runEnable(shared, local, []byte(joinToken), paged)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if res.Imported.Applied != total {
		t.Fatalf("the join imported %d entries, want all %d across pages", res.Imported.Applied, total)
	}
	if seq, _, err := shared.ImportMark("m2"); err != nil || seq != total {
		t.Fatalf("import mark for m2 = (%d, %v), want the last entry applied", seq, err)
	}
}

// TestSteadyDrainsEveryPageOfAnOrigin pins the steady import loop the same way:
// one attempt reads a backlog larger than a page to the end.
func TestSteadyDrainsEveryPageOfAnOrigin(t *testing.T) {
	shared, local := joinPair(t, "m1")
	log := synclog.NewMemTransport("m1")
	_, known := shared.SchemaVersions()
	const total = drainPage + 1
	seedFarMany(t, log, "m2", total, known)

	paged := &pagingLog{MemTransport: log, page: drainPage}
	// A page of real applies is not instant, so the step bound is set above the
	// time a page takes; the loop's own deadline is what this test does not
	// reach.
	runner := &Runner{Client: paged, Local: local, Timeout: 30 * time.Second}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if res.Applied != total {
		t.Fatalf("the attempt applied %d entries, want all %d across pages", res.Applied, total)
	}
}

// TestSteadyHeldOriginStopsTheDrain pins that a run which moved no mark stops
// the loop rather than spinning to the deadline: an origin held by a newer
// schema returns its report and the attempt finishes at once.
func TestSteadyHeldOriginStopsTheDrain(t *testing.T) {
	shared, local := joinPair(t, "m1")
	log := synclog.NewMemTransport("m1")
	_, known := shared.SchemaVersions()
	seedFar(t, log, "m2", "b1", known+1)

	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	start := time.Now()
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if len(res.Trouble.Held) != 1 {
		t.Fatalf("Trouble.Held = %+v, want the held origin", res.Trouble)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a held origin took %s, want the loop to stop on a run that moved nothing", elapsed)
	}
}
