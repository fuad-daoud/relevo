package sync

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// compactSpy counts the compactions that reached the log.
type compactSpy struct {
	*synclog.MemTransport
	compactions int
}

func (c *compactSpy) CompactOwn(floor int) (synclog.CompactResult, error) {
	c.compactions++
	return c.MemTransport.CompactOwn(floor)
}

// A superseded entry stays in the log however far every reader has moved past
// it: the steady tick never compacts.
func TestSteadyNeverCompactsTheLog(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := &compactSpy{MemTransport: synclog.NewMemTransport("m1")}
	_, known := shared.SchemaVersions()
	seedFar(t, log.MemTransport, "m1", "b1", known)
	seedFar(t, log.MemTransport, "m1", "b2", known)
	seedFar(t, log.MemTransport, "m1", "b1", known)
	if err := log.OnLog("m2").PublishMarks(map[string]int{"m1": 3}, time.Now()); err != nil {
		t.Fatalf("publish the reader's mark: %v", err)
	}

	runner := &Runner{Client: log, Local: local}
	for i := range 2 {
		if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
			t.Fatalf("SyncOnce %d: %v", i+1, res.Err)
		}
	}
	if log.compactions != 0 {
		t.Fatalf("compactions = %d, want none", log.compactions)
	}
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("read the marks: %v", err)
	}
	if got := state.Entries["m1"]; got != 3 {
		t.Fatalf("m1 holds %d entries, want all 3 kept", got)
	}
	if got := state.Floors["m1"]; got != 0 {
		t.Fatalf("m1 floor = %d, want none recorded", got)
	}
}
