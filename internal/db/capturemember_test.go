//go:build !modernc

package db

// The fake boundary for the property the re-land exists for: rows written
// through a member handle after the file is a member reach the change set a push
// reads. No remote is in reach, so the measurement is the change set itself --
// which is exactly what the push streams.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memberFile builds a file the daemon's handle holds and makes it a member the
// way the enable does: the marker tables first, then the record.
func memberFile(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	handle, err := OpenSplit(path, Options{})
	if err != nil {
		t.Fatalf("open the shared file: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	// The driver's open is what joins a file; the marker tables stand in for it
	// here so the test needs no remote.
	MarkSyncMember(path)
	return handle, path
}

// capturedChanges is how many change-set rows the push would stream for a table.
func capturedChanges(t *testing.T, path, table string) int {
	t.Helper()
	pool, err := OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("open the member read-only: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var n int
	if err := pool.QueryRow(`SELECT COUNT(*) FROM turso_cdc WHERE table_name = ?`, table).Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			// The capture tables appear when something asks for capture, so their
			// absence before the first write is a change set of nothing.
			return 0
		}
		t.Fatalf("count the change set for %s: %v", table, err)
	}
	return n
}

// TestAMemberHandleWriteReachesTheChangeSet is the fake-boundary half of the
// end-to-end property: after the file is a member, a write through the handle
// the daemon holds lands in the change set a push streams. Before the re-land it
// landed in the database and nowhere else.
func TestAMemberHandleWriteReachesTheChangeSet(t *testing.T) {
	handle, path := memberFile(t)
	if got := capturedChanges(t, path, "binding"); got != 0 {
		t.Fatalf("the seeded binding is already in the change set (%d changes), so the gap this closes is not there", got)
	}

	err := handle.Tx(func(tx *Tx) error {
		_, err := tx.UpsertBinding(Binding{
			ID:           "written-after-the-member-open",
			Name:         "written-after-the-member-open",
			CWD:          "/tmp",
			CreatedAt:    mustTime(t),
			IngestSource: IngestLive,
		})
		return err
	})
	if err != nil {
		t.Fatalf("the write through the member handle: %v", err)
	}
	if got := capturedChanges(t, path, "binding"); got == 0 {
		t.Error("a write through a member handle reached no change set, so no push can carry it")
	}
}

// TestANonMemberHandleWriteReachesNoChangeSet is the other direction: a file the
// driver never joined must not be given the capture pragma. Its tables are the
// sync driver's marker tables, so a bare file that carried them would read as a
// member to every role gate in the tree.
func TestANonMemberHandleWriteReachesNoChangeSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bare.db")
	handle, err := OpenSplit(path, Options{})
	if err != nil {
		t.Fatalf("open the bare file: %v", err)
	}
	defer func() { _ = handle.Close() }()

	err = handle.Tx(func(tx *Tx) error {
		_, err := tx.UpsertBinding(Binding{
			ID:           "written-on-a-bare-file",
			Name:         "written-on-a-bare-file",
			CWD:          "/tmp",
			CreatedAt:    mustTime(t),
			IngestSource: IngestLive,
		})
		return err
	})
	if err != nil {
		t.Fatalf("the write through the bare handle: %v", err)
	}

	pool, err := OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("open the bare file read-only: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var tables int
	if err := pool.QueryRow(
		`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name IN (?, ?, ?)`,
		"turso_cdc", "turso_sync_last_change_id", "turso_named_syncs").Scan(&tables); err != nil {
		t.Fatalf("read the marker tables: %v", err)
	}
	if tables != 0 {
		t.Errorf("a bare file carries %d sync marker tables, so it reads as a member", tables)
	}
}

// TestIsSyncMemberAnswersFromTheMarkerTables pins the membership read: a joined
// file answers true and a bare one false, and answering true is what records the
// path so the handle's writes carry the pragma.
func TestIsSyncMemberAnswersFromTheMarkerTables(t *testing.T) {
	_, member := memberFile(t)
	ok, err := IsSyncMember(member)
	if err != nil {
		t.Fatalf("IsSyncMember on a member: %v", err)
	}
	if !ok {
		t.Error("a file carrying the marker tables does not read as a member")
	}

	bare := filepath.Join(t.TempDir(), "bare.db")
	if ok, err := IsSyncMember(bare); err != nil || ok {
		t.Errorf("IsSyncMember on a bare file = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := IsSyncMember(""); err != nil || ok {
		t.Errorf("IsSyncMember on an empty path = (%v, %v), want (false, nil)", ok, err)
	}
}

// mustTime is the fixture's fixed creation stamp.
func mustTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := parseTime("2026-01-01T00:00:00.000Z")
	if err != nil {
		t.Fatalf("parse the fixture's stamp: %v", err)
	}
	return parsed
}
