//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// markMember gives path the marker tables the sync engine would have written, so
// the question "is this file a sync member" has a real answer without a remote.
// One marker is enough: the check asks whether any of them is there.
func markMember(t *testing.T, path string) {
	t.Helper()
	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS turso_named_syncs (name TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("mark %s as a member: %v", path, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// TestIsSyncMemberReadsTheMarkerRatherThanTheName pins that the answer is the
// file's own schema, and that both directions are decided: a file with a marker
// is a member, and one without is not however much it is written through.
func TestIsSyncMemberReadsTheMarkerRatherThanTheName(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "bare.db")
	if openTestDBPath(t, bare) == nil {
		return
	}

	if member, err := IsSyncMember(bare); err != nil || member {
		t.Fatalf("IsSyncMember of a bare file = %v, %v; want false, nil", member, err)
	}
	markMember(t, bare)
	if member, err := IsSyncMember(bare); err != nil || !member {
		t.Fatalf("IsSyncMember of a marked file = %v, %v; want true, nil", member, err)
	}
	if member, err := IsSyncMember(filepath.Join(dir, "absent.db")); err != nil || member {
		t.Fatalf("IsSyncMember of a file that is not there = %v, %v; want false, nil", member, err)
	}
}

// TestTheCapturePragmaIsSetOnlyForAMember pins the whole of why a push lands:
// capture is asked for per connection, and only a member is asked. A bare file
// that had it would acquire turso_cdc, which is itself one of the markers, so a
// database this package owns would start looking joined to a remote.
func TestTheCapturePragmaIsSetOnlyForAMember(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "bare.db")
	marked := filepath.Join(dir, "marked.db")
	for _, p := range []string{bare, marked} {
		if openTestDBPath(t, p) == nil {
			return
		}
	}
	markMember(t, marked)

	// The read is what the open does, and what records the answer: capture is
	// asked from a record rather than from the schema, because the per-connect
	// path must not reopen the file it is a connection to.
	if member, err := IsSyncMember(marked); err != nil || !member {
		t.Fatalf("IsSyncMember of the marked file = %v, %v; want true, nil", member, err)
	}
	if got := capturePragmas(marked); len(got) != 1 || got[0] != captureChangesPragma {
		t.Errorf("capturePragmas of a member = %v, want the capture pragma", got)
	}
	if got := capturePragmas(bare); len(got) != 0 {
		t.Errorf("capturePragmas of a bare file = %v, want none", got)
	}
}

// TestAMemberFilesWritesAreCaptured is the fix's own claim, measured on the
// database rather than on a mock: a row written through this package's pool over
// a member lands in the capture table the push reads.
//
// Without the pragma the same insert leaves turso_cdc with no row for it, which
// is the state a push reports success over.
func TestAMemberFilesWritesAreCaptured(t *testing.T) {
	dir := t.TempDir()
	member := filepath.Join(dir, "member.db")
	if openTestDBPath(t, member) == nil {
		return
	}
	markMember(t, member)

	// Reopened, because the capture decision is read per connection: the pool
	// open that marked the file is the one that has to ask for the pragma.
	d, err := OpenWith(member, Options{})
	if err != nil {
		t.Fatalf("open the member: %v", err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.sqlDB.ExecContext(context.Background(), `CREATE TABLE t (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.sqlDB.ExecContext(context.Background(), `INSERT INTO t (k) VALUES (1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	raw, err := OpenRaw(member)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var captured int
	if err := raw.QueryRow(`SELECT count(*) FROM turso_cdc`).Scan(&captured); err != nil {
		t.Fatalf("read the capture table: %v", err)
	}
	if captured == 0 {
		t.Error("a write through this package's pool over a sync member was not captured")
	}
}

// TestAMarkingMakesAnAlreadyOpenPoolCapture is the case the daemon is in: the
// pool was opened at start-up, and the membership arrives afterwards. The pool
// has to be made to open fresh connections, because a connection is given the
// pragma when it is created and the ones it already holds were created before
// there was a member to ask about.
func TestAMarkingMakesAnAlreadyOpenPoolCapture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "late.db")
	if openTestDBPath(t, path) == nil {
		return
	}

	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pool.Exec(`INSERT INTO t (k) VALUES (1)`); err != nil {
		t.Fatalf("the insert before the membership: %v", err)
	}
	if captured := countCaptured(t, pool); captured != 0 {
		t.Fatalf("a bare file captured %d rows, want 0", captured)
	}

	MarkSyncMember(path)
	if _, err := pool.Exec(`INSERT INTO t (k) VALUES (2)`); err != nil {
		t.Fatalf("the insert after the membership: %v", err)
	}
	if captured := countCaptured(t, pool); captured == 0 {
		t.Error("a pool open before the file became a member still captured nothing after it did")
	}
}

// TestAClosedPoolsHandleIsForgotten pins that a handle's close takes it out of
// the registry, so a pool that is gone is not reached by a later marking.
func TestAClosedPoolsHandleIsForgotten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "closed.db")
	d := openTestDBPath(t, path)
	if d == nil {
		return
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	memberPoolsMu.Lock()
	defer memberPoolsMu.Unlock()
	if pools := memberPools[path]; len(pools) != 0 {
		t.Errorf("%d pools are still registered over %s after its handle closed", len(pools), path)
	}
}

// TestTheWALCheckpointRefusesASyncMember is the pull's fix. Truncating the log
// over a plain connection is what leaves the sync engine holding a watermark
// naming a frame the log no longer has, and the next pull then dies inside the
// driver. The refusal is made here, where the reason can be named, rather than
// at a later pull with a message that names neither the cause nor the file.
func TestTheWALCheckpointRefusesASyncMember(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "bare.db")
	member := filepath.Join(dir, "member.db")
	for _, p := range []string{bare, member} {
		if openTestDBPath(t, p) == nil {
			return
		}
	}
	markMember(t, member)

	// A bare handle still checkpoints: this refusal is about membership, not a
	// change in what a checkpoint does.
	bareDB, err := OpenWith(bare, Options{})
	if err != nil {
		t.Fatalf("open the bare file: %v", err)
	}
	defer func() { _ = bareDB.Close() }()
	if err := bareDB.walCheckpoint(); err != nil {
		t.Errorf("the checkpoint of a bare file = %v, want none", err)
	}

	memberDB, err := OpenWith(member, Options{})
	if err != nil {
		t.Fatalf("open the member: %v", err)
	}
	defer func() { _ = memberDB.Close() }()
	err = memberDB.walCheckpoint()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("the checkpoint of a member = %v, want ErrInvalid", err)
	}
	if want := "sync member"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal %q does not say %q", err, want)
	}
}

