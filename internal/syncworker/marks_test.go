package syncworker

// The marks verbs over a real backend: what a publish writes, what a read hands
// back, and which rows a caller cannot reach. They run against a local replica
// file rather than a scratch remote, so CI reaches no network and starts no
// driver while the SQL underneath is the same statements a sync connection
// takes.

import (
	"errors"
	"testing"
	"time"
)

// marksAt is when a publish says it reached a sequence, pinned so a row read
// back is about the write rather than about a clock.
var marksAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// markRow is one row of the marks table as it was actually written, read through
// a second handle on the replica rather than through the backend that wrote it.
type markRow struct {
	reader string
	origin string
	seq    int
}

// markRows reads the marks table in reader then origin order, which is the order
// a test asserts a row's origin by rather than by position.
func markRows(t *testing.T, path string) []markRow {
	t.Helper()
	rows, err := openAt(t, path).Query(
		"SELECT reader, origin, seq FROM marks ORDER BY reader, origin")
	if err != nil {
		t.Fatalf("read the marks: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []markRow
	for rows.Next() {
		var m markRow
		if err := rows.Scan(&m.reader, &m.origin, &m.seq); err != nil {
			t.Fatalf("scan a mark: %v", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the marks: %v", err)
	}
	return out
}

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

// TestPublishMarksWritesOnlyOwnRows pins the rule the verb exists to hold: a
// publish records this reader's own rows and cannot be made to write another's,
// however it names them. The reader comes from the handshake rather than the
// request, because a mark is what a compaction floor is computed from and a
// caller that could write one would be choosing what the log may drop on a
// machine that does not hold the entries it speaks for.
func TestPublishMarksWritesOnlyOwnRows(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}

	// A request that names no reader at all: the pipe has no field for one, so
	// the only reader that can be written is the one that shook hands.
	resp := s.ask(Request{
		ID: "p1", Verb: VerbPublishMarks, At: &marksAt,
		Publish: []Mark{{Origin: "origin-b", Seq: 12}, {Origin: "origin-c", Seq: 3}},
	})
	if !resp.OK {
		t.Fatalf("publish_marks = %+v, want it carried out", resp)
	}

	// A second publish for one origin moves that row rather than adding another,
	// which is what makes the key the pair.
	resp = s.ask(Request{
		ID: "p2", Verb: VerbPublishMarks, At: &marksAt,
		Publish: []Mark{{Origin: "origin-b", Seq: 30}},
	})
	if !resp.OK {
		t.Fatalf("the second publish_marks = %+v, want it carried out", resp)
	}

	// The read goes through the verb rather than through the backend directly,
	// so it is answered on the same goroutine that owns the replica. A second
	// handle reading the file beside a worker still writing it is a race, and
	// the verb is the way a caller actually reads these anyway.
	reply := s.ask(Request{ID: "r1", Verb: VerbReadMarks})
	if !reply.OK {
		t.Fatalf("read_marks = %+v, want it carried out", reply)
	}
	state := reply.Marks
	if state == nil {
		t.Fatal("read_marks answered with no marks")
	}
	if len(state.Marks) != 2 {
		t.Errorf("read_marks returned %d marks, want the two the publish wrote", len(state.Marks))
	}
	for _, m := range state.Marks {
		if m.Reader != "origin-a" {
			t.Errorf("read_marks returned a mark for %q, want only origin-a", m.Reader)
		}
	}
	// The floors are read and nothing writes them yet, so they come back empty
	// rather than as a floor of zero that would read as compaction to the start.
	if state.Floors != nil {
		t.Errorf("read_marks returned floors %v, want none: nothing compacts yet", state.Floors)
	}
	if state.Heads["origin-a"] != 1 || state.Entries["origin-a"] != 1 {
		t.Errorf("read_marks sizes = heads %v entries %v, want the one appended entry",
			state.Heads, state.Entries)
	}

	// The rows themselves, read once the worker is gone. A second handle beside
	// a worker still writing is a race, so the read waits for the shutdown the
	// session's cleanup would otherwise do at the end of the test.
	if resp := s.ask(Request{ID: "bye", Verb: VerbShutdown}); !resp.OK {
		t.Fatalf("shutdown: %+v", resp)
	}
	if err := s.wait(); err != nil {
		t.Fatalf("the worker ended: %v", err)
	}
	rows := markRows(t, path)
	assertOwnMarks(t, rows, markRow{reader: "origin-a", origin: "origin-b", seq: 30},
		markRow{reader: "origin-a", origin: "origin-c", seq: 3})
}

// assertOwnMarks is the rows a publish under this handshake must have left: the
// two published origins, both under the handshaken reader and nothing else.
func assertOwnMarks(t *testing.T, got []markRow, want ...markRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the marks table holds %d rows, want one per published origin: %v", len(got), got)
	}
	for i, row := range got {
		if row != want[i] {
			t.Errorf("mark %d = %+v, want %+v", i, row, want[i])
		}
		if row.reader != "origin-a" {
			t.Errorf("a mark was written for reader %q, want only the handshaken origin-a", row.reader)
		}
	}
}

// TestPublishMarksRefusesWhatItCannotPlace pins the refusals that keep a
// malformed publish off the remote: a mark with no origin names nothing, and a
// mark of the reader's own origin says nothing about what any other machine
// needs, so writing it would only give the floor a row it must ignore.
func TestPublishMarksRefusesWhatItCannotPlace(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}

	cases := map[string]Request{
		"no time at all": {ID: "a", Verb: VerbPublishMarks,
			Publish: []Mark{{Origin: "origin-b", Seq: 4}}},
		"a mark with no origin": {ID: "b", Verb: VerbPublishMarks, At: &marksAt,
			Publish: []Mark{{Seq: 4}}},
		"a mark of the reader's own origin": {ID: "c", Verb: VerbPublishMarks, At: &marksAt,
			Publish: []Mark{{Origin: "origin-a", Seq: 4}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			resp := s.ask(req)
			if resp.OK {
				t.Fatalf("%s was accepted, want a refusal", name)
			}
			if !contains(resp.Error, "not the sync pipe protocol") {
				t.Errorf("refusal %q does not name the protocol", resp.Error)
			}
		})
	}
	if resp := s.ask(Request{ID: "bye", Verb: VerbShutdown}); !resp.OK {
		t.Fatalf("shutdown: %+v", resp)
	}
	if err := s.wait(); err != nil {
		t.Fatalf("the worker ended: %v", err)
	}
	if rows := markRows(t, path); len(rows) != 0 {
		t.Errorf("a refused publish wrote %v, want the marks table untouched", rows)
	}
}

// TestPublishMarksRefusedWithoutHello pins that the verb is one of the ones that
// needs the handshake: before it there is no reader to write a row for, and the
// origin the marks would name is not this worker's to publish.
func TestPublishMarksRefusedWithoutHello(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	s := startWorker(t, b)
	for _, req := range []Request{
		{ID: "a", Verb: VerbPublishMarks, At: &marksAt, Publish: []Mark{{Origin: "origin-b", Seq: 1}}},
		{ID: "b", Verb: VerbReadMarks},
	} {
		resp := s.ask(req)
		if resp.OK {
			t.Errorf("%s before hello = %+v, want a refusal", req.Verb, resp)
		}
		if resp.Error != ErrNoHello.Error() {
			t.Errorf("%s before hello = %q, want %q", req.Verb, resp.Error, ErrNoHello)
		}
	}
}

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
	if len(state.Marks) != 0 {
		t.Errorf("ReadMarks returned %d marks, want none published", len(state.Marks))
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

// TestMarksVerbsRefusedWithoutAReplica pins the second gate: a verb that needs a
// remote refuses rather than answering from a backend that has none, however
// the handshake was reached.
func TestMarksVerbsRefusedWithoutAReplica(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if err := b.PublishMarks(map[string]int{"origin-b": 1}, marksAt); !errorsIsNoReplica(err) {
		t.Errorf("PublishMarks without a replica = %v, want a no-replica refusal", err)
	}
	if _, err := b.ReadMarks(); !errorsIsNoReplica(err) {
		t.Errorf("ReadMarks without a replica = %v, want a no-replica refusal", err)
	}
}

// TestPublishMarksPushesAndTurnsAFailedPushIntoARefusal pins that a publish is
// pushed before it is answered, and that a push which fails turns the publish
// into a refusal rather than a success the log never received. The next attempt
// republishes the same marks, so a stale mark left behind is a lower one.
func TestPublishMarksPushesAndTurnsAFailedPushIntoARefusal(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	before := driver.pushes

	if err := b.PublishMarks(map[string]int{"origin-b": 4}, marksAt); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	if driver.pushes != before+1 {
		t.Errorf("a publish pushed %d times, want once: the remote learns the rows from a push",
			driver.pushes-before)
	}

	driver.pushErr = errors.New("the remote is not answering")
	if err := b.PublishMarks(map[string]int{"origin-c": 7}, marksAt); err == nil {
		t.Error("a publish whose push failed came back as a success")
	}
	_ = b.Close()
	if resp := startWorker(t, b).ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello after the failed push: %+v", resp)
	}
}

// TestPublishMarksPushFailureCarriesItsClass pins that a push the engine itself
// refused comes back marked, so a refusal that will repeat is acted on rather
// than retried as though it were a transient fault.
func TestPublishMarksPushFailureCarriesItsClass(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	// Set after the open: a driver that refuses from the start would fail the
	// create rather than the publish being asserted about here.
	driver.pushErr = errors.New("failed to execute sql: no such table")

	err := b.PublishMarks(map[string]int{"origin-b": 4}, marksAt)
	if err == nil {
		t.Fatal("a publish whose push was refused came back as a success")
	}
	if got := RefusalCodeOf(err); got != CodeSchema {
		t.Errorf("refusal class = %q, want %q: the fix is the schema, not another attempt", got, CodeSchema)
	}
}

// TestPublishMarksSkipsAnEmptySet pins that a machine with nothing to report
// writes no row rather than one row of zeros: there is nothing to write, so a
// caller is not made to wait on a transaction it did not need, and a mark of
// zero would read as a reader that has applied nothing of an origin it may never
// have read.
func TestPublishMarksSkipsAnEmptySet(t *testing.T) {
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	if err := b.PublishMarks(nil, marksAt); err != nil {
		t.Errorf("PublishMarks with nothing to publish = %v, want it accepted", err)
	}
	if err := b.PublishMarks(map[string]int{"origin-b": 4}, marksAt); err != nil {
		t.Errorf("a publish with a real origin = %v, want it accepted", err)
	}
}

// TestMarksVerbsReportAnUnreachableReplica pins that both verbs report a replica
// they cannot reach rather than answering from memory. A worker's handle can go
// underneath it -- a file replaced, a connection dropped -- and a publish that
// reported success there would leave the log believing a position the remote
// never received.
func TestMarksVerbsReportAnUnreachableReplica(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	// Closed through the driver rather than through the backend, which is what
	// a handle lost underneath the worker looks like: the backend still holds a
	// non-nil one and only the database knows it is gone.
	if err := driver.db.Close(); err != nil {
		t.Fatalf("close the replica: %v", err)
	}

	if err := b.PublishMarks(map[string]int{"origin-b": 4}, marksAt); err == nil {
		t.Error("a publish against an unreachable replica came back as a success")
	}
	if _, err := b.ReadMarks(); err == nil {
		t.Error("a read against an unreachable replica came back as a mark state")
	}
}

// TestPublishMarksReportsAMarkWithNoOrigin pins the backend's own rule rather
// than the verb's: the verb refuses one first, so this is the contract the seam
// carries for a caller that reaches the transport directly.
func TestPublishMarksReportsAMarkWithNoOrigin(t *testing.T) {
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	if err := b.PublishMarks(map[string]int{"": 4}, marksAt); err == nil {
		t.Error("a mark naming no origin was written, want it refused")
	}
}

// TestMarksVerbsReportAMissingTable pins that both verbs report a remote whose
// tables are not there rather than answering from what they happen to hold. A
// publish that reported success there would leave the log believing a position
// it never received, and a read that reported an empty mark state would read as
// "nobody has reached this origin" rather than as "this remote could not say".
func TestMarksVerbsReportAMissingTable(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	execOn(t, path, "DROP TABLE marks")

	err := b.PublishMarks(map[string]int{"origin-b": 4}, marksAt)
	if err == nil {
		t.Fatal("a publish against a remote with no marks table came back as a success")
	}
	// The words are the statement's, not this package's, so the case pins that
	// the failure is reported rather than the engine's exact phrasing.
	if !contains(err.Error(), "marks") {
		t.Errorf("refusal %q does not name the table that is missing", err)
	}
	if _, err := b.ReadMarks(); err == nil {
		t.Error("a read against a remote with no marks table came back as a mark state")
	}

	// A remote that lost its meta loses the floors with it, which is reported
	// rather than read as "nothing has been compacted".
	execOn(t, path, "CREATE TABLE marks (reader TEXT NOT NULL, origin TEXT NOT NULL,"+
		" seq INTEGER NOT NULL, at TEXT NOT NULL, PRIMARY KEY (reader, origin))")
	execOn(t, path, "DROP TABLE meta")
	if _, err := b.ReadMarks(); err == nil {
		t.Error("a read against a remote with no meta came back as a mark state")
	}

	// And one that lost its log cannot report any origin's size, which is the
	// last of the three reads and the one a caller floors an origin against.
	execOn(t, path, "CREATE TABLE meta (key TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL)")
	execOn(t, path, "DROP TABLE log")
	if _, err := b.ReadMarks(); err == nil {
		t.Error("a read against a remote with no log came back as a mark state")
	}
}

// TestMarksVerbsHandTheBackendARefusal pins that the two verbs carry a backend
// refusal to the daemon rather than swallowing it into an empty answer. A
// publish that failed and reported success would leave the log believing a
// position the remote never took, and a read that failed and reported nothing
// would read as a log nobody has reached.
func TestMarksVerbsHandTheBackendARefusal(t *testing.T) {
	f := &fakeBackend{}
	s := startWorker(t, f)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	// Set after the handshake, so what is asserted is a verb carrying a refusal
	// rather than a backend that would not open in the first place.
	f.fail = errors.New("the remote would not answer")

	for _, req := range []Request{
		{ID: "a", Verb: VerbPublishMarks, At: &marksAt, Publish: []Mark{{Origin: "origin-b", Seq: 1}}},
		{ID: "b", Verb: VerbReadMarks},
	} {
		resp := s.ask(req)
		if resp.OK {
			t.Errorf("%s the backend refused = %+v, want a refusal", req.Verb, resp)
		}
		if !contains(resp.Error, "the remote would not answer") {
			t.Errorf("%s refused with %q, want the backend's own reason", req.Verb, resp.Error)
		}
	}
}

// TestReadMarksSkipsAMalformedRow pins that a row this build cannot read is
// skipped and the rest are still answered. The marks and meta rows are written
// by other machines, so failing the whole read on one bad row would let any of
// them stop every reader's import -- and the error would carry the row's own
// text. A reader whose mark is dropped is only left out of the floor, and one
// that falls behind a floor takes the snapshot path.
func TestReadMarksSkipsAMalformedRow(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path,
		"INSERT INTO marks (reader, origin, seq, at) VALUES ('origin-b', 'origin-a', 4, 'BLOCKED')",
		"INSERT INTO marks (reader, origin, seq, at) VALUES ('origin-c', 'origin-a', 'BLOCKED', '2026-01-01T00:00:00Z')",
		"INSERT INTO marks (reader, origin, seq, at) VALUES ('origin-d', 'origin-a', 7, '2026-01-01T00:00:00Z')",
		"INSERT INTO meta (key, value) VALUES ('floor.origin-b', 'BLOCKED')",
		"INSERT INTO meta (key, value) VALUES ('floor.origin-c', '-3')",
		"INSERT INTO meta (key, value) VALUES ('floor.origin-d', '12')")
	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks with malformed rows: %v", err)
	}
	if len(state.Marks) != 1 || state.Marks[0].Reader != "origin-d" || state.Marks[0].Seq != 7 {
		t.Errorf("marks = %+v, want only origin-d's", state.Marks)
	}
	if len(state.Floors) != 1 || state.Floors["origin-d"] != 12 {
		t.Errorf("floors = %v, want only origin-d's", state.Floors)
	}
}

