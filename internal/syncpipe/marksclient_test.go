package syncpipe

// The client's marks read over the same real pipe every other client test uses:
// this binary respawned as a worker, one call at a time. What is pinned is that
// a read comes back as the worker's answer rather than a shape the client
// invented.

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// TestClientReadsMarksOverAPipe pins that a read comes back as the worker's
// answer rather than a shape the client invented: the heads and the entry
// counts are the ones the worker holds, not values this side computed.
func TestClientReadsMarksOverAPipe(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, err := c.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if state.Heads["origin-b"] != 4 || state.Entries["origin-b"] != 4 {
		t.Errorf("sizes = heads %v entries %v, want what the worker reported",
			state.Heads, state.Entries)
	}
}

// TestClientMarksVerbsRefusedByTheWorker pins that a refusal reaches the caller
// as one, rather than as a read that came back empty. An empty mark state would
// read as a log nobody has reached, which is a different fact from one the remote
// refused to answer.
func TestClientMarksVerbsRefusedByTheWorker(t *testing.T) {
	c, err := startFakeWorker(t, modeRefuseVerbs, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.ReadMarks(); err == nil {
		t.Error("a refused read came back as an empty mark state")
	}
}

// TestSupervisorDrivesTheMarksVerbs pins that the supervisor the daemon actually
// holds reaches the worker for the read, through the breaker's accounting like
// every other call. A worker that stops answering is a dead worker whatever it
// was doing, so a call that skipped the breaker would keep it.
func TestSupervisorDrivesTheMarksVerbs(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))

	state, err := s.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if state.Floors != nil {
		t.Errorf("ReadMarks returned floors %v, want none: nothing compacts yet", state.Floors)
	}
}

// TestSupervisorCountsAMarksRefusal pins that a refused marks verb settles the
// breaker as a refusal rather than as a death: the worker is alive and in step,
// and counting it as a death would back a healthy remote off for a call the
// next attempt can make again.
func TestSupervisorCountsAMarksRefusal(t *testing.T) {
	b := breakerOver(newMemKV())
	s := newSupervisor(t, fakeWorkerCfg(modeRefuseVerbs), b)

	if _, err := s.ReadMarks(); err == nil {
		t.Error("a refused read came back as an empty mark state")
	}
	if deaths, _ := b.Deaths(); deaths != 0 {
		t.Errorf("a refused read counted %d deaths, want none", deaths)
	}
}

func TestMarkStateFromWireCarriesTheSizes(t *testing.T) {
	got := markStateFromWire(syncworker.MarkState{
		Sizes: map[string]int64{"a": 7},
		// The wire still names a reader's published positions; feeding one
		// shows the conversion leaves it behind rather than failing the read.
		Marks: []syncworker.PublishedMark{{Reader: "origin-a", Origin: "origin-b", Seq: 4}},
	})
	if got.Sizes["a"] != 7 {
		t.Errorf("Sizes = %v, want a=7", got.Sizes)
	}
}
