package synclog

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Who an entry is allowed to write. An entry names an origin and a row, and the
// only thing that makes the pair mean anything is that the origin is the owner of
// the row. That is a question about this machine's own file rather than about the
// log, so the importer answers it here: the transport only knows what an entry
// claims, and a client that does not honour the rule would otherwise be able to
// remove another installation's rows and take its rows over by writing them again.

// victim is the reader's file holding one binding of its own and one of a third
// machine's, so a case has a row each side of the reader to name.
func victim(t *testing.T) (*db.DB, string) {
	t.Helper()
	peer, path := peerFile(t, "m2")
	seed(t, path,
		insertBinding("01A", "m2"),
		insertBinding("01B", "m3"),
	)
	return peer, path
}

// A delete naming a row this machine resolves to another installation is refused
// and the row stays. The deletion would otherwise be permanent on every peer at
// once: the victim's own file records the removal, and its reconcile then
// propagates the delete under the victim's own origin.
func TestImporterRefusesACrossOriginDelete(t *testing.T) {
	t.Parallel()
	peer, _ := victim(t)
	log := NewMemTransport("m1")
	entryFor(t, log, Entry{
		Origin: "m1", Table: "binding", PK: `["01A"]`, Op: OpDelete,
		SchemaVersion: knownVersion(t, peer),
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a delete for a row m2 owns: %v", err)
	}
	if got.Applied != 0 {
		t.Fatalf("import = %+v, want the cross-origin delete not applied", got)
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Origin != "m1" {
		t.Fatalf("Dropped = %+v, want m1's batch dropped", got.Dropped)
	}
	if have := sharedRows(t, peer); have != `binding ["01A"]|binding ["01B"]` {
		t.Fatalf("the peer holds %q, want both bindings still standing", have)
	}
	// No mark moves for a batch that named somebody else's row: the sequence
	// behind a forged claim is not progress, and a mark over it would hide every
	// genuine entry of that origin behind it.
	if seq, found, err := peer.ImportMark("m1"); err != nil || found {
		t.Fatalf("ImportMark = (%d, %t, %v), want no mark for a forged claim", seq, found, err)
	}
}

// An upsert naming a row this machine resolves to another installation is
// refused, and the row's owner is left where it was. A body that names the owner
// column is how the theft would have been carried: the row would stop resolving
// to its own machine, which then neither exports it nor reconciles it, so nothing
// would heal it.
func TestImporterRefusesACrossOriginUpsert(t *testing.T) {
	t.Parallel()
	peer, _ := victim(t)
	log := NewMemTransport("m1")
	entryFor(t, log, Entry{
		Origin: "m1", Table: "binding", PK: `["01A"]`, Op: OpUpsert,
		SchemaVersion: knownVersion(t, peer),
		Body: bodyFor(t, map[string]any{
			"id": "01A", "name": "01A", "cwd": "/stolen",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
		}),
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import an upsert for a row m2 owns: %v", err)
	}
	if got.Applied != 0 || len(got.Dropped) != 1 {
		t.Fatalf("import = %+v, want the cross-origin upsert dropped", got)
	}
	if cwd := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = '01A'`); cwd != "/x" {
		t.Fatalf("the binding's cwd = %q, want the value its own owner wrote", cwd)
	}
	if origin := columnOf(t, peer, `SELECT origin FROM binding WHERE id = '01A'`); origin != "m2" {
		t.Fatalf("the binding's origin = %q, want the owner this file resolves", origin)
	}
	if _, found, err := peer.ImportMark("m1"); err != nil || found {
		t.Fatalf("the mark was written for m1 (found=%t, %v), want none for a forged claim", found, err)
	}
}

// The gate is on the row, not on the origin: an entry from the installation that
// does own the row still applies exactly as it did, and lands under the owner
// this file resolved rather than under anything the body said about it.
func TestImporterAppliesEntriesForRowsTheOriginOwns(t *testing.T) {
	t.Parallel()
	peer, _ := victim(t)
	log := NewMemTransport("m3")
	entryFor(t, log, Entry{
		Origin: "m3", Table: "binding", PK: `["01B"]`, Op: OpUpsert,
		SchemaVersion: knownVersion(t, peer),
		Body: bodyFor(t, map[string]any{
			"id": "01B", "name": "01B", "cwd": "/moved",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m3",
		}),
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import an upsert for the row m3 owns: %v", err)
	}
	if got.Applied != 1 || got.Batches != 1 || len(got.Dropped) != 0 {
		t.Fatalf("import = %+v, want the one same-origin entry applied", got)
	}
	if cwd := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = '01B'`); cwd != "/moved" {
		t.Fatalf("the binding's cwd = %q, want the value the body carried", cwd)
	}
	if origin := columnOf(t, peer, `SELECT origin FROM binding WHERE id = '01B'`); origin != "m3" {
		t.Fatalf("the binding's origin = %q, want its own owner", origin)
	}
	if seq, found, err := peer.ImportMark("m3"); err != nil || !found || seq != 1 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the batch's last sequence applied", seq, found, err)
	}
}

// A delete naming a row this file does not hold is still applied: there is
// nothing there to remove, and refusing it would make an entry this machine has
// nothing to do with fail for as long as the log holds it. The same is true of a
// child whose parent is gone, which is the shape a cascade leaves behind on the
// machine that lost it.
func TestImporterSkipsADeleteOfARowItDoesNotHold(t *testing.T) {
	t.Parallel()
	peer, _ := victim(t)
	log := NewMemTransport("m1")
	appendBatch(t, log,
		Entry{Origin: "m1", Table: "binding", PK: `["never-existed"]`, Op: OpDelete,
			SchemaVersion: knownVersion(t, peer)},
		// A child whose parent is not here: its owner cannot be resolved, so
		// there is nothing to remove either.
		Entry{Origin: "m1", Table: "round_file", PK: `["ghost","f"]`, Op: OpDelete,
			SchemaVersion: knownVersion(t, peer)},
	)

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import deletes of rows this file does not hold: %v", err)
	}
	if got.Applied != 2 || len(got.Dropped) != 0 || len(got.Held) != 0 {
		t.Fatalf("import = %+v, want both deletes applied rather than refused", got)
	}
	if have := sharedRows(t, peer); have != `binding ["01A"]|binding ["01B"]` {
		t.Fatalf("the peer holds %q, want both bindings untouched", have)
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 2 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the mark past the no-op deletes", seq, found, err)
	}
}