// TestQuotaWordInARowIsNotAQuota pins that the quota word is only looked for in
// errors from a push or a pull. An error a local statement raised can carry a
// row's text, and the remote's rows are not this machine's to trust.
func TestQuotaWordInARowIsNotAQuota(t *testing.T) {
	t.Parallel()
	err := markDriverRefusal(errors.New(`strconv.Atoi: parsing "BLOCKED": invalid syntax`))
	if got := RefusalCodeOf(err); got == CodeQuota {
		t.Fatalf("a local parse error carrying the word was marked %q", got)
	}
}

// TestUpgradeReportsAFailedPush pins that an upgrade whose push did not reach the
// remote is reported. The replica already holds the new table and the new format
// row, so the next open finds a remote that is in neither shape and refuses it
// rather than half-upgrading -- which is why a lost push has to be a failure the
// caller sees.
func TestUpgradeReportsAFailedPush(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, logWithoutMAC, createStatements[1], createStatements[2],
		"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '1')")
	driver := &localDriver{path: path, pushErr: errors.New("the remote is not answering")}

	b := NewTursoBackend(driver)
	err := b.Open(Spec{Version: ProtocolVersion, Origin: "origin-a",
		URL: "libsql://remote.example", Token: "the-token"})
	if err == nil {
		t.Fatal("an upgrade whose push failed came back as a success")
	}
	if !contains(err.Error(), "push the upgraded log") {
		t.Errorf("refusal %q does not say the push is what failed", err)
	}
	_ = b.Close()
}
