package availability

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// A join is where both halves of the tables have to hold at once. A fresh
// joiner brings its own history -- the documents it recorded before the tables
// existed, migrated into rows on the machine it is joining from -- and receives
// a peer's. It then sees both origins' rows, because a display read combines
// them, and pushes none of the peer's back, because that row belongs to the
// machine that wrote it.
//
// This runs the real exchange: exporter, importer and reconciler, in that order,
// across two real files and a fake log, until neither machine moves. Nothing is
// handed to the importer by hand.

// joinMachine is one installation over the split pair the daemon opens: the
// shared file, where the history rows and the outbox live, with the
// machine-local file the retired documents lived in attached.
func joinMachine(t *testing.T, origin string) *db.DB {
	t.Helper()
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("open the file of %s: %v", origin, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if d.Local() == nil {
		t.Fatalf("%s opened without a machine-local file, so the migration has nowhere local to read", origin)
	}
	return d
}

// joinRound is one machine's full pass over the exchange, in the order the
// parts depend on each other: the outbox first, so the log learns what this
// machine wrote; then the other origins' entries, so the file learns what they
// wrote; then reconcile, which compares the file against head. It reports
// whether the machine exported anything and whether it applied anything.
func joinRound(t *testing.T, log *synclog.MemTransport, d *db.DB) (moved, applied bool) {
	t.Helper()
	handle := log.OnLog(d.Origin())

	exported, err := synclog.NewExporter(d, handle).Export()
	if err != nil {
		t.Fatalf("%s: export: %v", d.Origin(), err)
	}
	imported, err := synclog.NewImporter(d, handle).Import()
	if err != nil {
		t.Fatalf("%s: import: %v", d.Origin(), err)
	}
	if _, err := synclog.NewReconciler(d, handle).Reconcile(); err != nil {
		t.Fatalf("%s: reconcile: %v", d.Origin(), err)
	}
	return exported.Appended > 0, imported.Applied > 0
}

// joinSettle runs rounds on the pair until neither machine moved in one, and
// reports the bound it took. Four is the bound the synclog convergence cases
// use: a round on each machine is enough to cross history in both directions,
// and a pair that needs more than a couple is not converging at all.
func joinSettle(t *testing.T, log *synclog.MemTransport, first, second *db.DB) int {
	t.Helper()
	const bound = 4
	for round := 1; round <= bound; round++ {
		firstMoved, _ := joinRound(t, log, first)
		secondMoved, _ := joinRound(t, log, second)
		if !firstMoved && !secondMoved {
			return round
		}
	}
	return bound
}

// rowsByOrigin is every row of one history table grouped by the origin that owns
// it, so a case says which machine owns what a file holds rather than only how
// many rows there are.
func rowsByOrigin(t *testing.T, d *db.DB, table string) map[string][]string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open %s for reading: %v", d.Path(), err)
	}
	defer func() { _ = raw.Close() }()

	rows, err := raw.Query("SELECT id, origin FROM " + table + " ORDER BY id")
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string][]string{}
	for rows.Next() {
		var id, origin string
		if err := rows.Scan(&id, &origin); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		out[origin] = append(out[origin], id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	return out
}

// assertHoldsBothOrigins fails unless the file holds exactly one row of each
// named origin in the named table: the joiner's own migrated row and the peer's
// imported one.
func assertHoldsBothOrigins(t *testing.T, d *db.DB, table, own, peer string) {
	t.Helper()
	rows := rowsByOrigin(t, d, table)
	if len(rows[own]) != 1 || len(rows[peer]) != 1 || len(rows) != 2 {
		t.Errorf("%s on %s holds %v, want one row under %s and one under %s",
			table, d.Origin(), rows, own, peer)
	}
}

// TestJoinReceivesPeerHistoryAndExportsOwn is Round 3 step 4: a fresh joiner
// receives a peer's history rows over the real exchange, and its own migrated
// rows go the other way.
//
// It fails in the three ways the round's wiring can break. If a history row did
// not enter the outbox, the peer's rows never arrive and the joiner holds one
// origin where it should hold two. If the importer refused a peer's row the log
// legitimately carries, the peer side of the join fails. If the exporter did not
// scope to the rows this machine owns, the peer's rows come back in the
// joiner's head and the echo assertion below catches it.
func TestJoinReceivesPeerHistoryAndExportsOwn(t *testing.T) {
	t.Parallel()

	const (
		peerOrigin   = "m1"
		joinerOrigin = "m2"
	)

	// The rows are recent, because a row arriving older than the retention
	// window is skipped rather than imported (the importer's arrival rule), and
	// a case about the join must not be about the window instead.
	now := time.Now().UTC()
	at := db.UnixMS(now.Add(-time.Hour))

	// The peer, which joined earlier and has history of its own.
	theirs := joinMachine(t, peerOrigin)
	if err := theirs.InsertAvailabilityEvent(peerOrigin, db.AvailabilityEvent{
		ID: db.NewID(), AtMS: at,
		Kind: string(RateLimited), Provider: "test", Token: testClaudeRef, Source: "planner",
	}); err != nil {
		t.Fatalf("the peer's availability row: %v", err)
	}
	if err := theirs.InsertLatencySample(peerOrigin, db.LatencySample{
		ID: db.NewID(), AtMS: at,
		Token: testClaudeRef, Host: "peer", TTFTMS: 900, TotalMS: 1200,
	}); err != nil {
		t.Fatalf("the peer's latency row: %v", err)
	}

	// The joiner, arriving with the two documents it recorded before the tables
	// existed. The migration runs where the runtime opens both files, which is
	// before the exchange starts, so its window is rows by the time the join
	// does.
	mine := joinMachine(t, joinerOrigin)
	seedDocuments(t, mine.Local(),
		[]Event{{At: now.Add(-2 * time.Hour), Kind: SpawnFailed, Provider: "test", Source: "relevo"}},
		[]Sample{{At: now.Add(-2 * time.Hour), Token: testClaudeRef, Host: "laptop", TTFTMS: 500, TotalMS: 800}})

	migrated, err := MigrateHistoryDocuments(mine.Local(), mine, joinerOrigin)
	if err != nil {
		t.Fatalf("MigrateHistoryDocuments on the joiner: %v", err)
	}
	if migrated.Events != 1 || migrated.Samples != 1 {
		t.Fatalf("the joiner migrated %+v, want 1 event and 1 sample", migrated)
	}

	log := synclog.NewMemTransport(peerOrigin)
	if rounds := joinSettle(t, log, mine, theirs); rounds == 4 {
		t.Fatal("the pair needed the whole bound of 4 rounds, want it to settle")
	}

	// The joiner holds both origins, in both tables.
	assertHoldsBothOrigins(t, mine, "availability_event", joinerOrigin, peerOrigin)
	assertHoldsBothOrigins(t, mine, "latency_sample", joinerOrigin, peerOrigin)

	// The peer holds the joiner's migrated rows too, so the migration really did
	// export rather than merely write rows nobody else can see.
	assertHoldsBothOrigins(t, theirs, "availability_event", peerOrigin, joinerOrigin)
	assertHoldsBothOrigins(t, theirs, "latency_sample", peerOrigin, joinerOrigin)

	// A display read on the joiner sees both machines' windows together, which
	// is the whole point of the tables over two kv documents.
	h, err := LoadHistory(Deps{History: mine, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("LoadHistory on the joiner: %v", err)
	}
	if len(h.Events) != 2 {
		t.Errorf("the joiner's display history has %d events, want both machines': %+v", len(h.Events), h.Events)
	}

	assertNoPeerEcho(t, log, mine, joinerOrigin, peerOrigin)
}

// assertNoPeerEcho fails when the joiner's head names a history row the peer
// owns. The joiner holds rows it does not own and owns rows of its own, so an
// entry for the peer's row in the joiner's head would travel it back to the
// machine that wrote it and be applied there a second time -- the echo the
// exporter's `owned` scope exists to prevent.
func assertNoPeerEcho(t *testing.T, log *synclog.MemTransport, mine *db.DB, own, peer string) {
	t.Helper()
	head, err := log.Head(own)
	if err != nil {
		t.Fatalf("read the joiner's head: %v", err)
	}

	peersRows := map[string]bool{}
	for _, id := range rowsByOrigin(t, mine, "availability_event")[peer] {
		peersRows[id] = true
	}
	for _, id := range rowsByOrigin(t, mine, "latency_sample")[peer] {
		peersRows[id] = true
	}

	for _, entry := range head {
		if entry.Table != "availability_event" && entry.Table != "latency_sample" {
			continue
		}
		if id := historyRowID(entry.PK); peersRows[id] {
			t.Errorf("the joiner's head names %s %s, which %s owns", entry.Table, id, peer)
		}
	}
}

// historyRowID is the id out of the json_array text an entry's PK holds. The
// tables' primary key is one component, so the first element is the row's id.
func historyRowID(pk string) string {
	var parts []string
	if err := json.Unmarshal([]byte(pk), &parts); err != nil || len(parts) == 0 {
		return pk
	}
	return parts[0]
}
