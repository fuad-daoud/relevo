package sync

import (
	"context"
	"encoding/json"
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

func readCompactMarker(t *testing.T, kv interface {
	KVGet(string) ([]byte, bool, error)
}) compactState {
	t.Helper()
	var got compactState
	body, ok, err := kv.KVGet(KeyCompact)
	if err != nil || !ok {
		t.Fatalf("read the compaction marker: ok=%v err=%v", ok, err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode the compaction marker: %v", err)
	}
	return got
}

// A machine compacts its own log to the slowest live reader's mark, records
// what it did, and does not run again until it is due.
func TestSteadyCompactsToTheSlowestReaderOnce(t *testing.T) {
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
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if log.compactions != 1 {
		t.Fatalf("compactions = %d, want one", log.compactions)
	}
	got := readCompactMarker(t, local)
	if got.Floor != 3 || got.Dropped != 1 || got.Entries != 2 || got.At.IsZero() {
		t.Fatalf("marker = %+v, want floor 3, one dropped, two entries left", got)
	}

	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("second SyncOnce: %v", res.Err)
	}
	if log.compactions != 1 {
		t.Fatalf("compactions = %d after a quiet second tick, want still one", log.compactions)
	}
}

// With no reader past what the log already records, there is nothing to drop and
// no marker is written.
func TestSteadySkipsCompactionWhenTheFloorWouldNotRise(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := &compactSpy{MemTransport: synclog.NewMemTransport("m1")}
	_, known := shared.SchemaVersions()
	seedFar(t, log.MemTransport, "m1", "b1", known)
	if err := log.OnLog("m2").PublishMarks(map[string]int{"m1": 1}, time.Now()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := log.Compact("m1", 1); err != nil {
		t.Fatalf("compact: %v", err)
	}

	runner := &Runner{Client: log, Local: local}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if log.compactions != 0 {
		t.Fatalf("compactions = %d, want none: the floor is already 1", log.compactions)
	}
}
