package db

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// repointOrigin is an installation id of the shape one is minted as, and
// repointSeen is the one timestamp every seeded row carries, so a fixture reads
// as a single moment rather than a range.
const (
	repointOrigin = "01M3ORIGINORIGINORIGINORIGIN"
	repointSeen   = "2026-09-01T10:00:00.000Z"
)

// repointNow is the stamp a start writes.
var repointNow = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

// schemaReference is one foreign key, named by the child that holds it.
type schemaReference struct {
	child, column, parent string
}

// schemaReferencesIntoTwinTables is every foreign key the schema actually holds
// into a table the pass stamps, read from the database rather than from the list
// under test -- so what the schema test compares is the schema against the code
// and never the code against itself.
func schemaReferencesIntoTwinTables(t *testing.T, d *DB) map[schemaReference]bool {
	t.Helper()
	twinRuled := make(map[string]bool)
	for _, table := range backfillTables() {
		twinRuled[table] = true
	}

	refs := map[schemaReference]bool{}
	for _, table := range schemaTables(t, d) {
		rows, err := d.sqlDB.Query(`SELECT "table", "from" FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			t.Fatalf("the foreign keys of %s: %v", table, err)
		}
		for rows.Next() {
			var parent, column string
			if err := rows.Scan(&parent, &column); err != nil {
				t.Fatalf("scan a foreign key of %s: %v", table, err)
			}
			if twinRuled[parent] {
				refs[schemaReference{child: table, column: column, parent: parent}] = true
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate the foreign keys of %s: %v", table, err)
		}
		_ = rows.Close()
	}
	return refs
}

// schemaTables is every table the migrated schema holds, minus the engine's own.
func schemaTables(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.sqlDB.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list the schema's tables: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan a table name: %v", err)
		}
		if !strings.HasPrefix(name, "sqlite_") {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the schema's tables: %v", err)
	}
	return tables
}

// TestBackfillTwinReferencesMatchTheSchema pins the reference list against the
// schema it is read from, in both directions.
//
// The forward direction is what the field report turned on: a foreign key the
// schema holds into a twin-ruled table that this list omits is a stale row the
// pass still cannot drop, and it presents as the whole pass skipping with no
// progress -- exactly what the journal line showed.
//
// The backward direction matters just as much. A reference here the schema does
// not have is a repoint of a table or column that does not exist, so the pass
// would fail on it rather than settle anything. Either drift is a row the rule
// cannot resolve and cannot say why, so it is the test's job to catch.
func TestBackfillTwinReferencesMatchTheSchema(t *testing.T) {
	d := directOpenTestDB(t)
	inSchema := schemaReferencesIntoTwinTables(t, d)

	// Forward: the schema's keys are all carried. binding_record's are the
	// expected exception -- nothing drops a record row, so nothing repoints out
	// of its way -- and the exception is stated rather than skipped, so a table
	// that stopped halting would show up here as an uncarried key.
	for key := range inSchema {
		if key.parent == "binding_record" {
			continue
		}
		if !slices.ContainsFunc(twinReferencesFor(key.parent), func(r twinReference) bool {
			return r.child == key.child && r.column == key.column
		}) {
			t.Errorf("the schema holds %s.%s -> %s(id), which backfillTwinReferences does not carry: "+
				"the pass could not drop a stale %s row anything named", key.child, key.column, key.parent, key.parent)
		}
	}

	// Backward: every reference carried is one the schema has, into a table the
	// pass actually stamps.
	for parent, refs := range backfillTwinReferences {
		if !slices.Contains(backfillTables(), parent) {
			t.Errorf("backfillTwinReferences carries references into %s, which the pass does not stamp", parent)
			continue
		}
		for _, ref := range refs {
			if key := (schemaReference{child: ref.child, column: ref.column, parent: parent}); !inSchema[key] {
				t.Errorf("backfillTwinReferences carries %s.%s -> %s(id), which the schema does not have",
					ref.child, ref.column, parent)
			}
		}
	}
}

// seedRepoTwin is one checkout recorded twice: an unstamped row and this
// installation's stamped row over the same common_dir, which is the pair the
// collision the twin rule reads is made of.
func seedRepoTwin(t *testing.T, d *DB, staleID, liveID, staleURL, liveURL, dir, firstSeen string) {
	t.Helper()
	for _, row := range []struct{ id, stamp, url, seen string }{
		{staleID, "", staleURL, repointSeen},
		{liveID, repointOrigin, liveURL, firstSeen},
	} {
		if _, err := d.sqlDB.Exec(`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen)
			VALUES (?, ?, ?, ?, ?)`, row.id, row.stamp, row.url, dir, row.seen); err != nil {
			t.Fatalf("insert the %s repo row: %v", row.id, err)
		}
	}
}

// seedBinding is a binding row stamped or not, as the caller needs it.
func seedBinding(t *testing.T, d *DB, id, stamp, name, repoID, createdAt string) {
	t.Helper()
	var repo any
	if repoID != "" {
		repo = repoID
	}
	if _, err := d.sqlDB.Exec(`INSERT INTO binding (id, origin, name, repo_id, cwd, created_at, builder_mode, ingest_source)
		VALUES (?, ?, ?, ?, '/work', ?, 'runner', 'manual')`, id, stamp, name, repo, createdAt); err != nil {
		t.Fatalf("insert the %s binding: %v", id, err)
	}
}

// seedRound is a round at number 1 on one binding.
func seedRound(t *testing.T, d *DB, id, bindingID string) {
	t.Helper()
	if _, err := d.sqlDB.Exec(`INSERT INTO round (id, binding_id, number, started_at, outcome, switches)
		VALUES (?, ?, 1, ?, 'green', 0)`, id, bindingID, repointSeen); err != nil {
		t.Fatalf("insert the %s round: %v", id, err)
	}
}

// seedEvent is one event on a binding and the round it belongs to.
func seedEvent(t *testing.T, d *DB, id, bindingID, roundID string) {
	t.Helper()
	if _, err := d.sqlDB.Exec(`INSERT INTO event (id, binding_id, round_id, seq, ts, kind, direction, entry_json, confirmed, late)
		VALUES (?, ?, ?, 0, ?, 'note', 'in', '{}', 0, 0)`, id, bindingID, roundID, repointSeen); err != nil {
		t.Fatalf("insert the %s event: %v", id, err)
	}
}

// rowID is the id column of one row, or "" when the row is gone. A missing row is
// the expected outcome in half these tests, so it reads as a value rather than an
// error the caller has to convert.
func rowID(t *testing.T, d *DB, query string, args ...any) string {
	t.Helper()
	var id string
	err := d.sqlDB.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("select %q: %v", query, err)
	}
	return id
}

// countRows is how many rows a query matches.
func countRows(t *testing.T, d *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.sqlDB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// assertBindingsRepointedTo pins the attribution a repoint has to preserve: each
// named binding now points at the live id, and every other column on it is what
// it was. The second half matters as much as the first -- a repoint that rewrote
// more than the pointer would pass the first check while moving attribution.
func assertBindingsRepointedTo(t *testing.T, d *DB, names []string, wantRepoID, wantCreated string) {
	t.Helper()
	for _, name := range names {
		var repoID, cwd, createdAt, ingest string
		if err := d.sqlDB.QueryRow(`SELECT repo_id, cwd, created_at, ingest_source FROM binding WHERE id = ?`, "b-"+name).
			Scan(&repoID, &cwd, &createdAt, &ingest); err != nil {
			t.Fatalf("select the %s binding: %v", name, err)
		}
		if repoID != wantRepoID {
			t.Errorf("the %s binding points at repo %q, want the live id %q", name, repoID, wantRepoID)
		}
		if cwd != "/work" || createdAt != wantCreated || ingest != "manual" {
			t.Errorf("the %s binding = cwd %q created_at %q ingest %q, want them unchanged by the repoint",
				name, cwd, createdAt, ingest)
		}
	}
}

// assertLiveRepoUntouched pins that the rule resolved in favour of the stamped row
// without writing to it: the live row still carries exactly the bytes it was
// seeded with. A pass that stamped, refreshed or re-keyed the surviving twin
// would settle the collision and lose the row's history.
func assertLiveRepoUntouched(t *testing.T, d *DB, liveID, wantURL, wantDir, wantSeen string) {
	t.Helper()
	var origin, gotURL, gotDir, gotSeen string
	if err := d.sqlDB.QueryRow(`SELECT origin, origin_url, common_dir, first_seen FROM repo WHERE id = ?`, liveID).
		Scan(&origin, &gotURL, &gotDir, &gotSeen); err != nil {
		t.Fatalf("select the live repo row: %v", err)
	}
	if origin != repointOrigin || gotURL != wantURL || gotDir != wantDir || gotSeen != wantSeen {
		t.Errorf("the live repo row = origin %q url %q dir %q first_seen %q, want it byte-identical to what it was",
			origin, gotURL, gotDir, gotSeen)
	}
}

// TestBackfillResolvesStaleRepoTwinsThatBindingsName is the field report's shape:
// a stale ” repo row, this installation's stamped live row holding the same
// common_dir, and stamped bindings pointing at the stale id. The pass completes,
// the bindings are repointed at the live id, the stale row is gone, the live row
// is byte-identical, and the gate counts nothing.
//
// Repointing is not a cosmetic step here: without it the drop is refused by the
// foreign key and the pass settles nothing at all, which is the line the field
// journal carried.
func TestBackfillResolvesStaleRepoTwinsThatBindingsName(t *testing.T) {
	d := directOpenTestDB(t)
	const (
		liveID   = "01M359NHDFV7PJTKMAMZNE26X6"
		liveURL  = "https://github.com/fuad-daoud/relevo-site"
		liveSeen = "2026-09-20T10:00:00.000Z"
		dir      = "/home/fuad/projects/relevo-site/.git"
	)
	seedRepoTwin(t, d, "r-stale", liveID, "https://github.com/fuad-daoud/relay-site", liveURL, dir, liveSeen)
	// A third stamped repo row over a different checkout. It is not a twin of
	// anything here, so nothing may be repointed at it: a live row the pair did
	// not name is the one wrong id that would still satisfy the foreign key, and
	// the assertions below are what catch it.
	if _, err := d.sqlDB.Exec(`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen)
		VALUES ('r-decoy', ?, 'https://github.com/acme/other.git', '/home/fuad/projects/other/.git', '2026-09-21T10:00:00.000Z')`,
		repointOrigin); err != nil {
		t.Fatalf("insert the decoy repo row: %v", err)
	}

	// The field's own bindings: they share one repo_id, and every one is stamped,
	// which is why only the repo side was ever a twin.
	names := []string{"money-typ", "ai", "money-repin", "money-allowance", "money-evid", "spaceapi", "spaceapi-2"}
	for _, name := range names {
		seedBinding(t, d, "b-"+name, repointOrigin, name, "r-stale", "2026-09-05T10:00:00.000Z")
	}

	before, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins before the pass: %v", err)
	}
	if before.Tables[2].Rows != 1 {
		t.Fatalf("repo holds %d unstamped rows before the pass, want 1 (the stale one)", before.Tables[2].Rows)
	}

	stats, ran, err := BackfillOriginOnce(d, repointOrigin, repointNow)
	if err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
	if !ran {
		t.Fatal("the pass reported ran = false, want it to have settled the pair and recorded itself")
	}
	if stats.Dropped() != 1 {
		t.Errorf("the pass dropped %d stale rows, want 1", stats.Dropped())
	}
	if stats.Halted() != 0 {
		t.Errorf("the pass halted on %d rows, want 0: the bindings were repointed, so nothing needed a person",
			stats.Halted())
	}
	if got := stats.HaltedByTable(); len(got) != 0 {
		t.Errorf("HaltedByTable() = %v, want empty on a pass that halted nothing", got)
	}

	// The stale row is gone and the live row is untouched: the rule resolved in
	// favour of the stamped row, not the other way round.
	if left := countRows(t, d, `SELECT COUNT(*) FROM repo WHERE id = 'r-stale'`); left != 0 {
		t.Error("the stale '' repo row survived the pass, want it dropped for the stamped live row")
	}
	if left := countRows(t, d, `SELECT COUNT(*) FROM repo WHERE id = 'r-decoy'`); left != 1 {
		t.Error("the decoy repo row is gone, want it untouched: it is not the twin of anything here")
	}
	assertLiveRepoUntouched(t, d, liveID, liveURL, dir, liveSeen)

	// The attribution assertion: every binding that named the stale id now names
	// the live one, and nothing else about it moved. A repoint to any other id
	// would leave these pointing at a repo row the pair never named.
	assertBindingsRepointedTo(t, d, names, liveID, "2026-09-05T10:00:00.000Z")

	// The bindings were already stamped, so the pass moved no binding rows: the
	// only thing it did in binding was nothing, which is the point of the shape.
	if stats.Bindings != 0 {
		t.Errorf("the pass stamped %d binding rows, want 0: they were all stamped already", stats.Bindings)
	}

	after, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after the pass: %v", err)
	}
	if !after.Empty() {
		t.Errorf("counts after the pass = %s, want none: the gate would refuse an enable on a resolved database", after)
	}
}

// TestBackfillRepointsRoundsAndEventsOntoTheLiveBinding pins the same repair on
// the other twin-ruled table with references of its own. binding is named by
// round, by event and by binding.forked_from_binding_id, so a stale binding twin
// needs all three moved before its drop can go through -- and round is keyed
// (binding_id, number), so a repoint to the wrong id is a unique violation
// rather than a silent misattribution.
func TestBackfillRepointsRoundsAndEventsOntoTheLiveBinding(t *testing.T) {
	d := directOpenTestDB(t)
	// The same binding name and created_at twice: that pair is what the
	// (origin, name, created_at) index collides on.
	seedBinding(t, d, "b-stale", "", "api", "", repointSeen)
	seedBinding(t, d, "b-live", repointOrigin, "api", "", repointSeen)
	seedRound(t, d, "rd-1", "b-stale")
	seedEvent(t, d, "ev-1", "b-stale", "rd-1")
	if _, err := d.sqlDB.Exec(`INSERT INTO binding (id, origin, name, forked_from_binding_id, cwd, created_at, builder_mode, ingest_source)
		VALUES ('b-fork', ?, 'api-fork', 'b-stale', '/work', '2026-10-01T10:00:00.000Z', 'runner', 'manual')`,
		repointOrigin); err != nil {
		t.Fatalf("insert the forked binding: %v", err)
	}

	stats, ran, err := BackfillOriginOnce(d, repointOrigin, repointNow)
	if err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
	if !ran {
		t.Fatal("the pass reported ran = false, want it to have settled the binding pair")
	}
	if stats.Dropped() != 1 {
		t.Errorf("the pass dropped %d stale rows, want 1 (the stale binding)", stats.Dropped())
	}
	if stats.Halted() != 0 {
		t.Errorf("the pass halted on %d rows, want 0: all three references were repointed", stats.Halted())
	}
	if left := countRows(t, d, `SELECT COUNT(*) FROM binding WHERE id = 'b-stale'`); left != 0 {
		t.Error("the stale '' binding survived the pass, want it dropped for the stamped live row")
	}
	for _, ref := range []struct{ what, sql string }{
		{"round", `SELECT binding_id FROM round WHERE id = 'rd-1'`},
		{"event", `SELECT binding_id FROM event WHERE id = 'ev-1'`},
		{"forked binding", `SELECT forked_from_binding_id FROM binding WHERE id = 'b-fork'`},
	} {
		if got := rowID(t, d, ref.sql); got != "b-live" {
			t.Errorf("the %s points at %q, want the live binding id", ref.what, got)
		}
	}

	after, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after the pass: %v", err)
	}
	if !after.Empty() {
		t.Errorf("counts after the pass = %s, want none", after)
	}
}

// TestBackfillHaltsARowWhoseReferenceCannotMove pins the one case the repoint
// cannot resolve, and pins that it costs one row rather than the pass.
//
// round is keyed (binding_id, number). Moving the stale binding's round onto the
// live binding would collide with a round the live binding already holds at the
// same number, and two histories of one binding cannot be merged by moving a
// pointer. So the pass leaves that row unstamped for a person and carries on: the
// other twin in the same table is settled, and the halt is reported against its
// table so a journal line can name it.
func TestBackfillHaltsARowWhoseReferenceCannotMove(t *testing.T) {
	d := directOpenTestDB(t)
	// Two colliding pairs. The first pair's live binding already holds round 1, so
	// the stale binding's round 1 cannot move onto it. The second pair has nothing
	// in the way, so the pass has a row it can settle in the same table the halt
	// came from.
	for _, pair := range []struct{ stale, live, createdAt string }{
		{"b-blocked-stale", "b-blocked-live", repointSeen},
		{"b-free-stale", "b-free-live", "2026-09-05T10:00:00.000Z"},
	} {
		seedBinding(t, d, pair.stale, "", "api", "", pair.createdAt)
		seedBinding(t, d, pair.live, repointOrigin, "api", "", pair.createdAt)
	}
	seedRound(t, d, "rd-blocked-stale", "b-blocked-stale")
	seedRound(t, d, "rd-blocked-live", "b-blocked-live")

	stats, ran, err := BackfillOriginOnce(d, repointOrigin, repointNow)
	if err == nil {
		t.Fatal("the pass returned no error on a reference that cannot move, want a halt for a person")
	}
	if ran {
		t.Error("a halted pass reported ran = true, want false: it recorded nothing")
	}
	if stats.Halted() != 1 {
		t.Errorf("the pass halted on %d rows, want 1", stats.Halted())
	}
	if stats.Dropped() != 1 {
		t.Errorf("the pass dropped %d stale rows, want 1: the halt cost the one row, not the pass", stats.Dropped())
	}

	// The halt names its table and how many rows, which is what a journal line
	// reports and what a person acts on.
	if byTable := stats.HaltedByTable(); len(byTable) != 1 || byTable["binding"] != 1 {
		t.Errorf("HaltedByTable() = %v, want binding=1", byTable)
	}

	// The blocked row is untouched on both sides: no half-migrated history, and
	// the stale binding still holds its own round.
	if stamp := rowID(t, d, `SELECT origin FROM binding WHERE id = 'b-blocked-stale'`); stamp != "" {
		t.Errorf("the blocked stale binding was stamped as %q, want it left unstamped for a person", stamp)
	}
	if got := rowID(t, d, `SELECT binding_id FROM round WHERE id = 'rd-blocked-stale'`); got != "b-blocked-stale" {
		t.Errorf("the blocked round points at %q, want it left on the stale binding: a halt must not half-migrate", got)
	}
	if got := rowID(t, d, `SELECT binding_id FROM round WHERE id = 'rd-blocked-live'`); got != "b-blocked-live" {
		t.Errorf("the live round points at %q, want it untouched", got)
	}

	// The pass continued past the halt and settled the other twin.
	if left := countRows(t, d, `SELECT COUNT(*) FROM binding WHERE id = 'b-free-stale'`); left != 0 {
		t.Error("the free stale binding is still present, want the pass to have settled it past the halt")
	}

	// And the halted row is the only thing left unstamped, so a second start
	// resumes on exactly it rather than starting over.
	if len(stats.LeftUnstamped) != 1 || stats.LeftUnstamped["binding"] != 1 {
		t.Errorf("LeftUnstamped = %v, want binding=1", stats.LeftUnstamped)
	}
	if _, ok, err := d.KVGet(originBackfillKVKey); err != nil || ok {
		t.Errorf("kv row %s = (_, %v, %v), want it absent so the next start resumes", originBackfillKVKey, ok, err)
	}
}

// TestBackfillRepointRefusalIsNotAFailure pins the classification the halt rests
// on: a unique violation is the pass declining to merge two histories, not a
// database failure, and a foreign-key refusal is likewise a decision rather than
// an error. Both must be recognisable from the shape each engine actually
// produces, because the Turso engine reports the primary SQLITE_CONSTRAINT in
// both code slots and so carries no extended code to tell the two apart.
func TestBackfillRepointRefusalIsNotAFailure(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantFK  bool
		wantUni bool
	}{
		{
			name:    "a foreign key refusal names itself in the message",
			err:     errors.New("turso: constraint failed: FOREIGN KEY constraint failed"),
			wantFK:  true,
			wantUni: false,
		},
		{
			name:    "a unique refusal names itself in the message",
			err:     errors.New("turso: constraint failed: UNIQUE constraint failed: round.binding_id, round.number"),
			wantUni: true,
		},
		{
			name:    "another constraint is neither",
			err:     errors.New("turso: constraint failed: NOT NULL constraint failed: repo.first_seen"),
			wantFK:  false,
			wantUni: false,
		},
		{
			name:    "an unrelated failure is neither",
			err:     errors.New("database is locked"),
			wantFK:  false,
			wantUni: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isForeignKeyRefusal(tc.err); got != tc.wantFK {
				t.Errorf("isForeignKeyRefusal(%v) = %t, want %t", tc.err, got, tc.wantFK)
			}
			if got := isUniqueViolation(tc.err); got != tc.wantUni {
				t.Errorf("isUniqueViolation(%v) = %t, want %t", tc.err, got, tc.wantUni)
			}
		})
	}
}

// TestSettleStaleRowHaltsRatherThanFailing pins the row-level outcome directly,
// over the foreign key the field hit: a stale repo row a binding still names must
// come back as halted, not as an error, and the binding must still be pointing at
// it. This is the branch the field proved untested, called on its own so the
// classification is pinned without a whole pass around it.
func TestSettleStaleRowHaltsRatherThanFailing(t *testing.T) {
	d := directOpenTestDB(t)
	const dir = "/work/.git"
	seedRepoTwin(t, d, "r-stale", "r-live", "https://o/a.git", "https://o/b.git", dir, "2026-09-20T10:00:00.000Z")
	seedBinding(t, d, "b-1", repointOrigin, "api", "r-stale", "2026-09-05T10:00:00.000Z")

	// With binding.repo_id dropped from the repoint list the drop meets the
	// foreign key the schema still holds, which is the refusal the halt exists
	// for. Restoring it afterwards keeps the fixture's own schema intact.
	saved := backfillTwinReferences["repo"]
	backfillTwinReferences["repo"] = nil
	t.Cleanup(func() { backfillTwinReferences["repo"] = saved })

	halted, err := settleStaleRow(d, "repo", "r-stale", "r-live")
	if err != nil {
		t.Fatalf("settleStaleRow on a row the engine refuses = (_, %v), want it halted, not failed", err)
	}
	if !halted {
		t.Error("settleStaleRow reported the row settled, want it halted: a binding still names it")
	}
	if left := countRows(t, d, `SELECT COUNT(*) FROM repo WHERE id = 'r-stale'`); left != 1 {
		t.Error("the stale repo row is gone, want it left standing for a person")
	}
	if got := rowID(t, d, `SELECT repo_id FROM binding WHERE id = 'b-1'`); got != "r-stale" {
		t.Errorf("the binding points at repo %q, want it left on the stale row", got)
	}
}

// TestRepointRefusalNamesTheColumnItCannotMove pins that the refusal says which
// reference stopped it, so a halt is actionable from the log alone. The message
// names the table and column and never a row's contents.
func TestRepointRefusalNamesTheColumnItCannotMove(t *testing.T) {
	err := error(&repointRefusal{table: "round", column: "binding_id"})
	if !strings.Contains(err.Error(), "round.binding_id") {
		t.Errorf("the refusal %q does not name the reference it could not move", err)
	}
	var target *repointRefusal
	if !errors.As(fmt.Errorf("wrapped: %w", err), &target) {
		t.Error("a wrapped refusal is not recoverable as one, want the pass to classify it through the wrap")
	}
}
