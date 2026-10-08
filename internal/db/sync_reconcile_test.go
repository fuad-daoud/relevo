package db

import (
	"errors"
	"strings"
	"testing"
)

// The enumerate-a-table seam's contract: it returns one installation's own rows
// of a shared table, in key order, with every column the table declares, and it
// resolves ownership the way the outbox triggers do rather than by a second rule.
//
// The fixture seeds one row per shared table under a different owner each, so a
// walk has something to find in every table and something to leave alone in every
// other.

// ownedKeysOf is the "table pk" a walk returned, joined, so a case can say which
// rows it found without counting one table at a time.
func ownedKeysOf(t *testing.T, d *DB, tbl, origin string) string {
	t.Helper()
	rows, err := d.SharedOwnedRows(tbl, origin)
	if err != nil {
		t.Fatalf("SharedOwnedRows %s for %s: %v", tbl, origin, err)
	}
	var out []string
	for _, row := range rows {
		out = append(out, row.PK)
	}
	return strings.Join(out, ";")
}

// seededOwner is the installation each seeded row belongs to. It is written out
// rather than derived, because the point of the fixture is that the rows have
// different owners: a walk for one of them has to find its own row and no other.
var seededOwner = map[string]string{
	"repo":           "instP",
	"mastermind":     "instM",
	"binding_record": "instR",
	"installation":   "instI",
	"binding":        "instB",
	"chains":         "instC",
	// The four rows that resolve through a parent inherit that parent's owner,
	// which is the case a second owner rule would get wrong.
	"binding_event": "instR",
	"round_file":    "instR",
	"chain_event":   "instC",
	"chain_member":  "instC",
	"chain_check":   "instC",
	// Two hops out, through a round and then a binding.
	"round":      "instB",
	"event":      "instB",
	"artifact":   "instB",
	"transcript": "instB",
}

// Every shared table returns its own installation's row and nobody else's, and
// the key comes back in the outbox's own spelling so it compares against a head
// row as one string.
func TestSharedOwnedRowsFindsEachTablesOwnRow(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	seeded := sharedRowFixture(t, d)

	for _, seeded := range seededSharedKeys {
		owner := seededOwner[seeded.tbl]
		got := ownedKeysOf(t, d, seeded.tbl, owner)
		if got != seeded.pk {
			t.Errorf("SharedOwnedRows %s for %s = %q, want %q", seeded.tbl, owner, got, seeded.pk)
		}
	}
	if len(seeded) != len(SharedTables) {
		t.Fatalf("the fixture seeds %d rows for %d shared tables", len(seeded), len(SharedTables))
	}
}

// A child resolves through the parent it names, the way its trigger does, so a
// walk for the root's owner finds the child and a walk for any other
// installation does not. This is the case a Go-side owner rule that stopped at
// the parent, or that read the child's own column, would get wrong.
func TestSharedOwnedRowsResolvesAChildThroughItsParent(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	// binding_event hangs off binding_record, whose owner is instR.
	if got := ownedKeysOf(t, d, "binding_event", "instR"); got != `["rec1",1]` {
		t.Errorf("the record's event for instR = %q, want the one it owns through the record", got)
	}
	if got := ownedKeysOf(t, d, "binding_event", "instB"); got != "" {
		t.Errorf("the record's event for instB = %q, want none: the binding does not own it", got)
	}

	// artifact is two hops out, through a round and then a binding.
	if got := ownedKeysOf(t, d, "artifact", "instB"); got != `["a1"]` {
		t.Errorf("the round's artifact for instB = %q, want the one the two hops resolve to", got)
	}
	if got := ownedKeysOf(t, d, "artifact", "instR"); got != "" {
		t.Errorf("the round's artifact for instR = %q, want none: the record is not on that path", got)
	}
}

