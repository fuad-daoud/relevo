package relevo

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The tests here drive the whole tick rather than the phase, so a phase removed
// from it fails them. The property they pin is the outbox's life on a machine
// whose sync is off: it collects an entry per shared-table write whether or not
// anything drains it, and the daemon empties it on a window. With sync on the
// entries are the log a drain will read, so nothing on this path may drop them.

// outboxRuntime returns a runtime whose store is a real machine database with
// entries in its outbox, and a driver connection to that same file so a test
// counts what the tick left behind.
func outboxRuntime(t *testing.T) (Runtime, *sql.DB) {
	t.Helper()
	st := store.New(t.TempDir())
	mdb, err := st.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	seedOutboxWrite(t, mdb, "one")
	seedOutboxWrite(t, mdb, "two")

	raw, err := db.OpenRaw(st.DBPath())
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	return Runtime{Store: st}, raw
}

// seedOutboxWrite writes one repo row, whose directory is the row's natural
// key, so a shared-table write lands an entry in the outbox.
func seedOutboxWrite(t *testing.T, mdb *db.DB, dir string) {
	t.Helper()
	if err := mdb.Tx(func(tx *db.Tx) error {
		_, err := tx.UpsertRepo(db.Repo{CommonDir: &dir})
		return err
	}); err != nil {
		t.Fatalf("seed a repo row: %v", err)
	}
}

// outboxCount reads how many entries the outbox holds.
func outboxCount(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&n); err != nil {
		t.Fatalf("count sync_outbox: %v", err)
	}
	return n
}

// TestSyncOutboxTruncatesWhileOff pins the off half: a machine with no enabled
// mark collects entries and the tick empties them.
//
// It runs the tick rather than the phase, so the phase being on the tick is what
// it checks first: removing that line leaves the fixture's two entries in place
// and this fails.
func TestSyncOutboxTruncatesWhileOff(t *testing.T) {
	t.Parallel()

	rt, raw := outboxRuntime(t)
	if got := outboxCount(t, raw); got != 2 {
		t.Fatalf("the fixture recorded %d outbox entries, want 2", got)
	}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := outboxCount(t, raw); got != 0 {
		t.Errorf("the outbox holds %d entries after a tick with sync off, want none", got)
	}
}

// TestSyncOutboxKeptWhileOn pins the half that must never regress: a machine
// whose mark says sync is on keeps every entry, because those entries are what
// its drain reads.
//
// The mutation is inverting the enabled check, which truncates an on-state
// machine's log and leaves this failing on a non-zero count.
func TestSyncOutboxKeptWhileOn(t *testing.T) {
	t.Parallel()

	rt, raw := outboxRuntime(t)
	mdb, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, baseTime); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	seedOutboxWrite(t, mdb, "three")

	want := outboxCount(t, raw)
	if want == 0 {
		t.Fatal("the fixture recorded no outbox entries, so the comparison is vacuous")
	}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := outboxCount(t, raw); got != want {
		t.Errorf("the outbox holds %d entries after a tick with sync on, want %d", got, want)
	}
}
