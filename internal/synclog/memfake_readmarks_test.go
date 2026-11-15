package synclog

// ReadMarks on the fake log: what the log reports about each origin's size,
// per origin rather than across the whole log.

import (
	"testing"
	"time"
)

// markNow is the timestamp every entry this file builds carries, so a case
// states a time rather than a clock reading.
var markNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// TestMemTransportReadMarksReportsTheLogSize pins the counts a reader of the
// log's shape gets: each origin's head and how many entries it holds. They are
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
	if state.Floors != nil {
		t.Errorf("floors = %v, want nil: nothing has compacted", state.Floors)
	}

	// An empty log reports empty rather than a head of zero per origin: nothing
	// has written, which is a different fact from an origin at the start, and an
	// origin that compacted nothing stays apart from one whose floor is at the
	// start, because the two mean different things to the reader.
	empty, err := NewMemTransport("m9").ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks on an empty log: %v", err)
	}
	if len(empty.Heads) != 0 || len(empty.Entries) != 0 {
		t.Errorf("an empty log reported %+v, want nothing at all", empty)
	}
	if empty.Floors != nil {
		t.Errorf("floors = %v, want nil: nothing has compacted", empty.Floors)
	}
}
