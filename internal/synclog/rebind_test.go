package synclog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func insertBindingRecord(id, origin, owner, name string) string {
	return fmt.Sprintf(`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin)
		VALUES ('%s', '%s', '%s', 'open', 1, '/z', '{}', 't', 't', '%s')`, id, owner, name, origin)
}

// Two-database rebind: machine A deletes a binding_record and inserts one with
// the same (origin, owner, name) plus a binding, chain_member and round of a new
// chain in one drain window, exports; machine B imports. B holds the new record,
// binding, member and round, and no old record; Dropped empty; state converged.
func TestTwoDatabaseRebind(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	writer := &recording{MemTransport: log}

	seed(t, theirPath,
		insertBindingRecord("rec1", "m1", "o", "rebind-test"),
	)
	got := importFrom(t, peer, log, theirs, writer)
	if got.Applied != 1 || len(got.Dropped) != 0 {
		t.Fatalf("initial import: %+v, want 1 applied and 0 dropped", got)
	}

	// In one drain window on machine A:
	// Delete rec1, and insert rec2 with same (origin, owner, name), plus a binding,
	// chain, chain_member, and round.
	seed(t, theirPath,
		`DELETE FROM binding_record WHERE id = 'rec1'`,
		insertBindingRecord("rec2", "m1", "o", "rebind-test"),
		insertBinding("b2", "m1"),
		`INSERT INTO chains (id, origin, owner, name, status, phase, step, plan, plans, plan_paths, builder, created_at, updated_at)
			VALUES ('c2', 'm1', 'o', 'chain2', 'open', 'p', 's', 1, 1, '[]', 'b', 't', 't')`,
		`INSERT INTO chain_member (chain_id, binding, actor, seq) VALUES ('c2', 'b2', 'a', 1)`,
		insertRound("rnd2", "b2"),
	)

	rebindRes := importFrom(t, peer, log, theirs, writer)
	if len(rebindRes.Dropped) != 0 {
		t.Fatalf("rebind import dropped = %+v, want none", rebindRes.Dropped)
	}

	// Machine B holds rec2 and does not hold rec1.
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding_record WHERE id = 'rec1'`); count != "0" {
		t.Fatalf("peer still holds old record rec1, want it deleted")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding_record WHERE id = 'rec2'`); count != "1" {
		t.Fatalf("peer does not hold new record rec2, want it applied")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding WHERE id = 'b2'`); count != "1" {
		t.Fatalf("peer does not hold binding b2, want it applied")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM round WHERE id = 'rnd2'`); count != "1" {
		t.Fatalf("peer does not hold round rnd2, want it applied")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM chains WHERE id = 'c2'`); count != "1" {
		t.Fatalf("peer does not hold chain c2, want it applied")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM chain_member WHERE chain_id = 'c2'`); count != "1" {
		t.Fatalf("peer does not hold chain_member for c2, want it applied")
	}

	assertReferencesClean(t, theirs)
	assertReferencesClean(t, peer)
}

// Old-order batch: append by hand a batch with the upsert before the delete
// (v0.18.0 order) from a remote origin; the fixed importer applies both, Dropped empty.
func TestOldOrderBatch(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m2")
	seed(t, peerPath,
		insertBindingRecord("rec1", "m1", "o", "old-order-name"),
	)

	body := bodyFor(t, map[string]any{
		"id": "rec2", "owner": "o", "name": "old-order-name", "state": "open",
		"round": 1, "cwd": "/z", "record_json": "{}", "created_at": "t", "updated_at": "t", "origin": "m1",
	})

	// v0.18.0 order: upsert rec2 (collides with existing rec1), then delete rec1.
	batch := []Entry{
		{
			Origin: "m1", Batch: 1, Seq: 1, Table: "binding_record", PK: `["rec2"]`,
			Op: OpUpsert, SchemaVersion: 1, Body: body,
		},
		{
			Origin: "m1", Batch: 1, Seq: 2, Table: "binding_record", PK: `["rec1"]`,
			Op: OpDelete, SchemaVersion: 1,
		},
	}

	log := NewMemTransport("m1")
	if _, err := log.Append(batch); err != nil {
		t.Fatalf("append batch: %v", err)
	}

	imp := NewImporter(peer, log.OnLog("m2"))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import old-order batch: %v", err)
	}
	if len(res.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want empty", res.Dropped)
	}
	if res.Applied != 2 {
		t.Fatalf("Applied = %d, want 2", res.Applied)
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding_record WHERE id = 'rec1'`); count != "0" {
		t.Fatalf("rec1 still exists, want it deleted")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding_record WHERE id = 'rec2'`); count != "1" {
		t.Fatalf("rec2 not found, want it applied")
	}
	assertReferencesClean(t, peer)
}

