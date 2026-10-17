package synclog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

var handoverKey = []byte("0123456789abcdef0123456789abcdef")

func makeSignedEntry(t *testing.T, key []byte, origin, tbl, pk string, op Op, schema int, body map[string]any, after *Ref) Entry {
	t.Helper()
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var b json.RawMessage
	if body != nil {
		b = bodyFor(t, body)
	}
	e := Entry{
		Origin:        origin,
		Table:         tbl,
		PK:            pk,
		Op:            op,
		SchemaVersion: schema,
		Body:          b,
		At:            at,
		After:         after,
	}
	if len(key) > 0 {
		e.MAC = EntryMAC(key, e)
	}
	return e
}

func appendSignedBatch(t *testing.T, log *MemTransport, key []byte, entries ...Entry) []Entry {
	t.Helper()
	for i := range entries {
		if len(key) > 0 {
			entries[i].MAC = EntryMAC(key, entries[i])
		}
	}
	var b Boundary
	if len(key) > 0 && len(entries) > 0 {
		state, err := log.ReadMarks()
		if err != nil {
			t.Fatalf("ReadMarks: %v", err)
		}
		nextSeq := state.Heads[entries[0].Origin] + 1
		b = Boundary{Seq: nextSeq, Tag: BoundaryTag(key, entries[0].Origin, nextSeq)}
	}
	res, err := log.AppendSigned(entries, b)
	if err != nil {
		t.Fatalf("AppendSigned: %v", err)
	}
	return res
}

// TestHandoverInOrder covers hand-over applied in-order:
// m1 creates binding b1, hands over to m2, and m2's subsequent entries with After apply.
func TestHandoverInOrder(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// 1. m1 creates binding_record b1
	b1Body := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m1",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Body, nil))

	// 2. m1 writes a child entry
	f1Body := map[string]any{
		"record_id": "b1", "name": "f1", "round": 1, "body": []byte{1, 2},
		"bytes": 2, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f1"]`, OpUpsert, know, f1Body, nil))

	// 3. m1 hands over b1 to m2 (sets writer = m2)
	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	hBatch := appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Handover, nil))
	handoverSeq := hBatch[0].Seq

	// 4. m2 writes a child entry with After = (m1, handoverSeq)
	f2Body := map[string]any{
		"record_id": "b1", "name": "f2", "round": 1, "body": []byte{3, 4},
		"bytes": 2, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	after := &Ref{Origin: "m1", Seq: handoverSeq}
	appendSignedBatch(t, log.OnLog("m2"), handoverKey, makeSignedEntry(t, handoverKey, "m2", "round_file", `["b1","f2"]`, OpUpsert, know, f2Body, after))

	// Peer m3 imports
	imp := NewImporter(peer, log.OnLog("m3"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("import had holds/drops: %+v", res)
	}
	if res.Applied != 4 {
		t.Fatalf("Applied = %d, want 4", res.Applied)
	}

	// Verify writer on peer is m2
	writer := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`)
	if writer != "m2" {
		t.Fatalf("binding_record writer = %q, want m2", writer)
	}

	// Verify term recorded for m1 with upto = handoverSeq
	upto, _, _, found, err := peer.Term("b1", "m1")
	if err != nil || !found || upto != handoverSeq {
		t.Fatalf("peer.Term(b1, m1) = (%d, %t, %v), want (%d, true, nil)", upto, found, err, handoverSeq)
	}

	// Verify rows present
	have := sharedRows(t, peer)
	if !strings.Contains(have, `round_file ["b1","f1"]`) || !strings.Contains(have, `round_file ["b1","f2"]`) {
		t.Fatalf("peer sharedRows = %q, want both children", have)
	}
}

// TestHandoverOutOfOrderAcrossLogs covers new writer entries arriving before the hand-over:
// m2's entry has After = (m1, h). When m2 is evaluated while mark[m1] < h, m2 holds.
// Once m1 reaches h, fixpoint loop applies m1 and then m2.
func TestHandoverOutOfOrderAcrossLogs(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// 1. m1 creates binding b1 at seq 1
	b1Body := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m1",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Body, nil))

	// 2. m2 appends an entry waiting for m1 at seq 2
	f2Body := map[string]any{
		"record_id": "b1", "name": "f2", "round": 1, "body": []byte{3, 4},
		"bytes": 2, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	after := &Ref{Origin: "m1", Seq: 2}
	appendSignedBatch(t, log.OnLog("m2"), handoverKey, makeSignedEntry(t, handoverKey, "m2", "round_file", `["b1","f2"]`, OpUpsert, know, f2Body, after))

	// First import: m1 only has seq 1. m2 arrives but is held because mark[m1] == 0 < 2.
	// m1 will be applied (seq 1), but m2 should be held because seq 2 of m1 hasn't arrived.
	imp := NewImporter(peer, log.OnLog("m3"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(res.Held) != 1 || res.Held[0].Origin != "m2" {
		t.Fatalf("Held = %+v, want m2 held waiting for m1", res.Held)
	}
	if !strings.Contains(res.Held[0].Reason, "waiting for m1 at seq 2") {
		t.Fatalf("hold reason = %q, want waiting for m1 at seq 2", res.Held[0].Reason)
	}

	// 3. m1 appends the handover at seq 2
	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Handover, nil))

	// Second import: fixpoint loop imports m1 (reaching seq 2), then resolves m2!
	imp2 := NewImporter(peer, log.OnLog("m3"))
	imp2.SetKey(handoverKey)
	res2, err := imp2.Import()
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(res2.Held) != 0 {
		t.Fatalf("res2.Held = %+v, want no holds once m1 reached seq 2", res2.Held)
	}
	if res2.Applied != 2 { // m1's handover + m2's f2
		t.Fatalf("res2.Applied = %d, want 2", res2.Applied)
	}

	// Writer should be m2 and f2 present
	writer := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`)
	if writer != "m2" {
		t.Fatalf("binding_record writer = %q, want m2", writer)
	}
	if have := sharedRows(t, peer); !strings.Contains(have, `round_file ["b1","f2"]`) {
		t.Fatalf("peer rows = %q, want f2 present", have)
	}
}