// The installation table resolves to its own id rather than reading an origin
// column, because a row of it is the installation. A walk for any other
// installation must not find it.
func TestSharedOwnedRowsTreatsAnInstallationAsItsOwnOwner(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	if got := ownedKeysOf(t, d, "installation", "instI"); got != `["instI"]` {
		t.Errorf("the installation for itself = %q, want its own row", got)
	}
	if got := ownedKeysOf(t, d, "installation", "instB"); got != "" {
		t.Errorf("the installation for instB = %q, want none", got)
	}
}

// A walk returns the row's stored values, BLOB columns as the bytes on disk, so
// the body built from it is what the file holds rather than a rendering of it. A
// compressed body that came back as text would not survive a recompression.
func TestSharedOwnedRowsReturnsStoredValues(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	rows, err := d.SharedOwnedRows("round_file", "instR")
	if err != nil {
		t.Fatalf("SharedOwnedRows round_file: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("round_file returned %d rows, want the one the fixture wrote", len(rows))
	}
	columns := map[string]any{}
	for _, col := range rows[0].Columns {
		columns[col.Name] = col.Value
	}
	body, ok := columns["body"].([]byte)
	if !ok {
		t.Fatalf("the body column came back as %T, want the stored bytes", columns["body"])
	}
	if len(body) == 0 {
		t.Error("the body column came back empty, want the bytes the fixture wrote")
	}
	// The codec column travels beside it, which is what lets an importer put the
	// row back without deciding whether it should recompress it.
	if codec, ok := columns["body_codec"].(int64); !ok || codec != 1 {
		t.Errorf("the body_codec column = %v (%T), want the integer 1 the fixture wrote", columns["body_codec"], columns["body_codec"])
	}
}

// A table no shared table names, and a shared table with no owner rule, are both
// refused rather than walked: a name reaching SQL has to be one this package
// already classified.
func TestSharedOwnedRowsRefusesATableItCannotWalk(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	if _, err := d.SharedOwnedRows("sync_outbox", "instR"); !errors.Is(err, ErrInvalid) {
		t.Errorf("SharedOwnedRows on an unshared table = %v, want ErrInvalid", err)
	}
	if _, err := d.SharedOwnedRows("no_such_table", "instR"); !errors.Is(err, ErrInvalid) {
		t.Errorf("SharedOwnedRows on an unknown table = %v, want ErrInvalid", err)
	}
}

// A second row of the same table under the same owner is returned too, in key
// order, and a row under another owner is left out. The order is what makes a
// walk's output a sequence rather than a set: two rows of one table reaching an
// importer in either order still apply, but the walk's own output should not
// depend on the order the query engine happened to return them in.
func TestSharedOwnedRowsOrdersAndExcludesForeignRows(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	err := d.Tx(func(t *Tx) error {
		return execTxAll(t,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin)
			 VALUES ('b2', 'n2', '/y', 'headless', 't', '', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin)
			 VALUES ('b0', 'n0', '/w', 'headless', 't', '', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin)
			 VALUES ('b3', 'n3', '/v', 'headless', 't', '', 'instZ')`,
		)
	})
	if err != nil {
		t.Fatalf("seed three more bindings: %v", err)
	}

	if got := ownedKeysOf(t, d, "binding", "instB"); got != `["b0"];["b1"];["b2"]` {
		t.Errorf("instB's bindings = %q, want its three in key order and the other installation's left out", got)
	}
	if got := ownedKeysOf(t, d, "binding", "instZ"); got != `["b3"]` {
		t.Errorf("instZ's bindings = %q, want only its own", got)
	}
}

// The *Tx form reads inside the caller's transaction, so a reconcile that walks
// and compares can hold the rows and head's view in one consistent read rather
// than seeing a row change between the walk and the comparison.
func TestSharedOwnedRowsReadsInsideTheCallersTransaction(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	var got string
	err := d.Tx(func(t *Tx) error {
		rows, err := t.SharedOwnedRows("binding", "instB")
		if err != nil {
			return err
		}
		for _, row := range rows {
			got = row.PK
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the owned rows inside a Tx: %v", err)
	}
	if got != `["b1"]` {
		t.Fatalf("the Tx read %q, want the binding the fixture wrote", got)
	}
}