func cascadeBatches(t *testing.T) ([]Entry, []Entry) {
	t.Helper()
	rec2Body := bodyFor(t, map[string]any{
		"id": "rec2", "owner": "o", "name": "casc-name", "state": "open",
		"round": 1, "cwd": "/z", "record_json": "{}", "created_at": "t", "updated_at": "t", "origin": "m1",
	})
	parentBody := bodyFor(t, map[string]any{
		"id": "p1", "name": "p1", "cwd": "/cwd", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})
	foreignBody := bodyFor(t, map[string]any{
		"id": "peer-b", "name": "stolen", "cwd": "/stolen", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})
	roundBody := bodyFor(t, map[string]any{
		"id": "rnd1", "binding_id": "p1", "number": 1, "started_at": "t",
		"outcome": "done", "switches": 0,
	})
	b1 := []Entry{
		{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["p1"]`, Op: OpUpsert, SchemaVersion: 1, Body: parentBody},
		{Origin: "m1", Batch: 1, Seq: 2, Table: "binding", PK: `["peer-b"]`, Op: OpUpsert, SchemaVersion: 1, Body: foreignBody},
		{Origin: "m1", Batch: 1, Seq: 3, Table: "binding_record", PK: `["rec2"]`, Op: OpUpsert, SchemaVersion: 1, Body: rec2Body},
		{Origin: "m1", Batch: 1, Seq: 4, Table: "binding_record", PK: `["rec1"]`, Op: OpDelete, SchemaVersion: 1},
	}
	b2 := []Entry{
		{Origin: "m1", Batch: 5, Seq: 5, Table: "round", PK: `["rnd1"]`, Op: OpUpsert, SchemaVersion: 1, Body: roundBody},
	}
	return b1, b2
}

// Cascade: batch 1 holds a parent binding, an unrelated entry claiming a row
// another origin owns, and the old-order rebind; batch 2 holds the child round.
// After import the parent and child exist, only the foreign-owner entry is in
// Dropped with its own sequence.
func TestCascadeBatchAndLegacyReplay(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m2")
	seed(t, peerPath,
		insertBindingRecord("rec1", "m1", "o", "casc-name"),
		insertBinding("peer-b", "m2"),
	)
	batch1, batch2 := cascadeBatches(t)

	log := NewMemTransport("m1")
	if _, err := log.Append(batch1); err != nil {
		t.Fatalf("append batch1: %v", err)
	}
	if _, err := log.Append(batch2); err != nil {
		t.Fatalf("append batch2: %v", err)
	}

	imp := NewImporter(peer, log.OnLog("m2"))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if len(res.Dropped) != 1 {
		t.Fatalf("Dropped = %+v, want exactly 1 drop for the foreign row", res.Dropped)
	}
	if res.Dropped[0].Seq != 2 || res.Dropped[0].Origin != "m1" {
		t.Fatalf("Dropped[0] = %+v, want seq 2 from m1", res.Dropped[0])
	}

	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding WHERE id = 'p1'`); count != "1" {
		t.Fatalf("parent binding p1 not found, want it applied")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM round WHERE id = 'rnd1'`); count != "1" {
		t.Fatalf("child round rnd1 not found, want it applied")
	}
	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding_record WHERE id = 'rec2'`); count != "1" {
		t.Fatalf("rec2 not found, want it applied")
	}
	assertReferencesClean(t, peer)
}

// Swap: two live binding_record rows of one origin exchange names in one window;
// both entries refused individually, the remaining entries of the batch applied,
// Dropped has two lines naming the rows, mark at the tail.
func TestSwapUniqueEntries(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m2")
	seed(t, peerPath,
		insertBindingRecord("rA", "m1", "o", "alpha"),
		insertBindingRecord("rB", "m1", "o", "beta"),
	)

	b1Body := bodyFor(t, map[string]any{
		"id": "b1", "name": "b1", "cwd": "/1", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})
	b2Body := bodyFor(t, map[string]any{
		"id": "b2", "name": "b2", "cwd": "/2", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})

	// Swap names: rA takes beta, rB takes alpha.
	rABody := bodyFor(t, map[string]any{
		"id": "rA", "owner": "o", "name": "beta", "state": "open",
		"round": 1, "cwd": "/z", "record_json": "{}", "created_at": "t", "updated_at": "t", "origin": "m1",
	})
	rBBody := bodyFor(t, map[string]any{
		"id": "rB", "owner": "o", "name": "alpha", "state": "open",
		"round": 1, "cwd": "/z", "record_json": "{}", "created_at": "t", "updated_at": "t", "origin": "m1",
	})

	batch := []Entry{
		{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["b1"]`, Op: OpUpsert, SchemaVersion: 1, Body: b1Body},
		{Origin: "m1", Batch: 1, Seq: 2, Table: "binding_record", PK: `["rA"]`, Op: OpUpsert, SchemaVersion: 1, Body: rABody},
		{Origin: "m1", Batch: 1, Seq: 3, Table: "binding_record", PK: `["rB"]`, Op: OpUpsert, SchemaVersion: 1, Body: rBBody},
		{Origin: "m1", Batch: 1, Seq: 4, Table: "binding", PK: `["b2"]`, Op: OpUpsert, SchemaVersion: 1, Body: b2Body},
	}

	log := NewMemTransport("m1")
	if _, err := log.Append(batch); err != nil {
		t.Fatalf("append batch: %v", err)
	}

	imp := NewImporter(peer, log.OnLog("m2"))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if res.Applied != 2 {
		t.Fatalf("res.Applied = %d, want 2 (b1 and b2)", res.Applied)
	}
	if len(res.Dropped) != 2 {
		t.Fatalf("len(res.Dropped) = %d, want 2 for swapped rows", len(res.Dropped))
	}
	for _, d := range res.Dropped {
		if !strings.Contains(d.Reason, "binding_record") {
			t.Fatalf("drop %v does not name binding_record", d)
		}
	}

	mark, found, err := peer.ImportMark("m1")
	if err != nil || !found || mark != 4 {
		t.Fatalf("mark = (%d, %t, %v), want tail at 4", mark, found, err)
	}

	if nameA := columnOf(t, peer, `SELECT name FROM binding_record WHERE id = 'rA'`); nameA != "alpha" {
		t.Fatalf("rA name = %q, want pre-swap 'alpha'", nameA)
	}
	if nameB := columnOf(t, peer, `SELECT name FROM binding_record WHERE id = 'rB'`); nameB != "beta" {
		t.Fatalf("rB name = %q, want pre-swap 'beta'", nameB)
	}
	assertReferencesClean(t, peer)
}

// Re-point-then-delete-parent: parent delete deferred and applied after the child upsert
// on the fixed importer.
func TestRepointThenDeleteParent(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m2")
	seed(t, peerPath,
		insertBinding("p_old", "m1"),
		insertBinding("p_new", "m1"),
		insertRound("rnd1", "p_old"),
	)

	rndBody := bodyFor(t, map[string]any{
		"id": "rnd1", "binding_id": "p_new", "number": 1, "started_at": "t",
		"outcome": "done", "switches": 0,
	})

	// Deletes first order: delete p_old, then upsert rnd1 pointing to p_new.
	batch := []Entry{
		{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["p_old"]`, Op: OpDelete, SchemaVersion: 1},
		{Origin: "m1", Batch: 1, Seq: 2, Table: "round", PK: `["rnd1"]`, Op: OpUpsert, SchemaVersion: 1, Body: rndBody},
	}

	log := NewMemTransport("m1")
	if _, err := log.Append(batch); err != nil {
		t.Fatalf("append: %v", err)
	}

	imp := NewImporter(peer, log.OnLog("m2"))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want empty", res.Dropped)
	}
	if res.Applied != 2 {
		t.Fatalf("Applied = %d, want 2", res.Applied)
	}

	if count := columnOf(t, peer, `SELECT COUNT(*) FROM binding WHERE id = 'p_old'`); count != "0" {
		t.Fatalf("p_old still exists, want it deleted")
	}
	if parent := columnOf(t, peer, `SELECT binding_id FROM round WHERE id = 'rnd1'`); parent != "p_new" {
		t.Fatalf("rnd1 binding_id = %q, want p_new", parent)
	}
	assertReferencesClean(t, peer)
}