// A child is placed against the owner its parent resolves to, not against
// whatever the body claims. The parent is written earlier in the same batch, so
// the gate reads this file's own copy of it -- which is the only copy that can be
// trusted to say who owns the row.
func TestImporterGatesAChildOnItsParent(t *testing.T) {
	t.Parallel()
	peer, _ := victim(t)
	log := NewMemTransport("m3")
	appendBatch(t, log,
		Entry{Origin: "m3", Table: "binding_record", PK: `["04A"]`, Op: OpUpsert,
			SchemaVersion: knownVersion(t, peer),
			Body: bodyFor(t, map[string]any{
				"id": "04A", "owner": "o", "name": "04A", "state": "open", "round": 1,
				"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
				"origin": "somebody-else",
			}),
		},
		Entry{Origin: "m3", Table: "round_file", PK: `["04A","f"]`, Op: OpUpsert,
			SchemaVersion: knownVersion(t, peer),
			Body: bodyFor(t, map[string]any{
				"record_id": "04A", "name": "f", "round": 1, "body": []byte{0, 1},
				"bytes": 2, "sha256": "s", "mtime": "t", "sealed_at": "t",
			}),
		},
	)

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a parent and its child from their owner: %v", err)
	}
	if got.Applied != 2 {
		t.Fatalf("import = %+v, want both the parent and the child applied", got)
	}
	if origin := columnOf(t, peer, `SELECT origin FROM binding_record WHERE id = '04A'`); origin != "m3" {
		t.Fatalf("the record's origin = %q, want the owner the entry claimed, not the body's", origin)
	}
	// The child has no owner column of its own: its owner is read through the
	// parent, so the gate passes it and the row lands with nothing forged on it.
	if have := sharedRows(t, peer); have != `binding_record ["04A"]|binding ["01A"]|binding ["01B"]|round_file ["04A","f"]` {
		t.Fatalf("the peer holds %q, want the record and its child written", have)
	}
}
