package syncpipe

// The client's marks half over the same real pipe every other client test uses:
// this binary respawned as a worker, one call at a time. What is pinned is that
// a publish crosses as the daemon's own view of its marks with no reader among
// them, and that a read comes back as the worker's answer rather than a shape
// the client invented.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// TestClientPublishesAndReadsMarksOverAPipe drives both verbs across a real pipe.
//
// The publish is checked on the far side rather than on the reply, because a
// publish answers nothing: what matters is that the worker was handed the
// origins and sequences the daemon read, with no reader to name.
func TestClientPublishesAndReadsMarksOverAPipe(t *testing.T) {
	record := filepath.Join(t.TempDir(), "published.jsonl")
	cfg := handshake()
	cfg.Env = append(cfg.Env, publishedMarksEnv+"="+record)
	c, err := startFakeWorker(t, modeNormal, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := c.PublishMarks(map[string]int{"origin-c": 9, "origin-b": 4}, pinnedAt); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	published := readPublished(t, record)
	if len(published) != 1 {
		t.Fatalf("the worker was asked to publish %d times, want once", len(published))
	}
	got := published[0]
	if got.Marks["origin-b"] != 4 || got.Marks["origin-c"] != 9 {
		t.Errorf("the worker was handed %v, want the two origins the daemon read", got.Marks)
	}
	at, err := time.Parse(time.RFC3339Nano, got.At)
	if err != nil {
		t.Fatalf("decode the publish time %q: %v", got.At, err)
	}
	if !at.Equal(pinnedAt) {
		t.Errorf("the publish carried %s, want the daemon's own %s: only it knows when it got there",
			at, pinnedAt)
	}

	state, err := c.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if len(state.Marks) != 1 {
		t.Fatalf("ReadMarks returned %d marks, want the one the worker holds", len(state.Marks))
	}
	m := state.Marks[0]
	if m.Reader != "origin-a" || m.Origin != "origin-b" || m.Seq != 4 {
		t.Errorf("mark = %+v, want origin-a's mark of origin-b at 4", m)
	}
	if !m.At.Equal(pinnedAt) {
		t.Errorf("mark time = %s, want %s", m.At, pinnedAt)
	}
	if state.Heads["origin-b"] != 4 || state.Entries["origin-b"] != 4 {
		t.Errorf("sizes = heads %v entries %v, want what the worker reported",
			state.Heads, state.Entries)
	}
}

// TestClientPublishesAnEmptyMarkSet pins that a machine with nothing to report
// still gets its call answered rather than sending a publish with no origins in
// it: a set with nothing in it is a fact about this machine, and refusing it
// would turn every idle window into a failure.
func TestClientPublishesAnEmptyMarkSet(t *testing.T) {
	record := filepath.Join(t.TempDir(), "published.jsonl")
	cfg := handshake()
	cfg.Env = append(cfg.Env, publishedMarksEnv+"="+record)
	c, err := startFakeWorker(t, modeNormal, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := c.PublishMarks(nil, pinnedAt); err != nil {
		t.Fatalf("PublishMarks with nothing to publish: %v", err)
	}
	if published := readPublished(t, record); len(published) != 1 {
		t.Errorf("the worker was asked to publish %d times, want once", len(published))
	}
}

// TestClientMarksVerbsRefusedByTheWorker pins that a refusal reaches the caller
// as one, rather than as a publish that appeared to go through or a read that
// came back empty. An empty mark state would read as a log nobody has reached,
// which is a different fact from one the remote refused to answer.
func TestClientMarksVerbsRefusedByTheWorker(t *testing.T) {
	c, err := startFakeWorker(t, modeRefuseVerbs, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := c.PublishMarks(map[string]int{"origin-b": 1}, pinnedAt); err == nil {
		t.Error("a refused publish came back as a success")
	}
	if _, err := c.ReadMarks(); err == nil {
		t.Error("a refused read came back as an empty mark state")
	}
}

// TestMarksToWireIsSorted pins the order a mark set crosses in. The wire is
// what a golden line and a failing diff are read against, so the same marks must
// encode to the same line every time rather than in whatever order a map
// happened to iterate.
func TestMarksToWireIsSorted(t *testing.T) {
	marks := map[string]int{"origin-c": 3, "origin-a": 1, "origin-b": 2}
	got := marksToWire(marks)
	want := []syncworker.Mark{
		{Origin: "origin-a", Seq: 1},
		{Origin: "origin-b", Seq: 2},
		{Origin: "origin-c", Seq: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("marksToWire = %+v, want %d marks", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mark %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if got := marksToWire(nil); got != nil {
		t.Errorf("marksToWire(nil) = %+v, want nil so the request carries no field", got)
	}
	if got := markStateFromWire(syncworker.MarkState{}); len(got.Marks) != 0 {
		t.Errorf("markStateFromWire of an empty reply = %+v, want no marks", got)
	}
}

// TestSupervisorDrivesTheMarksVerbs pins that the supervisor the daemon actually
// holds reaches the worker for both, through the breaker's accounting like every
// other call. A worker that stops answering a publish is a dead worker whatever
// it was doing, so a call that skipped the breaker would keep it.
func TestSupervisorDrivesTheMarksVerbs(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))

	if err := s.PublishMarks(map[string]int{"origin-b": 4}, pinnedAt); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	state, err := s.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if len(state.Marks) != 1 || state.Marks[0].Reader != "origin-a" {
		t.Errorf("ReadMarks = %+v, want the worker's one mark", state.Marks)
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

	if err := s.PublishMarks(map[string]int{"origin-b": 1}, pinnedAt); err == nil {
		t.Fatal("a refused publish came back as a success")
	}
	if deaths, _ := b.Deaths(); deaths != 0 {
		t.Errorf("a refused publish counted %d deaths, want none", deaths)
	}
	if _, err := s.ReadMarks(); err == nil {
		t.Error("a refused read came back as an empty mark state")
	}
	if deaths, _ := b.Deaths(); deaths != 0 {
		t.Errorf("a refused read counted %d deaths, want none", deaths)
	}
}

func TestMarkStateFromWireCarriesTheSizes(t *testing.T) {
	got := markStateFromWire(syncworker.MarkState{Sizes: map[string]int64{"a": 7}})
	if got.Sizes["a"] != 7 {
		t.Errorf("Sizes = %v, want a=7", got.Sizes)
	}
}
