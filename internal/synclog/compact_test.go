package synclog

import (
	"sort"
	"testing"
	"time"
)

func upsertAt(seq int, tbl, pk string) Entry {
	return Entry{Origin: "o", Seq: seq, Batch: seq, Table: tbl, PK: pk, Op: OpUpsert}
}

func deleteAt(seq int, tbl, pk string) Entry {
	return Entry{Origin: "o", Seq: seq, Batch: seq, Table: tbl, PK: pk, Op: OpDelete}
}

func droppedSeqs(drop map[int]bool) []int {
	out := make([]int, 0, len(drop))
	for seq := range drop {
		out = append(out, seq)
	}
	sort.Ints(out)
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A row whose only newer entry is above the floor keeps its older one: a reader
// applying up to the floor must meet the parent of every child it applies.
func TestCompactionKeepsStateAsOfFloor(t *testing.T) {
	t.Parallel()
	entries := []Entry{
		upsertAt(5, "binding", `["P"]`),
		upsertAt(6, "binding", `["C"]`),
		upsertAt(20, "binding", `["P"]`),
	}
	if got := droppedSeqs(compactDrops(entries, 10)); len(got) != 0 {
		t.Fatalf("compacting to 10 dropped %v, want P@5 kept because its only successor is above the floor", got)
	}
	if got := droppedSeqs(compactDrops(entries, 20)); !sameInts(got, []int{5}) {
		t.Fatalf("compacting to 20 dropped %v, want only P@5", got)
	}
}

// A delete entry that is the latest for its row survives, so a reader that held
// the row and missed its removal still removes it.
func TestCompactionKeepsDeleteEntries(t *testing.T) {
	t.Parallel()
	entries := []Entry{
		upsertAt(1, "binding", `["X"]`),
		deleteAt(2, "binding", `["X"]`),
		upsertAt(3, "binding", `["Y"]`),
	}
	if got := droppedSeqs(compactDrops(entries, 3)); !sameInts(got, []int{1}) {
		t.Fatalf("compacting dropped %v, want the superseded upsert and not the delete", got)
	}
}

func appendOne(t *testing.T, m *MemTransport, tbl, pk string) {
	t.Helper()
	if _, err := m.Append([]Entry{{Table: tbl, PK: pk, Op: OpUpsert, Body: []byte(`{"id":"x"}`), SchemaVersion: 1}}); err != nil {
		t.Fatalf("append %s: %v", pk, err)
	}
}

func TestFloorNeverMovesDown(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("o")
	for n := 0; n < 10; n++ {
		appendOne(t, m, "binding", `["a"]`)
	}
	if _, err := m.Compact("o", 10); err != nil {
		t.Fatalf("compact to 10: %v", err)
	}
	if _, err := m.Compact("o", 5); err != nil {
		t.Fatalf("compact to 5: %v", err)
	}
	state, err := m.ReadMarks()
	if err != nil {
		t.Fatalf("read marks: %v", err)
	}
	if got := state.Floors["o"]; got != 10 {
		t.Fatalf("floor = %d after compacting to 10 then 5, want 10", got)
	}
}

func TestMemCompactDropsAndRecordsTogether(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("o")
	appendOne(t, m, "binding", `["a"]`)
	appendOne(t, m, "binding", `["b"]`)
	appendOne(t, m, "binding", `["a"]`)
	dropped, err := m.Compact("o", 3)
	if err != nil || dropped != 1 {
		t.Fatalf("Compact = %d, %v; want the one superseded entry dropped", dropped, err)
	}
	left, _ := m.PullRange("o", 0, 3)
	if len(left) != 2 || left[0].Seq != 2 || left[1].Seq != 3 {
		t.Fatalf("entries left = %v, want seq 2 and 3", left)
	}
	if _, err := m.Compact("o", 99); err == nil {
		t.Fatal("compacting past the head succeeded, want a refusal")
	}
}

func TestCompactDueOnTenPercentGrowth(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	last := now.Add(-time.Hour)
	if !CompactDue(last, 100, 110, now) {
		t.Error("110 entries after 100 is a tenth of growth, want due")
	}
	if CompactDue(last, 100, 109, now) {
		t.Error("109 entries after 100 is under a tenth, want not due")
	}
}

func TestCompactDueWeekly(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if !CompactDue(now.Add(-7*24*time.Hour), 100, 100, now) {
		t.Error("a week after the last run, want due")
	}
	if CompactDue(now.Add(-7*24*time.Hour+time.Minute), 100, 100, now) {
		t.Error("just under a week with no growth, want not due")
	}
}

func TestCompactDueNeverRan(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if !CompactDue(time.Time{}, 0, 1, now) {
		t.Error("never ran with an entry, want due")
	}
	if CompactDue(time.Time{}, 0, 0, now) {
		t.Error("never ran with nothing to compact, want not due")
	}
}