// TestLateChild covers an honest late write from an old writer:
// An entry with Seq <= upto is accepted and applied even after the term was recorded.
func TestLateChild(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// Pre-seed peer with binding b1 (writer = m2) and term for m1 (upto = 2)
	seed(t, peerPath,
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin, writer)
		 VALUES ('b1', 'o1', 'b1', 'open', 1, '/x', '{}', 't', 't', 'm1', 'm2')`,
		`INSERT INTO sync_binding_term (root_id, writer, upto) VALUES ('b1', 'm1', 2)`,
	)

	// m1 appends a child entry at seq 1 (<= upto 2)
	fLateBody := map[string]any{
		"record_id": "b1", "name": "f_late", "round": 1, "body": []byte{9},
		"bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f_late"]`, OpUpsert, know, fLateBody, nil))

	imp := NewImporter(peer, log.OnLog("m3"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Applied != 1 || len(res.Dropped) != 0 {
		t.Fatalf("import: %+v, want 1 applied and 0 dropped", res)
	}
	if have := sharedRows(t, peer); !strings.Contains(have, `round_file ["b1","f_late"]`) {
		t.Fatalf("peer rows = %q, want late child applied", have)
	}
}

// TestPostTermRefusal covers an entry from the old writer with Seq > upto:
// It is refused and dropped with reason naming "writer moved".
func TestPostTermRefusal(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// m1 creates binding b1 at seq 1
	b1Body := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m1",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Body, nil))

	// m1 hands over b1 to m2 at seq 2 (upto = 2)
	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Handover, nil))

	// Import m1 through seq 2: term recorded with upto = 2
	imp := NewImporter(peer, log.OnLog("m3"))
	imp.SetKey(handoverKey)
	if _, err := imp.Import(); err != nil {
		t.Fatalf("initial import: %v", err)
	}

	// Now m1 appends a child entry at seq 3 > upto (2)
	postTermBody := map[string]any{
		"record_id": "b1", "name": "f_invalid", "round": 1, "body": []byte{9},
		"bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f_invalid"]`, OpUpsert, know, postTermBody, nil))

	// Import seq 3: must be dropped with "writer moved"
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("post-term import: %v", err)
	}
	if len(res.Dropped) != 1 {
		t.Fatalf("res.Dropped = %+v, want 1 dropped entry", res.Dropped)
	}
	if !strings.Contains(res.Dropped[0].Reason, "writer moved") {
		t.Fatalf("drop reason = %q, want containing 'writer moved'", res.Dropped[0].Reason)
	}
	// Row was not applied
	if have := sharedRows(t, peer); strings.Contains(have, `round_file ["b1","f_invalid"]`) {
		t.Fatalf("peer holds f_invalid, want dropped: %q", have)
	}
}

// TestSnapshotFirstJoiner covers a joiner importing a snapshot of an old writer's log:
// The snapshot carries the hand-over root entry (writer = m2) and older children.
// The joiner records the term from the root entry and applies the older children.
func TestSnapshotFirstJoiner(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// m1 creates b1 and child f1, then hands over to m2
	b1Body := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m1",
	}
	f1Body := map[string]any{
		"record_id": "b1", "name": "f1", "round": 1, "body": []byte{1},
		"bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	appendSignedBatch(t, log, handoverKey,
		makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Body, nil),
		makeSignedEntry(t, handoverKey, "m1", "round_file", `["b1","f1"]`, OpUpsert, know, f1Body, nil),
		makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Handover, nil),
	)

	// Compact m1's log to floor 3 (all entries in snapshot)
	if _, err := log.Compact("m1", 3); err != nil {
		t.Fatalf("compact: %v", err)
	}

	// Joiner m3 imports m1 via snapshot path
	imp := NewImporter(peer, log.OnLog("m3"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("snapshot import: %v", err)
	}
	if len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("snapshot import held/dropped: %+v", res)
	}
	if res.Snapshots != 1 {
		t.Fatalf("Snapshots = %d, want 1", res.Snapshots)
	}

	// Verify writer is m2
	writer := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`)
	if writer != "m2" {
		t.Fatalf("binding_record writer = %q, want m2", writer)
	}

	// Verify child f1 was applied
	have := sharedRows(t, peer)
	if !strings.Contains(have, `round_file ["b1","f1"]`) {
		t.Fatalf("peer rows = %q, want f1 applied from snapshot", have)
	}

	// Verify term recorded for m1
	upto, _, _, found, err := peer.Term("b1", "m1")
	if err != nil || !found || upto < 1 {
		t.Fatalf("peer.Term(b1, m1) = (%d, %t, %v), want valid term", upto, found, err)
	}
}