// Ownership: an entry from origin X naming a row of origin Y is still refused,
// and a UNIQUE conflict with a row of another origin is never resolved by deleting it.
func TestOwnershipEntryRefused(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m2")
	seed(t, peerPath,
		insertBinding("by", "m2"),
	)

	body := bodyFor(t, map[string]any{
		"id": "by", "name": "stolen", "cwd": "/bad", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})
	validBody := bodyFor(t, map[string]any{
		"id": "bx", "name": "valid", "cwd": "/good", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})

	batch := []Entry{
		{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["by"]`, Op: OpUpsert, SchemaVersion: 1, Body: body},
		{Origin: "m1", Batch: 1, Seq: 2, Table: "binding", PK: `["bx"]`, Op: OpUpsert, SchemaVersion: 1, Body: validBody},
	}

	log := NewMemTransport("m1")
	if _, err := log.Append(batch); err != nil {
		t.Fatalf("append: %v", err)
	}

	imp := NewImporter(peer, log.OnLog("m2"))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Dropped) != 1 || res.Dropped[0].Seq != 1 {
		t.Fatalf("Dropped = %+v, want entry 1 dropped", res.Dropped)
	}
	if res.Applied != 1 {
		t.Fatalf("Applied = %d, want 1", res.Applied)
	}

	if owner := columnOf(t, peer, `SELECT origin FROM binding WHERE id = 'by'`); owner != "m2" {
		t.Fatalf("by origin = %q, want m2 untouched", owner)
	}
	if cwd := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = 'by'`); cwd == "/bad" {
		t.Fatalf("by was rewritten by unauthorized origin")
	}
	assertReferencesClean(t, peer)
}

func signingEntries(t *testing.T, key []byte) (Entry, Entry, Entry) {
	t.Helper()
	makeBody := func(id, name, cwd string) json.RawMessage {
		return bodyFor(t, map[string]any{
			"id": id, "name": name, "cwd": cwd, "builder_mode": "local",
			"created_at": "t", "ingest_source": "manual", "origin": "m1",
		})
	}
	e1 := Entry{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["b1"]`, Op: OpUpsert, SchemaVersion: 1, Body: makeBody("b1", "b1", "/1")}
	e2 := Entry{Origin: "m1", Batch: 1, Seq: 2, Table: "binding", PK: `["b2"]`, Op: OpUpsert, SchemaVersion: 1, Body: makeBody("b2", "b2", "/2")}
	e3 := Entry{Origin: "m1", Batch: 3, Seq: 3, Table: "binding", PK: `["b3"]`, Op: OpUpsert, SchemaVersion: 1, Body: makeBody("b3", "b3", "/3")}
	e3.MAC = EntryMAC(key, e3)
	return e1, e2, e3
}

// Replayed entries pass verification. Replay unsigned entries an origin wrote
// before it first signed, after that origin has started signing, proving they apply
// rather than being refused or holding the origin.
func TestReplayUnsignedEntriesAfterSigning(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	key := []byte("01234567890123456789012345678901")
	e1, e2, e3 := signingEntries(t, key)

	log := NewMemTransport("m1")
	if _, err := log.Append([]Entry{e1, e2}); err != nil {
		t.Fatalf("append unsigned: %v", err)
	}

	imp := NewImporter(peer, log.OnLog("m2"))
	imp.SetKey(key)

	res, err := imp.Import()
	if err != nil || res.Applied != 2 || len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("initial import: %+v, %v", res, err)
	}

	// Now append signed batch and publish boundary.
	if _, err := log.AppendSigned([]Entry{e3}, Boundary{Seq: 3, Tag: BoundaryTag(key, "m1", 3)}); err != nil {
		t.Fatalf("append signed: %v", err)
	}

	res, err = imp.Import()
	if err != nil || res.Applied != 1 || len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("signed import: %+v, %v", res, err)
	}

	// Rewind mark for m1 to 0 (simulating replay).
	if err := peer.RewindImportMark("m1", 0); err != nil {
		t.Fatalf("rewind: %v", err)
	}

	// Replay import: pulls all entries from 0.
	replayImp := NewImporter(peer, log.OnLog("m2"))
	replayImp.SetKey(key)
	res, err = replayImp.Import()
	if err != nil || len(res.Held) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("replay import: %+v, %v", res, err)
	}

	mark, _, err := peer.ImportMark("m1")
	if err != nil || mark != 3 {
		t.Fatalf("mark after replay = %d, want 3", mark)
	}
	assertReferencesClean(t, peer)
}
