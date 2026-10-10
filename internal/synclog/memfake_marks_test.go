package synclog

// MemTransport's marks half: what a publish records, what a read hands back, and
// the one rule the in-memory log exists to prove -- a handle built for one
// installation writes only that installation's rows.

import (
	"testing"
	"time"
)

// TestMemTransportPublishesMarksUnderItsOwnReader pins the rule the fake
// reproduces so the exporter, importer and steady loop can be written against
// it: a mark belongs to the reader that published it, and a second handle on the
// same log writes its own rather than moving another's.
func TestMemTransportPublishesMarksUnderItsOwnReader(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	other := log.OnLog("m2")

	if err := log.PublishMarks(map[string]int{"origin-a": 12}, markNow); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	if err := other.PublishMarks(map[string]int{"origin-a": 3}, markNow); err != nil {
		t.Fatalf("the second reader's PublishMarks: %v", err)
	}

	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if len(state.Marks) != 2 {
		t.Fatalf("the log holds %d marks, want one per reader: %+v", len(state.Marks), state.Marks)
	}
	for _, m := range state.Marks {
		if m.Reader == "m1" && (m.Origin != "origin-a" || m.Seq != 12) {
			t.Errorf("m1's mark = %+v, want origin-a at 12", m)
		}
		if m.Reader == "m2" && (m.Origin != "origin-a" || m.Seq != 3) {
			t.Errorf("m2's mark = %+v, want origin-a at 3", m)
		}
		if m.Reader != "m1" && m.Reader != "m2" {
			t.Errorf("a mark belongs to %q, want one of the two handles", m.Reader)
		}
	}
	// The answer is in reader then origin order, so a case walking it sees the
	// same row every time rather than whichever order a map iterated.
	if state.Marks[0].Reader != "m1" || state.Marks[1].Reader != "m2" {
		t.Errorf("marks came back as %+v, want m1 before m2", state.Marks)
	}

	// The floors come back empty: nothing compacts in this slice, so a floor has
	// no writer here and reporting one would be a value no test could produce.
	if state.Floors != nil {
		t.Errorf("floors = %v, want none", state.Floors)
	}
}

// TestMemTransportMarksOnlyGrow pins that a repeat naming a lower sequence is
// ignored. A mark is read as the furthest a reader has got, and a stale publish
// must not make a reader look as though it went backwards -- which is what would
// raise another machine's floor above entries it still needs.
func TestMemTransportMarksOnlyGrow(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	if err := log.PublishMarks(map[string]int{"origin-a": 30}, markNow); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	if err := log.PublishMarks(map[string]int{"origin-a": 12}, markNow); err != nil {
		t.Fatalf("the stale PublishMarks: %v", err)
	}

	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if len(state.Marks) != 1 {
		t.Fatalf("the log holds %d marks, want the one origin", len(state.Marks))
	}
	if state.Marks[0].Seq != 30 {
		t.Errorf("the mark is at %d, want 30: a lower repeat must not move it back",
			state.Marks[0].Seq)
	}

	// A higher one does move it, and the timestamp with it: the reader's age is
	// what decides whether it still counts towards a floor.
	later := markNow.Add(time.Hour)
	if err := log.PublishMarks(map[string]int{"origin-a": 44}, later); err != nil {
		t.Fatalf("the later PublishMarks: %v", err)
	}
	state, err = log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if state.Marks[0].Seq != 44 || !state.Marks[0].At.Equal(later) {
		t.Errorf("the mark = %+v, want origin-a at 44 as of %s", state.Marks[0], later)
	}
}

// TestMemTransportReadMarksReportsTheLogSize pins the counts a compaction reads
// beside the marks: each origin's head and how many entries it holds. They are
// per origin rather than across the log because a floor is one origin's, and a
// head that mixed two origins would floor one of them past what it holds.
func TestMemTransportReadMarksReportsTheLogSize(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	far := log.OnLog("m2")
	for i, id := range []string{"b1", "b2", "b3"} {
		entry, err := NewUpsert("m1", "board", `["`+id+`"]`, 3,
			[]byte(`{"title":"mine"}`), markNow)
		if err != nil {
			t.Fatalf("build m1's entry %d: %v", i, err)
		}
		if _, err := log.Append([]Entry{entry}); err != nil {
			t.Fatalf("append m1's entry %d: %v", i, err)
		}
	}
	entry, err := NewUpsert("m2", "board", `["far"]`, 3, []byte(`{"title":"theirs"}`), markNow)
	if err != nil {
		t.Fatalf("build m2's entry: %v", err)
	}
	if _, err := far.Append([]Entry{entry}); err != nil {
		t.Fatalf("append m2's entry: %v", err)
	}

	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if state.Heads["m1"] != 3 || state.Entries["m1"] != 3 {
		t.Errorf("m1's size = head %d entries %d, want 3 and 3",
			state.Heads["m1"], state.Entries["m1"])
	}
	if state.Heads["m2"] != 1 || state.Entries["m2"] != 1 {
		t.Errorf("m2's size = head %d entries %d, want 1 and 1",
			state.Heads["m2"], state.Entries["m2"])
	}

	// An empty log reports empty rather than a head of zero per origin: nothing
	// has written, which is a different fact from an origin at the start.
	empty, err := NewMemTransport("m9").ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks on an empty log: %v", err)
	}
	if len(empty.Marks) != 0 || len(empty.Heads) != 0 || len(empty.Entries) != 0 {
		t.Errorf("an empty log reported %+v, want nothing at all", empty)
	}
}

// TestMemTransportPublishesNothingForAnEmptySet pins that a machine with no marks
// to report writes no row rather than one row of zeros: a mark of zero would
// read as a reader that has applied nothing of an origin it may never have read,
// and would hold that origin's floor at the start of its log.
func TestMemTransportPublishesNothingForAnEmptySet(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	if err := log.PublishMarks(nil, markNow); err != nil {
		t.Fatalf("PublishMarks with nothing to publish: %v", err)
	}
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if len(state.Marks) != 0 {
		t.Errorf("an empty publish wrote %+v, want no rows", state.Marks)
	}
}

// TestMemTransportMarksAndTheFloorAgree pins the fake against the rule that
// reads them: what the fake holds, once the lifetime filter is applied, is the
// floor the synclog rule computes. Without this the fake and the rule could
// drift apart and every case written against the fake would be pinned to
// something the real transport does not do.
func TestMemTransportMarksAndTheFloorAgree(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	other := log.OnLog("m2")
	if err := other.PublishMarks(map[string]int{"origin-a": 20}, markNow); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	if err := other.PublishMarks(map[string]int{"origin-b": 5}, markNow); err != nil {
		t.Fatalf("the second PublishMarks: %v", err)
	}

	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if got := FloorFor(state.Marks, "origin-a", 100, markNow); got != 20 {
		t.Errorf("FloorFor for origin-a = %d, want the fake's published 20", got)
	}

	// An origin the fake has no mark for falls back to its head, which is what
	// a machine syncing alone needs.
	if got := FloorFor(state.Marks, "origin-c", 40, markNow); got != 40 {
		t.Errorf("FloorFor for an unread origin = %d, want its head 40", got)
	}
}
