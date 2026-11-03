package db

import (
	"testing"
	"time"
)

// TestRecordListArchivedSinceListsAtOrAfter pins the mirror cursor's read:
// rows stamped at or after since, oldest first, scoped to the owner, with no
// JSON carried. The at-or-after boundary is what lets rows sharing a stamp
// through, so the caller's seen set tells a new row from an already mirrored
// one instead of the query hiding it.
func TestRecordListArchivedSinceListsAtOrAfter(t *testing.T) {
	d := openTestDB(t)

	seedRecord(t, d, "alice", "first", "", "")
	seedRecord(t, d, "alice", "second", "", "")
	seedRecord(t, d, "bob", "other", "", "")
	first := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Second)
	archive := func(owner, name string, at time.Time) {
		t.Helper()
		if err := d.Tx(func(tx *Tx) error { return tx.RecordArchive(owner, name, at) }); err != nil {
			t.Fatalf("archive %s/%s: %v", owner, name, err)
		}
	}
	archive("alice", "first", first)
	archive("alice", "second", second)
	archive("bob", "other", second)

	got, err := d.RecordListArchivedSince("alice", first)
	if err != nil {
		t.Fatalf("RecordListArchivedSince: %v", err)
	}
	if len(got) != 2 || got[0].Name != "first" || got[1].Name != "second" {
		t.Fatalf("since first stamp = %+v, want first then second", got)
	}
	for i, want := range []time.Time{first, second} {
		if !got[i].ArchivedAt.Equal(want) {
			t.Errorf("row %d stamp = %v, want %v", i, got[i].ArchivedAt, want)
		}
		if got[i].ID == "" {
			t.Errorf("row %d has no id", i)
		}
	}

	// The boundary is inclusive: a row stamped exactly at since is listed.
	got, err = d.RecordListArchivedSince("alice", second)
	if err != nil {
		t.Fatalf("RecordListArchivedSince at the later stamp: %v", err)
	}
	if len(got) != 1 || got[0].Name != "second" {
		t.Fatalf("since the later stamp = %+v, want just second", got)
	}

	got, err = d.RecordListArchivedSince("alice", second.Add(time.Second))
	if err != nil {
		t.Fatalf("RecordListArchivedSince past every stamp: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("past every stamp = %+v, want none", got)
	}
}