// TestAVacuumRefusesASyncMember pins the same refusal for the swap. A vacuum is
// the worse of the two: it renames a fresh file over the database, which
// discards the log the engine holds a watermark into and leaves it addressing a
// file that is not the one it opened.
func TestAVacuumRefusesASyncMember(t *testing.T) {
	dir := t.TempDir()
	member := filepath.Join(dir, "member.db")
	createBareFile(t, member)
	markMember(t, member)

	// One handle and no other: the gate also refuses a second handle, and a
	// refusal for that reason would pass this test while saying nothing about
	// membership.
	d := openOneHandle(t, member)
	// The swap's own gate, rather than the call: the checkpoint inside Vacuum
	// refuses a member too, so asserting through the call would pass on an
	// implementation that never checked here.
	if err := d.vacuumAllowed(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the vacuum gate of a member = %v, want ErrInvalid", err)
	}
	if err := d.Vacuum(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Vacuum of a member = %v, want ErrInvalid", err)
	}
}

// TestTheVacuumSwapLeavesAMembersFileAlone pins that the refused vacuum changed
// nothing: a refused call that had already replaced the file would be worse than
// the failure it reports.
func TestTheVacuumSwapLeavesAMembersFileAlone(t *testing.T) {
	dir := t.TempDir()
	member := filepath.Join(dir, "member.db")
	createBareFile(t, member)
	markMember(t, member)

	d := openOneHandle(t, member)
	if _, err := d.sqlDB.ExecContext(context.Background(), `CREATE TABLE t (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.sqlDB.ExecContext(context.Background(), `INSERT INTO t (k) VALUES (1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	before, err := os.ReadFile(member)
	if err != nil {
		t.Fatalf("read the file: %v", err)
	}
	if err := d.Vacuum(); err == nil {
		t.Fatal("Vacuum of a member reported no failure")
	}
	after, err := os.ReadFile(member)
	if err != nil {
		t.Fatalf("read the file after: %v", err)
	}
	if len(before) != len(after) {
		t.Errorf("the refused vacuum changed the file: %d bytes -> %d", len(before), len(after))
	}
	if _, err := os.Stat(member + ".vacuum"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused vacuum left a swap sibling behind: %v", err)
	}
}

// countCaptured reads how many rows the capture table holds, treating an absent
// table as none: a file nobody captured into has no such table.
func countCaptured(t *testing.T, pool *sql.DB) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM turso_cdc`).Scan(&n); err != nil {
		return 0
	}
	return n
}

// createFile makes an empty database at path through the raw seam, which takes
// no handle: a test about one handle must not have made a second one first.
func createBareFile(t *testing.T, path string) {
	t.Helper()
	raw, err := OpenRaw(path)
	if err != nil {
		t.Skipf("open %s: %v", path, err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS placeholder (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// openOneHandle opens path as the single direct handle on it.
func openOneHandle(t *testing.T, path string) *DB {
	t.Helper()
	d, err := OpenWith(path, Options{})
	if err != nil {
		t.Skipf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// openTestDBPath creates a database at path through the ordinary open, and
// returns the handle. A nil handle means the open was refused, which the caller
// reports rather than failing a suite over a file it could not make.
func openTestDBPath(t *testing.T, path string) *DB {
	t.Helper()
	d, err := OpenWith(path, Options{})
	if err != nil {
		t.Skipf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
