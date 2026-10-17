package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// The paged-enumerate seam's contract: it returns one installation's own rows of
// a shared table, in key order, with every column the table declares, one bounded
// page at a time, and it resolves ownership the way the outbox triggers do rather
// than by a second rule.
//
// The fixture seeds one row per shared table under a different owner each, so a
// walk has something to find in every table and something to leave alone in every
// other.

// ownedKeysOf is the "table pk" a full walk returned, joined, so a case can say
// which rows it found without counting one table at a time. The walk reads small
// pages so the paging itself is on the path every case exercises.
func ownedKeysOf(t *testing.T, d *DB, tbl, origin string) string {
	t.Helper()
	var out []string
	var after int64
	for {
		rows, err := d.SharedOwnedRowPage(tbl, origin, after, 2)
		if err != nil {
			t.Fatalf("SharedOwnedRowPage %s for %s: %v", tbl, origin, err)
		}
		for _, row := range rows {
			out = append(out, row.PK)
		}
		if len(rows) < 2 {
			return strings.Join(out, ";")
		}
		after = rows[len(rows)-1].RowID
	}
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
	// The histories name their own owner, so a walk finds them under the origin
	// the row carries and no walk resolves them through a parent.
	"availability_event": "instH",
	"latency_sample":     "instH",
}

// Every shared table returns its own installation's row and nobody else's, and
// the key comes back in the outbox's own spelling so it compares against a head
// row as one string.
func TestSharedOwnedRowPageFindsEachTablesOwnRow(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	seeded := sharedRowFixture(t, d)

	for _, seeded := range seededSharedKeys {
		owner := seededOwner[seeded.tbl]
		got := ownedKeysOf(t, d, seeded.tbl, owner)
		if got != seeded.pk {
			t.Errorf("SharedOwnedRowPage %s for %s = %q, want %q", seeded.tbl, owner, got, seeded.pk)
		}
	}
	if len(seeded) != len(SharedTables) {
		t.Fatalf("the fixture seeds %d rows for %d shared tables", len(seeded), len(SharedTables))
	}
}

