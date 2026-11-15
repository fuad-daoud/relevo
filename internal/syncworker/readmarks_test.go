package syncworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
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

// TestReadMarksReportsWhatTheLogHolds pins the answers a reader takes beside the
// marks: each origin's head, how many entries it holds, the bytes of its
// segments, and the floor it has recorded. The floor is seeded directly because
// nothing writes it in this slice, and a read that did not find it would floor
// an origin below what the remote has already dropped. A second origin at floor
// zero stays out of Floors while its head is still reported.
func TestReadMarksReportsWhatTheLogHolds(t *testing.T) {
	path := tempReplica(t)
	b, store := backendWithStore(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append one: %v", err)
	}
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"two"}`)}); err != nil {
		t.Fatalf("append two: %v", err)
	}
	other := backendOnStore(t, &localDriver{path: path}, "origin-b", store)
	if _, err := other.Append([]Entry{upsertEntry("origin-b", "board", `["c"]`, `{"title":"three"}`)}); err != nil {
		t.Fatalf("append as origin-b: %v", err)
	}
	if err := execOnErr(t, path, "UPDATE heads SET floor = 4 WHERE origin = 'origin-a'"); err != nil {
		t.Fatalf("seed a floor: %v", err)
	}

	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if state.Floors["origin-a"] != 4 {
		t.Errorf("floors = %v, want origin-a at 4", state.Floors)
	}
	if _, ok := state.Floors["origin-b"]; ok {
		t.Errorf("floors = %v, want origin-b absent: its floor is zero", state.Floors)
	}
	if state.Heads["origin-a"] != 2 || state.Entries["origin-a"] != 2 {
		t.Errorf("heads %v entries %v, want the two appended entries", state.Heads, state.Entries)
	}
	if state.Heads["origin-b"] != 1 {
		t.Errorf("heads = %v, want origin-b present at 1", state.Heads)
	}

	// Sizes are the segment objects' bytes, not an estimate of the bodies:
	// the sum of the stored objects' lengths, which heads.bytes accumulates
	// as each segment is published. The two must agree after two appends.
	var want int64
	for _, key := range []string{blobstore.SegmentKey("origin-a", 1), blobstore.SegmentKey("origin-a", 2)} {
		var buf bytes.Buffer
		if _, err := store.Get(context.Background(), key, &buf); err != nil {
			t.Fatalf("get segment %s: %v", key, err)
		}
		want += int64(buf.Len())
	}
	if state.Sizes["origin-a"] != want {
		t.Errorf("sizes = %v, want origin-a at %d: the bytes of its two segments", state.Sizes, want)
	}
	var headBytes int64
	if err := openAt(t, path).QueryRow("SELECT bytes FROM heads WHERE origin = 'origin-a'").Scan(&headBytes); err != nil {
		t.Fatalf("read heads bytes: %v", err)
	}
	if headBytes != want {
		t.Errorf("heads bytes = %d, want %d: the stored objects' lengths", headBytes, want)
	}
}

// TestReadMarksReportsOnlyLabelledOrigins pins the labels a reader takes beside
// the heads: each origin's stored label rides Labels, and an origin that named
// none is absent rather than carried as an empty name.
// Mutant: drop label from the heads scan.
func TestReadMarksReportsOnlyLabelledOrigins(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	labelled := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-a", store, func(s *Spec) { s.Label = "laptop" })
	if _, err := labelled.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append as origin-a: %v", err)
	}
	plain := backendOnStore(t, &localDriver{path: path}, "origin-b", store)
	if _, err := plain.Append([]Entry{upsertEntry("origin-b", "board", `["b"]`, `{"title":"two"}`)}); err != nil {
		t.Fatalf("append as origin-b: %v", err)
	}

	state, err := labelled.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	want := map[string]string{"origin-a": "laptop"}
	if !reflect.DeepEqual(state.Labels, want) {
		t.Errorf("labels = %v, want %v: only the origin that named one", state.Labels, want)
	}
}

// TestReadMarksCarriesTheLabelsUnderTheirWireName pins the JSON name the labels
// cross the pipe under: a read_marks reply carries them as labels, so a daemon
// decoding the reply finds each machine's name without pulling history.
// Mutant: rename the field's json tag.
func TestReadMarksCarriesTheLabelsUnderTheirWireName(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	b := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-a", store, func(s *Spec) { s.Label = "laptop" })
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append as origin-a: %v", err)
	}
	plain := backendOnStore(t, &localDriver{path: path}, "origin-b", store)
	if _, err := plain.Append([]Entry{upsertEntry("origin-b", "board", `["b"]`, `{"title":"two"}`)}); err != nil {
		t.Fatalf("append as origin-b: %v", err)
	}

	s := startWorker(t, b)
	s.askHello()
	req := Request{ID: "m", Verb: VerbReadMarks}
	line, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal read_marks: %v", err)
	}
	if _, err := s.p.requests.Write(append(line, '\n')); err != nil {
		t.Fatalf("write read_marks request: %v", err)
	}
	reply, err := readLine(s.p.replies)
	if err != nil {
		t.Fatalf("read read_marks reply: %v", err)
	}
	// The raw line, not the decoded reply: a round trip agrees with itself
	// under any tag, and the name is what the daemon reads.
	if !strings.Contains(string(reply), `"labels":{"origin-a":"laptop"}`) {
		t.Errorf("read_marks reply %s does not carry the labelled origin under labels", reply)
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("decode read_marks reply: %v", err)
	}
	if resp.ID != req.ID || !resp.OK {
		t.Fatalf("read_marks = %+v, want an ok reply to %q", resp, req.ID)
	}
}

// TestReadMarksAnswersNilMapsOnAnEmptyLog pins the nil/empty rule: with no heads
// rows the heads maps stay nil rather than empty, so an absent answer and a zero
// one stay apart on the wire. The signing fields are whatever the replica holds.
func TestReadMarksAnswersNilMapsOnAnEmptyLog(t *testing.T) {
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	want := MarkState{SignedFrom: state.SignedFrom, SignedTags: state.SignedTags, EpochCuts: state.EpochCuts}
	if !reflect.DeepEqual(state, want) {
		t.Errorf("ReadMarks on an empty log = %+v, want nil heads, entries, sizes, floors and labels", state)
	}
}

// TestReadMarksIgnoresAStaleFloorMetaRow pins that a floor row an older build
// recorded in meta plays no part: the floor is read from heads, so an origin
// whose heads floor is zero stays out of Floors even when a stale meta row names
// one. Mutant: read the meta floor prefix again -> the stale row reappears.
func TestReadMarksIgnoresAStaleFloorMetaRow(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := execOnErr(t, path, "INSERT INTO meta (key, value) VALUES ('floor.origin-a', '4')"); err != nil {
		t.Fatalf("seed a stale floor row: %v", err)
	}

	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if _, ok := state.Floors["origin-a"]; ok {
		t.Errorf("floors = %v, want origin-a absent: the stale meta row is not read", state.Floors)
	}
	if state.Heads["origin-a"] != 1 {
		t.Errorf("heads = %v, want origin-a at 1", state.Heads)
	}
}

// TestReadMarksRefusesAHeadsRowItCannotScan pins that a heads row whose column
// types do not match heads is reported rather than read as a zero value.
func TestReadMarksRefusesAHeadsRowItCannotScan(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path, "INSERT INTO heads (origin, seq, key, entries, bytes, at)"+
		" VALUES ('origin-b', 'not a number', 'origin-b/log/000000000001', 1, 1, '2026-10-08T09:30:00Z')")
	if _, err := b.ReadMarks(); err == nil {
		t.Error("ReadMarks read a heads row whose sequence is not a number")
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
// heads table is not there rather than answering from what it happens to hold,
// and that the schema class crosses the pipe. A remote that lost its meta loses
// the boundaries and cuts with it, which is reported rather than read as empty.
func TestReadMarksReportsAMissingTable(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	execOn(t, path, "DROP TABLE heads")

	_, err := b.ReadMarks()
	if err == nil {
		t.Fatal("a read against a remote with no heads table came back as a mark state")
	}
	if !contains(err.Error(), "heads") {
		t.Errorf("refusal %q does not name the table that is missing", err)
	}

	// The class the daemon latches on crosses read_marks. The local file
	// driver reports a missing table in SQLite's own words, which the
	// classifier leaves unmarked, so the engine-shaped refusal it does read
	// is driven here to pin the class a missing heads table carries.
	w := startWorker(t, refusingBackend{fail: markDriverRefusal(
		errors.New("sync engine: failed to execute sql: no such table: heads"))})
	w.askHello()
	resp := w.ask(Request{ID: "m", Verb: VerbReadMarks})
	if resp.OK {
		t.Fatalf("read_marks over a missing heads table = %+v, want a refusal", resp)
	}
	if resp.Code != CodeSchema {
		t.Errorf("read_marks code = %q, want %q", resp.Code, CodeSchema)
	}
	if !contains(resp.Error, "heads") {
		t.Errorf("read_marks refusal %q does not name the table that is missing", resp.Error)
	}

	// A fresh replica keeps the heads read intact, so losing meta is what
	// fails the read rather than the heads table dropped above.
	metaPath := tempReplica(t)
	metaBackend := backendFor(t, &localDriver{path: metaPath}, "origin-a")
	execOn(t, metaPath, "DROP TABLE meta")
	if _, err := metaBackend.ReadMarks(); err == nil {
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
