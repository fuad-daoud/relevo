package syncworker

import (
	"errors"
	"testing"
)

// execOnErr runs one statement against a replica path, for the rows a case seeds
// that no verb in this slice writes.
func execOnErr(t *testing.T, path, stmt string) error {
	t.Helper()
	_, err := openAt(t, path).Exec(stmt)
	return err
}

// errorsIsNoReplica reports whether err is the refusal a verb gets before
// hello opened one.
func errorsIsNoReplica(err error) bool { return errors.Is(err, ErrNoHello) }

// TestReadMarksReportsWhatTheLogHolds pins the three answers a compaction reads
// beside the marks: each origin's head, how many entries it holds, and the
// floors the remote has recorded. The floors are seeded directly because
// nothing writes them in this slice, and a read that did not find them would
// floor an origin below what the remote has already dropped.
func TestReadMarksReportsWhatTheLogHolds(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-a", "board", `["a"]`, `{"title":"one"}`),
		upsertEntry("origin-a", "board", `["b"]`, `{"title":"two"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := execOnErr(t, path, "INSERT INTO meta (key, value) VALUES ('floor.origin-a', '4')"); err != nil {
		t.Fatalf("seed a floor: %v", err)
	}

	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if state.Floors["origin-a"] != 4 {
		t.Errorf("floors = %v, want origin-a at 4", state.Floors)
	}
	if state.Heads["origin-a"] != 2 || state.Entries["origin-a"] != 2 {
		t.Errorf("sizes = heads %v entries %v, want the two appended entries",
			state.Heads, state.Entries)
	}
	if want := int64(2*len(`{"title":"one"}`) + 2*logRowOverhead); state.Sizes["origin-a"] != want {
		t.Errorf("sizes = %v, want origin-a at %d: the bodies plus the per-row overhead", state.Sizes, want)
	}
}

// TestReadMarksRefusedWithoutAReplica pins the second gate: a verb that needs a
// remote refuses rather than answering from a backend that has none, however
// the handshake was reached.
func TestReadMarksRefusedWithoutAReplica(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.ReadMarks(); !errorsIsNoReplica(err) {
		t.Errorf("ReadMarks without a replica = %v, want a no-replica refusal", err)
	}
}

// TestReadMarksReportsAnUnreachableReplica pins that ReadMarks reports a
// replica it cannot reach rather than answering from memory.
func TestReadMarksReportsAnUnreachableReplica(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	// Closed through the driver rather than through the backend, which is what
	// a handle lost underneath the worker looks like: the backend still holds a
	// non-nil one and only the database knows it is gone.
	if err := driver.db.Close(); err != nil {
		t.Fatalf("close the replica: %v", err)
	}

	if _, err := b.ReadMarks(); err == nil {
		t.Error("a read against an unreachable replica came back as a mark state")
	}
}

// TestReadMarksReportsAMissingTable pins that ReadMarks reports a remote whose
// tables are not there rather than answering from what they happen to hold.
func TestReadMarksReportsAMissingTable(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	execOn(t, path, "DROP TABLE log")

	_, err := b.ReadMarks()
	if err == nil {
		t.Fatal("a read against a remote with no log table came back as a mark state")
	}
	if !contains(err.Error(), "log") {
		t.Errorf("refusal %q does not name the table that is missing", err)
	}

	// A remote that lost its meta loses the floors with it, which is reported
	// rather than read as empty floors.
	execOn(t, path, "CREATE TABLE log (origin TEXT, seq INTEGER, batch INTEGER, tbl TEXT, pk TEXT, op TEXT, schema_version INTEGER, body TEXT, at TEXT, mac BLOB, after_origin TEXT, after_seq INTEGER)")
	execOn(t, path, "DROP TABLE meta")
	if _, err := b.ReadMarks(); err == nil {
		t.Error("a read against a remote with no meta came back as a mark state")
	}
}

// TestReadMarksHandsTheBackendARefusal pins that ReadMarks carries a backend
// refusal to the daemon rather than swallowing it into an empty answer.
func TestReadMarksHandsTheBackendARefusal(t *testing.T) {
	f := &fakeBackend{}
	s := startWorker(t, f)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	// Set after the handshake, so what is asserted is a verb carrying a refusal
	// rather than a backend that would not open in the first place.
	f.fail = errors.New("the remote would not answer")

	req := Request{ID: "b", Verb: VerbReadMarks}
	resp := s.ask(req)
	if resp.OK {
		t.Errorf("%s the backend refused = %+v, want a refusal", req.Verb, resp)
	}
	if !contains(resp.Error, "the remote would not answer") {
		t.Errorf("%s refused with %q, want the backend's own reason", req.Verb, resp.Error)
	}
}