// The histories keep their time as Unix milliseconds in UTC rather than as
// RFC3339 text, and this is the test that says why. The three events below are
// the shape today's writes actually produce: the same day rendered in two
// different offsets, one of them with a fractional part and one without. Sorted
// as text they come out h3, h2, h1; sorted by the instants they name they are
// h1, h3, h2. Neither text order is the wall-clock order, and the age prune is
// a comparison against exactly this column.
//
// The rendering is kept beside the row in a scratch table, so the test shows the
// two orders disagreeing rather than asserting that they would.
func TestHistoryTimeIsAnInteger(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	// The renderings the machine wrote, and the instant each one names. A is
	// 06:00:00Z, B is 06:30:00Z and C is 06:00:00.5Z, so the true order is A, C,
	// B -- which is neither the lexicographic order of the renderings nor their
	// wall-clock order.
	renderings := []struct {
		id, at string
		ms     int64
	}{
		{"h1", "2026-10-14T09:00:00+03:00", 1760407200000},
		{"h2", "2026-10-14T06:30:00Z", 1760409000000},
		{"h3", "2026-10-14T06:00:00.5Z", 1760407200500},
	}
	if err := d.Tx(func(tx *Tx) error {
		if _, err := tx.exec(`CREATE TABLE event_text (id TEXT PRIMARY KEY, at TEXT NOT NULL)`); err != nil {
			return err
		}
		for _, r := range renderings {
			if err := execTxAll(tx,
				`INSERT INTO availability_event (id, at_ms, kind, provider, source, origin)
				 VALUES ('`+r.id+`', `+strconv.FormatInt(r.ms, 10)+`, 'rate_limited', 'group', 'probe', 'instH')`,
				`INSERT INTO event_text (id, at) VALUES ('`+r.id+`', '`+r.at+`')`,
			); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed the three events with their renderings: %v", err)
	}

	// The declared type is the pin. A column declared TEXT stores these values
	// and compares them as text, and the age prune -- `at_ms < ?` -- would then
	// be decided by a lexicographic rule rather than by the clock.
	wantType := map[string][]string{
		"availability_event": {"at_ms", "since_ms"},
		"latency_sample":     {"at_ms"},
	}
	for tbl, columns := range wantType {
		for _, col := range columns {
			if got := declaredColumnType(t, d, tbl, col); got != "INTEGER" {
				t.Errorf("%s.%s is declared %s, want INTEGER: an ordering column that is text misorders the offsets the writers produce",
					tbl, col, got)
			}
		}
	}

	if got := orderedIDs(t, d, "availability_event", "at_ms"); got != "h1,h3,h2" {
		t.Errorf("availability_event orders as %s, want h1,h3,h2: the instants these three events name", got)
	}
	// The renderings order as h3, h2, h1: the '+03:00' rendering sorts last
	// because its hour reads 09 where the other two read 06, even though it is
	// the earliest event of the three.
	if got := orderedIDs(t, d, "event_text", "at"); got != "h3,h2,h1" {
		t.Errorf("the renderings order as %s, want h3,h2,h1", got)
	}
	// The age prune: everything older than 06:15 UTC, which is h1 and h3 and
	// not h2. Read off the integer column.
	var old []string
	for _, id := range strings.Split(orderedIDs(t, d, "availability_event", "at_ms"), ",") {
		if id == "" {
			continue
		}
		if ms := eventMillis(t, d, id); ms < 1760408100000 {
			old = append(old, id)
		}
	}
	if strings.Join(old, ",") != "h1,h3" {
		t.Errorf("the rows older than the cutoff are %v, want h1,h3", old)
	}
}

// declaredColumnType is one column's declared type as the schema spells it, so a
// case can pin a column's type rather than the value that happens to sit in it.
func declaredColumnType(t *testing.T, d *DB, tbl, column string) string {
	t.Helper()
	var got string
	err := d.Tx(func(tx *Tx) error {
		return tx.queryRow(`SELECT type FROM pragma_table_info(?) WHERE name = ?`, tbl, column).Scan(&got)
	})
	if err != nil {
		t.Fatalf("read the declared type of %s.%s: %v", tbl, column, err)
	}
	return got
}

// orderedIDs is one table's ids in the order its named column sorts them,
// joined, so a case can state an order rather than count rows.
func orderedIDs(t *testing.T, d *DB, tbl, column string) string {
	t.Helper()
	var out []string
	err := d.Tx(func(tx *Tx) error {
		rows, err := tx.conn.QueryContext(tx.ctx,
			`SELECT id FROM `+quoteIdent(tbl)+` ORDER BY `+quoteIdent(column))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read %s ordered by %s: %v", tbl, column, err)
	}
	return strings.Join(out, ",")
}

// eventMillis reads one seeded event's stored instant.
func eventMillis(t *testing.T, d *DB, id string) int64 {
	t.Helper()
	var ms int64
	err := d.Tx(func(tx *Tx) error {
		return tx.queryRow(`SELECT at_ms FROM availability_event WHERE id = ?`, id).Scan(&ms)
	})
	if err != nil {
		t.Fatalf("read the instant of %s: %v", id, err)
	}
	return ms
}

// The two history tables are walked like any other shared table, and on a file
// that has written nothing to them the walk is an empty page rather than an
// error. They arrive empty -- the kv availability and latency documents are
// still the authoritative read -- so this is the state the whole walk sees in
// production until something writes the first event. A walk that reached for a
// table it could not resolve an owner rule for, or that paged a table missing
// from the schema, would refuse here rather than silently skipping the tables.
//
// The second half writes one row of each and walks again: the tables are walked
// under the origin the row names, which is what makes a reconcile of that origin
// see the same rows an export of it would have drained.
func TestSharedOwnedRowPageWalksTheHistoryTables(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	for _, tbl := range AppendOnlyTables {
		if got := ownedKeysOf(t, d, tbl, "instH"); got != "" {
			t.Errorf("SharedOwnedRowPage %s for instH on an empty table = %q, want nothing", tbl, got)
		}
	}

	if err := d.Tx(func(tx *Tx) error {
		return execTxAll(tx,
			`INSERT INTO availability_event (id, at_ms, kind, provider, source, origin)
			 VALUES ('h1', 1767225600000, 'rate_limited', 'group', 'probe', 'instH')`,
			`INSERT INTO latency_sample (id, at_ms, token, ttft_ms, total_ms, origin)
			 VALUES ('s1', 1767225600000, 'tok', 120, 400, 'instH')`,
		)
	}); err != nil {
		t.Fatalf("write one row of each history table: %v", err)
	}

	for _, want := range []struct{ tbl, pk string }{
		{"availability_event", `["h1"]`},
		{"latency_sample", `["s1"]`},
	} {
		if got := ownedKeysOf(t, d, want.tbl, "instH"); got != want.pk {
			t.Errorf("SharedOwnedRowPage %s for instH = %q, want %q", want.tbl, got, want.pk)
		}
		if got := ownedKeysOf(t, d, want.tbl, "instB"); got != "" {
			t.Errorf("SharedOwnedRowPage %s for instB = %q, want none: the row names its own origin", want.tbl, got)
		}
	}
}

// A child resolves through the parent it names, the way its trigger does, so a
// walk for the root's owner finds the child and a walk for any other
// installation does not. This is the case a Go-side owner rule that stopped at
// the parent, or that read the child's own column, would get wrong.
func TestSharedOwnedRowPageResolvesAChildThroughItsParent(t *testing.T) {
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
func TestSharedOwnedRowPageTreatsAnInstallationAsItsOwnOwner(t *testing.T) {
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

// A page returns the row's stored values, BLOB columns as the bytes on disk, so
// the body built from it is what the file holds rather than a rendering of it. A
// compressed body that came back as text would not survive a recompression.
func TestSharedOwnedRowPageReturnsStoredValues(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	rows, err := d.SharedOwnedRowPage("round_file", "instR", 0, 100)
	if err != nil {
		t.Fatalf("SharedOwnedRowPage round_file: %v", err)
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
func TestSharedOwnedRowPageRefusesATableItCannotWalk(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	if _, err := d.SharedOwnedRowPage("sync_outbox", "instR", 0, 10); !errors.Is(err, ErrInvalid) {
		t.Errorf("SharedOwnedRowPage on an unshared table = %v, want ErrInvalid", err)
	}
	if _, err := d.SharedOwnedRowPage("no_such_table", "instR", 0, 10); !errors.Is(err, ErrInvalid) {
		t.Errorf("SharedOwnedRowPage on an unknown table = %v, want ErrInvalid", err)
	}
}

// A page holds no more rows than it was asked for, so a table far larger than a
// page is walked in bounded reads rather than loaded whole. A page of zero is
// empty rather than an unbounded read.
func TestSharedOwnedRowPageBoundsTheRead(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	err := d.Tx(func(t *Tx) error {
		return execTxAll(t,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b1', 'n1', '/x', 'headless', 't', '', 'instB', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b2', 'n2', '/x', 'headless', 't', '', 'instB', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b3', 'n3', '/x', 'headless', 't', '', 'instB', 'instB')`,
		)
	})
	if err != nil {
		t.Fatalf("seed three bindings: %v", err)
	}

	rows, err := d.SharedOwnedRowPage("binding", "instB", 0, 2)
	if err != nil {
		t.Fatalf("SharedOwnedRowPage: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("a page of two returned %d rows, want two", len(rows))
	}
	empty, err := d.SharedOwnedRowPage("binding", "instB", 0, 0)
	if err != nil {
		t.Fatalf("SharedOwnedRowPage with a zero page: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("a zero-sized page returned %d rows, want none", len(empty))
	}
}

// A page reads only the rows its own statement walks: the plan for a page's
// SELECT uses the table's rowid order and stops at the page rather than sorting
// every row -- bodies included -- to answer an order on the key expression. A
// plan that carried a temporary B-tree for the order would be the whole-table
// sort that paging by rowid exists to avoid.
func TestSharedOwnedRowPageReadsOnlyItsOwnRows(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	shared := sharedTable("binding")
	if shared == nil {
		t.Fatal("binding is not a shared table")
	}
	columns, err := exchangeColumns(context.Background(), d.sqlDB, "binding")
	if err != nil {
		t.Fatalf("exchangeColumns: %v", err)
	}
	query, args := ownedRowPageQuery(*shared, OwnerRules["binding"], columns, "instB", 0, 2)

	rows, err := d.sqlDB.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain the page query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil {
		t.Fatalf("plan columns: %v", err)
	}
	var plan []string
	for rows.Next() {
		values := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan the plan: %v", err)
		}
		plan = append(plan, fmt.Sprint(values[len(values)-1]))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the plan: %v", err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "binding") {
		t.Fatalf("the page's plan does not walk the table:\n%s", joined)
	}
	if strings.Contains(joined, "SORTER") || strings.Contains(joined, "B-TREE") {
		t.Errorf("a page sorts every row to answer its order:\n%s", joined)
	}
}

// A later page starts after the rowid the previous one ended on, so a walk sees
// each row once and neither repeats a row nor skips one. The cursor is the rowid
// itself rather than an offset, so a write between two pages cannot shift a row
// across the boundary.
func TestSharedOwnedRowPageContinuesAfterTheCursor(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	err := d.Tx(func(t *Tx) error {
		return execTxAll(t,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b1', 'n1', '/x', 'headless', 't', '', 'instB', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b2', 'n2', '/x', 'headless', 't', '', 'instB', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b3', 'n3', '/x', 'headless', 't', '', 'instB', 'instB')`,
		)
	})
	if err != nil {
		t.Fatalf("seed three bindings: %v", err)
	}

	first, err := d.SharedOwnedRowPage("binding", "instB", 0, 2)
	if err != nil {
		t.Fatalf("SharedOwnedRowPage: %v", err)
	}
	if got := keysOfRows(first); got != `["b1"];["b2"]` {
		t.Fatalf("the first page = %q, want the two lowest keys", got)
	}
	second, err := d.SharedOwnedRowPage("binding", "instB", first[len(first)-1].RowID, 2)
	if err != nil {
		t.Fatalf("SharedOwnedRowPage after the cursor: %v", err)
	}
	if got := keysOfRows(second); got != `["b3"]` {
		t.Fatalf("the page after the cursor = %q, want the rest", got)
	}
}

// keysOfRows is one page's keys joined, so a case can name the rows a page held.
func keysOfRows(rows []ExchangeRow) string {
	var out []string
	for _, row := range rows {
		out = append(out, row.PK)
	}
	return strings.Join(out, ";")
}

// Both rows of the same table under one owner come back in the table's rowid
// order and a row under another owner is left out. The order is what makes a
// walk's output a sequence rather than a set: two rows of one table reaching an
// importer in either order still apply, but the walk's own output should not
// depend on the order the query engine happened to return them in.
func TestSharedOwnedRowPageOrdersAndExcludesForeignRows(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	err := d.Tx(func(t *Tx) error {
		return execTxAll(t,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b2', 'n2', '/y', 'headless', 't', '', 'instB', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b0', 'n0', '/w', 'headless', 't', '', 'instB', 'instB')`,
			`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
			 VALUES ('b3', 'n3', '/v', 'headless', 't', '', 'instZ', 'instZ')`,
		)
	})
	if err != nil {
		t.Fatalf("seed three more bindings: %v", err)
	}

	if got := ownedKeysOf(t, d, "binding", "instB"); got != `["b1"];["b2"];["b0"]` {
		t.Errorf("instB's bindings = %q, want its three in rowid order and the other installation's left out", got)
	}
	if got := ownedKeysOf(t, d, "binding", "instZ"); got != `["b3"]` {
		t.Errorf("instZ's bindings = %q, want only its own", got)
	}
}

// The *Tx form reads inside the caller's transaction, so a reconcile that walks
// and compares can hold the rows and head's view in one consistent read rather
// than seeing a row change between the walk and the comparison.
func TestSharedOwnedRowPageReadsInsideTheCallersTransaction(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	sharedRowFixture(t, d)

	var got string
	err := d.Tx(func(t *Tx) error {
		rows, err := t.SharedOwnedRowPage("binding", "instB", 0, 100)
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
