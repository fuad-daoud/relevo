package availability

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The one-time move of the two kv documents into the tables that replaced them.
// What the round claims is narrow and testable: the documents' contents land as
// rows under this machine's own origin with fresh identities, the documents are
// gone afterwards, and a second pass moves nothing and reads nothing.

// migratedMachine is one installation's pair: the machine-local file the
// documents live in and the shared file the rows go into.
func migratedMachine(t *testing.T, origin string) (local db.KV, shared *db.DB) {
	t.Helper()
	shared, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("open the shared file: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	localDB, err := db.OpenWith(filepath.Join(t.TempDir(), "local.db"), db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("open the local file: %v", err)
	}
	t.Cleanup(func() { _ = localDB.Close() })
	return localDB, shared
}

// seedDocuments writes the two retired documents as the code before the tables
// wrote them: one rewritten json document per history, in the local file.
func seedDocuments(t *testing.T, local db.KV, events []Event, samples []Sample) {
	t.Helper()
	eventsJSON, err := json.Marshal(History{Events: events})
	if err != nil {
		t.Fatalf("marshal the availability document: %v", err)
	}
	if err := local.KVPut(availabilityKey, eventsJSON); err != nil {
		t.Fatalf("write the availability document: %v", err)
	}
	samplesJSON, err := json.Marshal(LatencyHistory{Samples: samples})
	if err != nil {
		t.Fatalf("marshal the latency document: %v", err)
	}
	if err := local.KVPut(latencyKey, samplesJSON); err != nil {
		t.Fatalf("write the latency document: %v", err)
	}
}

// TestHistoryMigrationMovesKvOnce is Round 3 step 3: the documents move into
// their tables once, under this machine's own origin, and a second pass is a
// no-op.
//
// It fails without the marker (a second pass reads documents that are already
// deleted and either errors or writes a second copy of the window) and without
// the delete (the documents are still readable after the move, so the retired
// rows are still live and can still diverge from the rows).
func TestHistoryMigrationMovesKvOnce(t *testing.T) {
	t.Parallel()

	local, shared := migratedMachine(t, "m1")

	// Deliberately out of order in the document: the migration sorts by at, so
	// the rows come back in the sequence a reader expects rather than the
	// sequence the json happened to hold.
	events := []Event{
		{At: baseTime.Add(-2 * time.Hour), Kind: SpawnFailed, Provider: "test", Token: testClaudeRef, Source: "relevo", Binding: "b"},
		{At: baseTime, Kind: Cleared, Provider: "test", Source: "planner", Since: baseTime.Add(-3 * time.Hour)},
	}
	samples := []Sample{
		{At: baseTime.Add(-time.Hour), Token: testClaudeRef, Host: "laptop", TTFTMS: 640, TotalMS: 900},
	}
	seedDocuments(t, local, events, samples)

	moved, err := MigrateHistoryDocuments(local, shared, shared.Origin())
	if err != nil {
		t.Fatalf("MigrateHistoryDocuments: %v", err)
	}
	if moved.Events != 2 || moved.Samples != 1 {
		t.Fatalf("moved = %+v, want 2 events and 1 sample", moved)
	}

	assertMigratedRows(t, shared, events, samples)
	assertDocumentsGone(t, local)

	// A second pass moves nothing: the marker says the move happened, so the
	// deleted documents are never read again and the rows are not duplicated.
	again, err := MigrateHistoryDocuments(local, shared, shared.Origin())
	if err != nil {
		t.Fatalf("second MigrateHistoryDocuments: %v", err)
	}
	if again != (MigratedHistory{}) {
		t.Errorf("the second pass moved %+v, want nothing", again)
	}
	if len(eventRows(t, shared)) != 2 || len(sampleRows(t, shared)) != 1 {
		t.Errorf("rows after the second pass = %v / %v, want the same two and one",
			eventRows(t, shared), sampleRows(t, shared))
	}
}

// The marker is what stops a later pass importing a document written after the
// move. Without it the pass reads whatever the key holds every time it runs, so
// a machine whose window is kept by some tool this code does not know about
// would have it imported on every start -- duplicating the rows already in the
// tables on each one.
func TestHistoryMigrationDoesNotRereadAfterTheMarker(t *testing.T) {
	t.Parallel()

	local, shared := migratedMachine(t, "m1")
	seedDocuments(t, local,
		[]Event{{At: baseTime, Kind: RateLimited, Provider: "test", Source: "planner"}},
		[]Sample{{At: baseTime, Token: testClaudeRef, Host: "laptop", TTFTMS: 10, TotalMS: 20}})

	if _, err := MigrateHistoryDocuments(local, shared, shared.Origin()); err != nil {
		t.Fatalf("first MigrateHistoryDocuments: %v", err)
	}
	beforeEvents := len(eventRows(t, shared))
	beforeSamples := len(sampleRows(t, shared))

	// A document reappears at the retired key, out of band. The tables are the
	// only place the window lives, so nothing should read it again.
	seedDocuments(t, local,
		[]Event{{At: baseTime, Kind: Cleared, Provider: "test", Source: "relevo"}},
		[]Sample{{At: baseTime, Token: "other/token", Host: "laptop", TTFTMS: 99, TotalMS: 100}})

	again, err := MigrateHistoryDocuments(local, shared, shared.Origin())
	if err != nil {
		t.Fatalf("second MigrateHistoryDocuments: %v", err)
	}
	if again != (MigratedHistory{}) {
		t.Errorf("the second pass moved %+v, want nothing", again)
	}
	if got, want := len(eventRows(t, shared)), beforeEvents; got != want {
		t.Errorf("availability_event rows = %d, want the %d the first pass wrote", got, want)
	}
	if got, want := len(sampleRows(t, shared)), beforeSamples; got != want {
		t.Errorf("latency_sample rows = %d, want the %d the first pass wrote", got, want)
	}
}

