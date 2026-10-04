package db

import (
	"testing"
	"time"
)

// seedRecord puts one binding_record row in, with its link columns as given.
func seedRecord(t *testing.T, d *DB, owner, name, linkOrigin, linkID string) {
	t.Helper()
	if _, err := d.RecordPut(Record{
		Owner: owner, Name: name, State: "open",
		JSON:      `{}`,
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		// A row that predates the link columns carries NULL in both; a bound
		// row carries the other installation's id in both.
		LinkOrigin: linkOrigin,
		LinkID:     linkID,
	}); err != nil {
		t.Fatalf("seed record %s/%s: %v", owner, name, err)
	}
}

// TestRecordListUnlinkedNamesOnlyNullLinkRows pins the read the :sync view lists
// from: the rows that carry no link at all, and only those.
//
// It is a real-database test on purpose. The view draws whatever the snapshot
// holds, so the decision about which rows are unlinked is made here, in the
// WHERE clause, and a fake cannot observe a WHERE clause at all. Linking one
// fixture row is the mutation that matters here: if the predicate admitted a
// linked row, this test is the only thing that would notice.
func TestRecordListUnlinkedNamesOnlyNullLinkRows(t *testing.T) {
	d := openTestDB(t)

	seedRecord(t, d, "alice", "webshop", "", "")            // never linked
	seedRecord(t, d, "bob", "shopfront", "", "")            // never linked
	seedRecord(t, d, "carol", "docs", "02INSTALL", "rec-9") // bound to another machine
	// Half-bound: migration 015 makes each link column independently nullable,
	// so a row can name the other installation without naming the row there.
	// That is not "no link" and must not be listed as though it were.
	seedRecord(t, d, "dave", "half", "02INSTALL", "")

	got, err := d.RecordListUnlinked()
	if err != nil {
		t.Fatalf("RecordListUnlinked: %v", err)
	}
	want := []string{"shopfront", "webshop"} // name-ordered
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, r := range got {
		if r.Name != want[i] {
			t.Errorf("row %d is %q, want %q", i, r.Name, want[i])
		}
		if r.LinkOrigin != "" || r.LinkID != "" {
			t.Errorf("%s is listed but carries the link %q/%q", r.Name, r.LinkOrigin, r.LinkID)
		}
	}

	// Linking a row takes it out of the list. This is the whole contract: a
	// bound row is not waiting to be bound, and listing it would ask a user to
	// re-bind something already bound.
	if err := d.Tx(func(tx *Tx) error {
		_, err := tx.exec(`UPDATE binding_record SET link_origin = ?, link_id = ? WHERE name = ?`,
			"02INSTALL", "rec-1", "webshop")
		return err
	}); err != nil {
		t.Fatalf("link webshop: %v", err)
	}
	got, err = d.RecordListUnlinked()
	if err != nil {
		t.Fatalf("RecordListUnlinked after linking: %v", err)
	}
	if len(got) != 1 || got[0].Name != "shopfront" {
		t.Fatalf("after linking one row the list is %+v, want just shopfront", got)
	}
}

// TestRecordListUnlinkedReadsEveryOrigin pins that the list is not scoped to the
// reading handle's origin. A remote binding that arrived from another machine is
// precisely the row a user needs to see unlinked, so scoping it by origin would
// hide the thing the list exists to show.
func TestRecordListUnlinkedReadsEveryOrigin(t *testing.T) {
	d := openTestDB(t)
	seedRecord(t, d, "alice", "from-here", "", "")
	seedRecord(t, d, "bob", "from-there", "", "")

	got, err := d.RecordListUnlinked()
	if err != nil {
		t.Fatalf("RecordListUnlinked: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want both origins: %+v", len(got), got)
	}
}

// TestInstallationListIsUnscoped pins that the directory names the other
// installations too. A list scoped to the reader's own origin would be one row
// long and would say nothing about who else has written here.
func TestInstallationListIsUnscoped(t *testing.T) {
	d := openTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := d.Tx(func(tx *Tx) error { return tx.InstallationTouch("02INSTALL", "zen", at) }); err != nil {
		t.Fatalf("touch zen: %v", err)
	}
	if err := d.Tx(func(tx *Tx) error { return tx.InstallationTouch("01INSTALL", "laptop", at) }); err != nil {
		t.Fatalf("touch laptop: %v", err)
	}

	got, err := d.InstallationList()
	if err != nil {
		t.Fatalf("InstallationList: %v", err)
	}
	want := []string{"laptop", "zen"} // label-ordered
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, in := range got {
		if in.Label != want[i] {
			t.Errorf("row %d is %q, want %q", i, in.Label, want[i])
		}
		if in.LastSeen.IsZero() {
			t.Errorf("%s has no last_seen", in.Label)
		}
	}
}