// TestTwoChanges covers sequential ownership transfers (A to B to C):
// A hands over to B (upto h1), B writes with After=(A, h1) and hands over to C (upto h2),
// and C writes with After=(B, h2).
func TestTwoChanges(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "joiner")
	know := knownVersion(t, peer)
	log := NewMemTransport("A")

	// 1. A creates b1
	b1A := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "A", "writer": "A",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "A", "binding_record", `["b1"]`, OpUpsert, know, b1A, nil))

	// 2. A hands over to B
	b1ToB := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "A", "writer": "B",
	}
	h1Batch := appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "A", "binding_record", `["b1"]`, OpUpsert, know, b1ToB, nil))
	h1Seq := h1Batch[0].Seq

	// 3. B writes child fB with After=(A, h1Seq)
	fBBody := map[string]any{
		"record_id": "b1", "name": "fB", "round": 1, "body": []byte{2},
		"bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	appendSignedBatch(t, log.OnLog("B"), handoverKey, makeSignedEntry(t, handoverKey, "B", "round_file", `["b1","fB"]`, OpUpsert, know, fBBody, &Ref{Origin: "A", Seq: h1Seq}))

	// 4. B hands over to C with After=(A, h1Seq)
	b1ToC := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "A", "writer": "C",
	}
	h2Batch := appendSignedBatch(t, log.OnLog("B"), handoverKey, makeSignedEntry(t, handoverKey, "B", "binding_record", `["b1"]`, OpUpsert, know, b1ToC, &Ref{Origin: "A", Seq: h1Seq}))
	h2Seq := h2Batch[0].Seq

	// 5. C writes child fC with After=(B, h2Seq)
	fCBody := map[string]any{
		"record_id": "b1", "name": "fC", "round": 1, "body": []byte{3},
		"bytes": 1, "sha256": "s", "mtime": "t", "sealed_at": "t",
	}
	appendSignedBatch(t, log.OnLog("C"), handoverKey, makeSignedEntry(t, handoverKey, "C", "round_file", `["b1","fC"]`, OpUpsert, know, fCBody, &Ref{Origin: "B", Seq: h2Seq}))

	// Joiner imports all logs
	imp := NewImporter(peer, log.OnLog("joiner"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("held/dropped: %+v", res)
	}

	// Final writer must be C
	writer := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`)
	if writer != "C" {
		t.Fatalf("final writer = %q, want C", writer)
	}

	// Both terms A and B must exist
	uptoA, _, _, foundA, _ := peer.Term("b1", "A")
	if !foundA || uptoA != h1Seq {
		t.Fatalf("term A = (%d, %t), want (%d, true)", uptoA, foundA, h1Seq)
	}
	uptoB, _, _, foundB, _ := peer.Term("b1", "B")
	if !foundB || uptoB != h2Seq {
		t.Fatalf("term B = (%d, %t), want (%d, true)", uptoB, foundB, h2Seq)
	}

	// Both children fB and fC present
	have := sharedRows(t, peer)
	if !strings.Contains(have, `round_file ["b1","fB"]`) || !strings.Contains(have, `round_file ["b1","fC"]`) {
		t.Fatalf("sharedRows = %q, want fB and fC", have)
	}
}

// TestKeylessHold covers Rule 6:
// An importer with no key holds the origin for hand-over or After-bearing entries.
func TestKeylessHold(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// Handover entry
	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Handover, nil))

	// Keyless importer (SetKey not called)
	imp := NewImporter(peer, log.OnLog("m2"))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Held) != 1 || res.Held[0].Origin != "m1" {
		t.Fatalf("Held = %+v, want m1 held", res.Held)
	}
	if !strings.Contains(res.Held[0].Reason, "requires a key") {
		t.Fatalf("hold reason = %q, want naming key required", res.Held[0].Reason)
	}
	if res.Applied != 0 {
		t.Fatalf("Applied = %d, want 0", res.Applied)
	}
	// Mark must not advance
	mark, found, err := peer.ImportMark("m1")
	if err != nil || found || mark != 0 {
		t.Fatalf("ImportMark = (%d, %t, %v), want not advanced", mark, found, err)
	}
}

// TestUniqueCollisionHold covers a hand-over colliding with a local live binding:
// Landing a hand-over where target already has a live binding_record with same (owner, name)
// triggers HoldError: "name taken: unbind or rename the local binding, then the hand-over applies".
func TestUniqueCollisionHold(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// Pre-seed peer m2 with a local live binding with name "proj1" and owner "o1"
	seed(t, peerPath,
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin, writer)
		 VALUES ('b_local', 'o1', 'proj1', 'open', 1, '/local', '{}', 't', 't', 'm2', 'm2')`,
	)

	// Remote m1 hands over binding b_remote to m2 with same owner "o1" and name "proj1"
	bRemoteHandover := map[string]any{
		"id": "b_remote", "owner": "o1", "name": "proj1", "state": "open", "round": 1,
		"cwd": "/remote", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b_remote"]`, OpUpsert, know, bRemoteHandover, nil))

	// Peer m2 imports
	imp := NewImporter(peer, log.OnLog("m2"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Held) != 1 || res.Held[0].Origin != "m1" {
		t.Fatalf("Held = %+v, want m1 held on unique collision", res.Held)
	}
	wantReason := "name taken: unbind or rename the local binding, then the hand-over applies"
	if res.Held[0].Reason != wantReason {
		t.Fatalf("hold reason = %q, want %q", res.Held[0].Reason, wantReason)
	}
	if res.Applied != 0 {
		t.Fatalf("Applied = %d, want 0", res.Applied)
	}
	// Local binding is untouched
	localName := columnOf(t, peer, `SELECT name FROM binding_record WHERE id = 'b_local'`)
	if localName != "proj1" {
		t.Fatalf("b_local name = %q, want proj1", localName)
	}
}

// TestHandoverSchemaAboveKnownHeldBatch pins that a hand-over entry with SchemaVersion
// above known is held and never applied in the standard batch import path.
func TestHandoverSchemaAboveKnownHeldBatch(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	futureSchema := 99
	appendSignedBatch(t, log, handoverKey,
		makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, futureSchema, b1Handover, nil),
	)

	imp := NewImporter(peer, log.OnLog("m2"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Held) != 1 || res.Held[0].Origin != "m1" {
		t.Fatalf("Held = %+v, want m1 held", res.Held)
	}
	if res.Held[0].Schema != futureSchema {
		t.Errorf("Held schema = %d, want %d", res.Held[0].Schema, futureSchema)
	}
	if res.Applied != 0 {
		t.Errorf("Applied = %d, want 0", res.Applied)
	}

	// Verify row was never applied
	have := sharedRows(t, peer)
	if strings.Contains(have, "b1") {
		t.Fatalf("binding_record was applied despite future schema: %s", have)
	}
	if mark, found, err := peer.ImportMark("m1"); err != nil || (found && mark != 0) {
		t.Errorf("import mark = %d (found=%v, err=%v), want 0", mark, found, err)
	}
}

// TestHandoverSchemaAboveKnownHeldSnapshot pins that a hand-over entry with SchemaVersion
// above known is held and never applied in the snapshot import path.
func TestHandoverSchemaAboveKnownHeldSnapshot(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	futureSchema := 99
	appendSignedBatch(t, log, handoverKey,
		makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, futureSchema, b1Handover, nil),
	)

	if _, err := log.Compact("m1", 1); err != nil {
		t.Fatalf("compact: %v", err)
	}

	imp := NewImporter(peer, log.OnLog("m2"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Held) != 1 || res.Held[0].Origin != "m1" {
		t.Fatalf("Held = %+v, want m1 held", res.Held)
	}
	if res.Held[0].Schema != futureSchema {
		t.Errorf("Held schema = %d, want %d", res.Held[0].Schema, futureSchema)
	}
	if res.Applied != 0 {
		t.Errorf("Applied = %d, want 0", res.Applied)
	}

	// Verify row was never applied
	have := sharedRows(t, peer)
	if strings.Contains(have, "b1") {
		t.Fatalf("binding_record was applied despite future schema: %s", have)
	}
	if mark, found, err := peer.ImportMark("m1"); err != nil || (found && mark != 0) {
		t.Errorf("import mark = %d (found=%v, err=%v), want 0", mark, found, err)
	}
}

// TestHandoverRefusedWithoutVerifiedMAC pins that a hand-over with an unverified MAC
// is refused and stalls the origin.
func TestHandoverRefusedWithoutVerifiedMAC(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	entry := makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Handover, nil)
	appendSignedBatch(t, log, handoverKey, entry)
	log.log.mu.Lock()
	log.log.entries[0].MAC = []byte("corrupted-mac-value")
	log.log.mu.Unlock()

	imp := NewImporter(peer, log.OnLog("m2"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Stalled) == 0 {
		t.Fatal("expected stall on unverified MAC for hand-over, got none")
	}
	if res.Applied != 0 {
		t.Errorf("Applied = %d, want 0", res.Applied)
	}
}

// TestForgedCreatorClaimRefused pins that a root upsert whose body origin differs
// from the entry origin without carrying After is refused as a forged creator claim.
func TestForgedCreatorClaimRefused(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// Absent root: entry origin is m1, but body origin is "forged-creator", no After.
	b1Forged := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "forged-creator", "writer": "m1",
	}
	entry := makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Forged, nil)
	appendSignedBatch(t, log, handoverKey, entry)

	imp := NewImporter(peer, log.OnLog("m2"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Dropped) != 1 {
		t.Fatalf("Dropped = %v, want 1 dropped entry for forged creator claim", res.Dropped)
	}
	if res.Applied != 0 {
		t.Errorf("Applied = %d, want 0", res.Applied)
	}
	if rows := sharedRows(t, peer); strings.Contains(rows, `binding_record ["b1"]`) {
		t.Fatalf("peer holds forged row: %s", rows)
	}
}