// assertMigratedRows checks that the rows carry the documents' own values under
// this machine's origin, and that the Cleared event's Since survived: it is what
// BlockedDurations computes a block's length from, so a migration that dropped it
// would shorten every block in the panel to nothing.
func assertMigratedRows(t *testing.T, shared *db.DB, events []Event, samples []Sample) {
	t.Helper()

	gotEvents := eventRows(t, shared)
	if len(gotEvents) != 2 {
		t.Fatalf("availability_event rows = %v, want the document's two events", gotEvents)
	}
	want := []string{
		"spawn_failed test m1 " + itoa(db.UnixMS(events[0].At)),
		"cleared test m1 " + itoa(db.UnixMS(events[1].At)),
	}
	for i, w := range want {
		if gotEvents[i] != w {
			t.Errorf("row %d = %q, want %q (the document's own at, under this machine's origin)", i, gotEvents[i], w)
		}
	}
	if got := sampleRows(t, shared); len(got) != 1 || got[0] != samples[0].Token+" m1 "+itoa(db.UnixMS(samples[0].At)) {
		t.Fatalf("latency_sample rows = %v, want the document's one sample under this machine's origin", got)
	}

	rows, err := shared.AvailabilityEvents(0)
	if err != nil {
		t.Fatalf("read the migrated rows: %v", err)
	}
	var cleared db.AvailabilityEvent
	for _, r := range rows {
		if r.Kind == string(Cleared) {
			cleared = r
		}
	}
	if cleared.SinceMS == nil || *cleared.SinceMS != db.UnixMS(events[1].Since) {
		t.Errorf("the cleared row's since_ms = %v, want %d", cleared.SinceMS, db.UnixMS(events[1].Since))
	}
}

// assertDocumentsGone checks that neither retired document survives. Nothing
// reads or writes either key, and a key that did would be a second place the
// window lives -- one that can diverge from the rows.
func assertDocumentsGone(t *testing.T, local db.KV) {
	t.Helper()
	for _, key := range []string{availabilityKey, latencyKey} {
		if _, ok, err := local.KVGet(key); err != nil {
			t.Fatalf("read %s after the move: %v", key, err)
		} else if ok {
			t.Errorf("the %q document is still in the local file", key)
		}
	}
}

// A machine that never recorded anything moves nothing and is not an error: a
// fresh install has no documents, so the pass has no work and still finishes.
func TestHistoryMigrationOnAMachineWithNoDocuments(t *testing.T) {
	t.Parallel()

	local, shared := migratedMachine(t, "m1")

	moved, err := MigrateHistoryDocuments(local, shared, shared.Origin())
	if err != nil {
		t.Fatalf("MigrateHistoryDocuments: %v", err)
	}
	if moved != (MigratedHistory{}) {
		t.Errorf("moved = %+v, want nothing from a machine with no documents", moved)
	}
	if len(eventRows(t, shared)) != 0 || len(sampleRows(t, shared)) != 0 {
		t.Error("a machine with no documents wrote history rows")
	}

	// The marker is still written, so a later pass over a file that gained a
	// document out of band does not silently import it.
	if _, ok, err := local.KVGet(historyRowsMarker); err != nil || !ok {
		t.Errorf("the marker = (_, %t, %v), want it written even with no documents", ok, err)
	}
}

// The migrated rows are ordinary rows: the same INSERT an append point makes,
// under this machine's own origin, so they enter the outbox and travel like any
// other history. That is what makes a migrated window reach a peer rather than
// staying on the machine that recorded it.
func TestMigratedRowsExport(t *testing.T) {
	t.Parallel()

	local, shared := migratedMachine(t, "m1")
	seedDocuments(t, local,
		[]Event{{At: baseTime, Kind: RateLimited, Provider: "test", Source: "planner"}},
		[]Sample{{At: baseTime, Token: testClaudeRef, Host: "laptop", TTFTMS: 10}})

	if _, err := MigrateHistoryDocuments(local, shared, shared.Origin()); err != nil {
		t.Fatalf("MigrateHistoryDocuments: %v", err)
	}

	raw, err := db.OpenRawReadOnly(shared.Path())
	if err != nil {
		t.Fatalf("open the shared file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	outbox, err := raw.Query(`SELECT tbl, op FROM sync_outbox WHERE tbl IN ('availability_event','latency_sample') ORDER BY tbl`)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer func() { _ = outbox.Close() }()

	var got []string
	for outbox.Next() {
		var tbl, op string
		if err := outbox.Scan(&tbl, &op); err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		got = append(got, tbl+" "+op)
	}
	if err := outbox.Err(); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	want := []string{"availability_event insert", "latency_sample insert"}
	if len(got) != len(want) {
		t.Fatalf("outbox entries = %v, want %v: a migrated row that records nothing would never reach a peer", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("outbox entry %d = %q, want %q", i, got[i], w)
		}
	}
}
