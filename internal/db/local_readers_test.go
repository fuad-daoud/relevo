package db_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The remaining machine-local readers: an ingest cursor, a session's own
// consent, and the markers three one-time passes write. Each is a row that says
// something only about the machine that wrote it, so each reads and writes the
// machine-local file after the split -- and each still answers through a handle
// opened without one.

// localPair opens a split pair in a fresh directory and returns it.
func localPair(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// assertNotOnShared reports a machine-local row the shared file can still see.
func assertNotOnShared(t *testing.T, d *db.DB, key string) {
	t.Helper()
	if _, ok, err := d.KVGet(key); err != nil {
		t.Fatalf("read %s from the shared file: %v", key, err)
	} else if ok {
		t.Errorf("%s is readable on the shared file, so the write went there", key)
	}
}

// TestTheIngestCursorReadsAndWritesLocally pins the cursor: a byte offset and a
// hash of one file on this machine, which would be wrong on any other.
func TestTheIngestCursorReadsAndWritesLocally(t *testing.T) {
	d := localPair(t)

	c := db.Cursor{Source: "planner::/work/plan.md", ByteOffset: 4096, HeadSHA: "abc", UpdatedAt: time.Now().UTC()}
	if err := d.SaveCursor(c); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	assertNotOnShared(t, d, "ingest_cursor")

	got, ok, err := d.Cursor(c.Source)
	if err != nil || !ok {
		t.Fatalf("Cursor = %v, %t, want the saved row", err, ok)
	}
	if got.ByteOffset != c.ByteOffset || got.HeadSHA != c.HeadSHA {
		t.Errorf("Cursor = %+v, want %+v", got, c)
	}
	if _, ok, err := d.Local().Cursor(c.Source); err != nil {
		t.Fatalf("read through the local handle: %v", err)
	} else if !ok {
		t.Error("the local handle does not carry the saved cursor")
	}
}

// TestSessionConsentReadsAndWritesLocally pins the consent answers and the told
// baseline: a session answered a prompt on this machine, and the answer says
// nothing about a session of the same id elsewhere.
func TestSessionConsentReadsAndWritesLocally(t *testing.T) {
	d := localPair(t)
	const kind, session = "claude", "ses_abc"
	now := time.Now().UTC()

	if err := d.SetSessionConsent(kind, session, db.ConsentYes, now); err != nil {
		t.Fatalf("SetSessionConsent: %v", err)
	}
	if err := d.SetSessionTold(kind, session, "token-1", now); err != nil {
		t.Fatalf("SetSessionTold: %v", err)
	}

	got, err := d.SessionConsent(kind, session)
	if err != nil {
		t.Fatalf("SessionConsent: %v", err)
	}
	if got != db.ConsentYes {
		t.Errorf("SessionConsent = %q, want %q", got, db.ConsentYes)
	}
	told, ok, err := d.SessionTold(kind, session)
	if err != nil || !ok {
		t.Fatalf("SessionTold = %q, %t, %v, want the stored baseline", told, ok, err)
	}
	if told != "token-1" {
		t.Errorf("SessionTold = %q, want token-1", told)
	}

	if err := d.ClearSessionConsent(kind, session); err != nil {
		t.Fatalf("ClearSessionConsent: %v", err)
	}
	if got, err := d.SessionConsent(kind, session); err != nil {
		t.Fatalf("SessionConsent after clear: %v", err)
	} else if got != db.ConsentUnset {
		t.Errorf("SessionConsent after clear = %q, want unset", got)
	}
}

// TestTheDedicatedLocalHandleSeesOnlyLocalRows pins the split the other way: a
// row a machine-local surface wrote is not answerable from the local handle's
// opposite. Without this the three tests above could pass by writing and reading
// the shared file.
func TestTheDedicatedLocalHandleSeesOnlyLocalRows(t *testing.T) {
	d := localPair(t)

	if err := d.KVPut("record.of.the.shared.file", []byte(`"history"`)); err != nil {
		t.Fatalf("seed the shared file: %v", err)
	}
	if _, ok, err := d.Local().KVGet("record.of.the.shared.file"); err != nil {
		t.Fatalf("ask the local handle for a shared row: %v", err)
	} else if ok {
		t.Error("the local handle answered with the shared file's row")
	}

	local := d.Local()
	if err := local.KVPut("sync.local.only", []byte(`"here"`)); err != nil {
		t.Fatalf("seed the local file: %v", err)
	}
	if _, ok, err := d.KVGet("sync.local.only"); err != nil {
		t.Fatalf("ask the shared handle for a local row: %v", err)
	} else if ok {
		t.Error("the shared handle answered with the local file's row")
	}
}

// TestThePassMarkersAreMachineLocal pins the one-time markers. Each names
// a pass this machine finished, so a shared copy would stop another
// installation's pass from running over rows it has not stamped.
func TestThePassMarkersAreMachineLocal(t *testing.T) {
	t.Run("split", func(t *testing.T) {
		d := localPair(t)
		if _, ran, err := db.SplitOnce(d, t.TempDir(), time.Now().UTC()); err != nil {
			t.Fatalf("SplitOnce: %v", err)
		} else if !ran {
			t.Fatal("the split did not run on a fresh pair")
		}
		assertNotOnShared(t, d, "split-local.v1")
	})

	t.Run("compress", func(t *testing.T) {
		d := localPair(t)
		if _, ran, err := db.CompressHistoryOnce(d, t.TempDir(), time.Now().UTC()); err != nil {
			t.Fatalf("CompressHistoryOnce: %v", err)
		} else if !ran {
			t.Fatal("the compress pass did not run on a fresh pair")
		}
		assertNotOnShared(t, d, "zstd-compress.v1")
	})

	t.Run("origin backfill", func(t *testing.T) {
		d := localPair(t)
		if _, ran, err := db.BackfillOriginOnce(d, "01ORIGIN", time.Now().UTC()); err != nil {
			t.Fatalf("BackfillOriginOnce: %v", err)
		} else if !ran {
			t.Fatal("the origin backfill did not run on a fresh pair")
		}
		assertNotOnShared(t, d, "origin-backfill.v1")
	})

}

// TestThePassMarkersShortCircuitOnTheLocalFile pins that the marker is read
// where it is written: a second pass finds the first one's marker and does
// nothing, rather than running again and taking a second backup.
func TestThePassMarkersShortCircuitOnTheLocalFile(t *testing.T) {
	d := localPair(t)
	now := time.Now().UTC()
	backup := t.TempDir()

	if _, ran, err := db.BackfillOriginOnce(d, "01ORIGIN", now); err != nil || !ran {
		t.Fatalf("first BackfillOriginOnce = %t, %v, want it to run", ran, err)
	}
	if _, ran, err := db.BackfillOriginOnce(d, backup, now.Add(time.Hour)); err != nil {
		t.Fatalf("second BackfillOriginOnce: %v", err)
	} else if ran {
		t.Error("the second origin backfill ran; its marker was not found")
	}
	if _, ran, err := db.CompressHistoryOnce(d, backup, now); err != nil || !ran {
		t.Fatalf("first CompressHistoryOnce = %t, %v, want it to run", ran, err)
	}
	if _, ran, err := db.CompressHistoryOnce(d, backup, now.Add(time.Hour)); err != nil {
		t.Fatalf("second CompressHistoryOnce: %v", err)
	} else if ran {
		t.Error("the second compress pass ran; its marker was not found")
	}
	if _, ran, err := db.SplitOnce(d, backup, now); err != nil || !ran {
		t.Fatalf("first SplitOnce = %t, %v, want it to run", ran, err)
	}
	if _, ran, err := db.SplitOnce(d, backup, now.Add(time.Hour)); err != nil {
		t.Fatalf("second SplitOnce: %v", err)
	} else if ran {
		t.Error("the second split ran; its marker was not found")
	}
}

// TestAHandleWithoutALocalFileStillReadsAndWrites pins the fallback on the same
// surfaces: a database that predates the split has no local file, and every row
// it has is in the shared one, so the readers keep working.
func TestAHandleWithoutALocalFileStillReadsAndWrites(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	now := time.Now().UTC()
	const source = "planner::/work/plan.md"
	if err := d.SaveCursor(db.Cursor{Source: source, ByteOffset: 12, UpdatedAt: now}); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	if _, ok, err := d.Cursor(source); err != nil || !ok {
		t.Errorf("Cursor = %v, %t, want the saved row on a handle with no local file", err, ok)
	}
	if err := d.SetSessionConsent("claude", "ses_abc", db.ConsentNo, now); err != nil {
		t.Fatalf("SetSessionConsent: %v", err)
	}
	if got, err := d.SessionConsent("claude", "ses_abc"); err != nil {
		t.Fatalf("SessionConsent: %v", err)
	} else if got != db.ConsentNo {
		t.Errorf("SessionConsent = %q, want %q", got, db.ConsentNo)
	}
}