func targetOutboxCount(t *testing.T, d *db.DB) int {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var count int
	if err := raw.QueryRow(`SELECT count(*) FROM sync_outbox WHERE origin = ? OR origin = '' OR origin IS NULL`, d.Origin()).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	return count
}

// TestHandoverLandingOnTargetQueuesNothing pins C2: a hand-over landing on the target
// (both first sight and existing row) queues nothing in the target's outbox.
func TestHandoverLandingOnTargetQueuesNothing(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// First sight: peer has no row for b1. Hand-over arrives from m1.
	b1Handover := map[string]any{
		"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
		"origin": "m1", "writer": "m2",
	}
	e1 := makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b1"]`, OpUpsert, know, b1Handover, nil)
	appendSignedBatch(t, log, handoverKey, e1)

	imp := NewImporter(peer, log.OnLog("m2"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("first sight import: %v", err)
	}
	if res.Applied != 1 {
		t.Fatalf("first sight applied = %d, want 1", res.Applied)
	}
	if count := targetOutboxCount(t, peer); count != 0 {
		t.Fatalf("target outbox count after first sight = %d, want 0", count)
	}

	// Existing row: peer has b2 originally written by m1, now with writer = 'm2'.
	peer2, path2 := peerFile(t, "m2")
	know2 := knownVersion(t, peer2)
	seed(t, path2,
		`INSERT INTO binding_record (id, origin, writer, owner, name, state, round, cwd, record_json, created_at, updated_at)
		 VALUES ('b2', 'm1', 'm2', 'o1', 'b2', 'open', 1, '/x', '{}', 't', 't')`,
		`INSERT INTO sync_binding_term (root_id, writer, upto) VALUES ('b2', 'm1', 10)`,
		`DELETE FROM sync_outbox`,
	)

	log2 := NewMemTransport("m1")
	b2Update := map[string]any{
		"id": "b2", "owner": "o1", "name": "b2", "state": "active", "round": 1,
		"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t2",
		"origin": "m1", "writer": "m2",
	}
	e2 := makeSignedEntry(t, handoverKey, "m1", "binding_record", `["b2"]`, OpUpsert, know2, b2Update, nil)
	appendSignedBatch(t, log2, handoverKey, e2)

	imp2 := NewImporter(peer2, log2.OnLog("m2"))
	imp2.SetKey(handoverKey)
	res2, err := imp2.Import()
	if err != nil {
		t.Fatalf("existing row import: %v", err)
	}
	if res2.Applied != 1 {
		t.Fatalf("existing row applied = %d, want 1", res2.Applied)
	}
	if count := targetOutboxCount(t, peer2); count != 0 {
		t.Fatalf("target outbox count after existing row = %d, want 0", count)
	}
}

