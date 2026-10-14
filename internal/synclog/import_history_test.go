package synclog

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The two history tables across the whole exchange: what a write exports, what an
// arrival is refused for, what an age prune says and does not say, and what the
// reconciler leaves alone. Both tables are here in every case, because the rule
// is one rule about a set of tables rather than one table's rule, and a case that
// covered only one of them would pass on a file where the other had been dropped
// from the set.

// historyNow is the instant the arrival window and the age prune are judged
// against in the cases that pin the clock: fixed, so a row's age is a number
// rather than the moment the test happened to run.
var historyNow = time.Unix(1757000000, 0).UTC()

// cutoffMS is where the retention window ends at historyNow, and the two values
// either side of it are what the cases write: a row one millisecond inside the
// window and one millisecond outside it, so a case can sit either side of the
// boundary without doing arithmetic where the boundary is.
var (
	cutoffMS        = db.UnixMS(historyNow.Add(-db.HistoryWindow))
	insideWindowMS  = cutoffMS + 1
	outsideWindowMS = cutoffMS - 1
)

// recentMS is the at_ms of a row a day old by the wall clock, for the case that
// lets the importer keep the clock it ships with rather than pinning one.
func recentMS() int64 { return db.UnixMS(time.Now().Add(-24 * time.Hour)) }

// historyRows are the two tables with the columns a case writes, in the order
// the cases name them.
var historyRows = []string{"availability_event", "latency_sample"}

// insertHistoryRow writes one row of one of the two tables, owned by origin.
// Everything a case needs is named by the arguments, so the same statement
// builds a row this machine wrote and a row a peer wrote.
func insertHistoryRow(tbl, id, origin string, atMS int64) string {
	switch tbl {
	case "availability_event":
		return fmt.Sprintf(`INSERT INTO availability_event (id, at_ms, kind, provider, source, origin)
			VALUES ('%s', %d, 'rate_limited', 'test', 'relevo', '%s')`, id, atMS, origin)
	case "latency_sample":
		return fmt.Sprintf(`INSERT INTO latency_sample (id, at_ms, token, host, ttft_ms, total_ms, origin)
			VALUES ('%s', %d, 'opencode/test/m', 'laptop', 120, 900, '%s')`, id, atMS, origin)
	default:
		return fmt.Sprintf(`INSERT INTO %s DEFAULT VALUES`, tbl)
	}
}

// historyBody is one row's values as the exchange carries them: every column of
// the table, so an importer writes the row rather than a partial one. The origin
// is a parameter, because one case needs a body that claims an owner it is not.
func historyBody(tbl, id string, atMS int64, origin string) map[string]any {
	switch tbl {
	case "availability_event":
		return map[string]any{
			"id": id, "at_ms": atMS, "kind": "rate_limited", "provider": "test",
			"account": "", "token": "", "source": "relevo", "binding": "", "note": "", "origin": origin,
		}
	default:
		return map[string]any{
			"id": id, "at_ms": atMS, "token": "opencode/test/m", "host": "laptop",
			"ttft_ms": 120, "total_ms": 900, "err": "", "origin": origin,
		}
	}
}

// fixedImporter is an importer whose arrival window sits at historyNow.
func fixedImporter(d *db.DB, t LogTransport) *Importer {
	i := NewImporter(d, t)
	i.now = func() time.Time { return historyNow }
	return i
}

// fixedReconciler is a reconciler whose window sits at historyNow.
func fixedReconciler(d *db.DB, t LogTransport) *Reconciler {
	r := NewReconciler(d, t)
	r.now = func() time.Time { return historyNow }
	return r
}

// Who an entry is allowed to write, for the two append-only tables. There is no
// parent to resolve through, so the row's own origin column is the whole of the
// owner question, and the two ways an entry can get it wrong are the two cases
// here: a row the entry's origin does not own, and a body that claims an owner
// the entry's origin is not.
func TestImporterRefusesACrossOriginHistoryRow(t *testing.T) {
	t.Parallel()
	for _, tbl := range historyRows {
		t.Run(tbl, func(t *testing.T) {
			t.Parallel()
			t.Run("an upsert naming another origin's row is dropped", func(t *testing.T) {
				t.Parallel()
				refusesAnUpsertOfAnotherOriginsRow(t, tbl)
			})
			t.Run("the body's origin column never decides", func(t *testing.T) {
				t.Parallel()
				ignoresTheOriginTheBodyClaims(t, tbl)
			})
			t.Run("a delete is refused even for a row the origin owns", func(t *testing.T) {
				t.Parallel()
				refusesAHistoryDelete(t, tbl)
			})
		})
	}
}

// refusesAnUpsertOfAnotherOriginsRow is the cross-origin upsert: the entry names
// a row this file resolves to m2, so it is dropped and moved past, and the row
// keeps the owner this file resolved.
func refusesAnUpsertOfAnotherOriginsRow(t *testing.T, tbl string) {
	t.Helper()
	peer, path := peerFile(t, "m2")
	seed(t, path, insertHistoryRow(tbl, "01A", "m2", insideWindowMS))
	log := NewMemTransport("m1")
	entryFor(t, log, Entry{
		Origin: "m1", Table: tbl, PK: `["01A"]`, Op: OpUpsert,
		SchemaVersion: knownVersion(t, peer),
		Body:          bodyFor(t, historyBody(tbl, "01A", insideWindowMS, "m1")),
	})

	got, err := fixedImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import an upsert for a row m2 owns: %v", err)
	}
	if got.Applied != 0 || len(got.Dropped) != 1 || got.Dropped[0].Origin != "m1" {
		t.Fatalf("import = %+v, want the cross-origin upsert dropped", got)
	}
	if owner := historyOrigin(t, peer, tbl, "01A"); owner != "m2" {
		t.Fatalf("the row's origin = %q, want the owner this file resolves", owner)
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 1 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the mark moved past the refused batch", seq, found, err)
	}
}

// ignoresTheOriginTheBodyClaims is the other half of the owner rule: the owner
// column a body carries is a claim, not a fact, so the row lands under the
// entry's own origin whatever the body said.
func ignoresTheOriginTheBodyClaims(t *testing.T, tbl string) {
	t.Helper()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	entryFor(t, log, Entry{
		Origin: "m1", Table: tbl, PK: `["09Z"]`, Op: OpUpsert,
		SchemaVersion: knownVersion(t, peer),
		Body:          bodyFor(t, historyBody(tbl, "09Z", insideWindowMS, "m2")),
	})

	got, err := fixedImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import an upsert whose body claims another origin: %v", err)
	}
	if got.Applied != 1 || len(got.Dropped) != 0 {
		t.Fatalf("import = %+v, want the row placed under the entry's own origin", got)
	}
	if owner := historyOrigin(t, peer, tbl, "09Z"); owner != "m1" {
		t.Fatalf("the row's origin = %q, want the entry's origin, not the one the body claimed", owner)
	}
}

// refusesAHistoryDelete is the third case: a row the entry's origin does own is
// still not deletable, because the only delete these tables ever perform is the
// age prune and that one is never recorded.
func refusesAHistoryDelete(t *testing.T, tbl string) {
	t.Helper()
	peer, path := peerFile(t, "m2")
	seed(t, path, insertHistoryRow(tbl, "01B", "m1", insideWindowMS))
	log := NewMemTransport("m1")
	entryFor(t, log, Entry{
		Origin: "m1", Table: tbl, PK: `["01B"]`, Op: OpDelete,
		SchemaVersion: knownVersion(t, peer),
	})

	got, err := fixedImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a delete of a history row: %v", err)
	}
	if got.Applied != 0 || len(got.Dropped) != 1 {
		t.Fatalf("import = %+v, want the history delete refused", got)
	}
	if owner := historyOrigin(t, peer, tbl, "01B"); owner != "m1" {
		t.Fatalf("the row's origin = %q, want the row still standing", owner)
	}
}

// An arriving row already older than the window is not written, and nothing about
// it is left behind that would export it again: a joiner that was away for a
// month must not import a peer's whole window and then carry it straight back
// out. The mark still moves, so the batch is not offered a second time, and a row
// inside the window in the same batch is written as usual.
func TestOldHistoryRowImportsWithoutReexport(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	for _, tbl := range historyRows {
		entryFor(t, log, Entry{
			Origin: "m1", Table: tbl, PK: `["old"]`, Op: OpUpsert,
			SchemaVersion: knownVersion(t, peer),
			Body:          bodyFor(t, historyBody(tbl, "old", outsideWindowMS, "m1")),
		})
		entryFor(t, log, Entry{
			Origin: "m1", Table: tbl, PK: `["fresh"]`, Op: OpUpsert,
			SchemaVersion: knownVersion(t, peer),
			Body:          bodyFor(t, historyBody(tbl, "fresh", insideWindowMS, "m1")),
		})
	}

	got, err := fixedImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a batch carrying an aged row: %v", err)
	}
	if got.Applied != 4 {
		t.Fatalf("import = %+v, want every entry read, the two aged ones skipped and the two fresh ones written", got)
	}
	if len(got.Dropped) != 0 {
		t.Fatalf("import = %+v, want an aged row skipped rather than refused", got)
	}
	for _, tbl := range historyRows {
		if held(t, peer, tbl, "old") {
			t.Errorf("%s holds the aged row, want it skipped", tbl)
		}
		if owner := historyOrigin(t, peer, tbl, "fresh"); owner != "m1" {
			t.Errorf("%s fresh row's origin = %q, want it applied", tbl, owner)
		}
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 4 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the mark moved over the skipped row too", seq, found, err)
	}
	// The rows this machine did apply are the peer's, so the exporter's own
	// owner filter drops them and a round exports nothing: a machine never
	// carries an arrival back out, whether the arrival was written or skipped.
	r := &recording{MemTransport: NewMemTransport("m2")}
	if res, err := exporter(peer, r).Export(); err != nil || res.Appended != 0 {
		t.Fatalf("export = (%+v, %v), want nothing exported back", res, err)
	}
	if shape := r.shape(); shape != "" {
		t.Fatalf("appended = %q, want a peer's rows never carried back", shape)
	}
}

// The age prune exports nothing. The tables carry an insert trigger alone, so a
// delete by age reaches the outbox through nothing: the row leaves this machine's
// file and no machine is asked to reproduce the removal.
func TestHistoryPruneExportsNothing(t *testing.T) {
	t.Parallel()
	mine, path := exporterFile(t, "m1")
	seed(t, path,
		insertHistoryRow("availability_event", "aged-e", "m1", outsideWindowMS),
		insertHistoryRow("latency_sample", "aged-s", "m1", outsideWindowMS),
		insertHistoryRow("availability_event", "live-e", "m1", insideWindowMS),
		insertHistoryRow("latency_sample", "live-s", "m1", insideWindowMS),
	)

	r := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(mine, r).Export(); err != nil {
		t.Fatalf("export the rows the file starts with: %v", err)
	}
	if queued := outboxRows(t, mine); queued != "" {
		t.Fatalf("outbox after the first export = %q, want it drained", queued)
	}

	pruned, err := mine.PruneHistory(cutoffMS)
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if pruned.Events != 1 || pruned.Samples != 1 {
		t.Fatalf("pruned = %+v, want one row of each table", pruned)
	}
	if queued := outboxRows(t, mine); queued != "" {
		t.Fatalf("outbox after the prune = %q, want the age delete recorded nowhere", queued)
	}
	if held(t, mine, "availability_event", "aged-e") {
		t.Error("the aged availability event is still present")
	}
	if held(t, mine, "latency_sample", "aged-s") {
		t.Error("the aged latency sample is still present")
	}

	// The prune is the only thing that has changed the file since the last
	// export, so a fresh transport is enough to ask whether it left anything
	// behind to say.
	after := &recording{MemTransport: NewMemTransport("m1")}
	got, err := exporter(mine, after).Export()
	if err != nil {
		t.Fatalf("export after the prune: %v", err)
	}
	if got.Appended != 0 || after.shape() != "" {
		t.Fatalf("export = %+v, appended %q, want the prune exported nothing", got, after.shape())
	}
	// The rows still inside the window are untouched: a prune is a comparison
	// against one column, and it must not take the rest of the table with it.
	if !held(t, mine, "availability_event", "live-e") {
		t.Error("the live availability event was pruned")
	}
	if !held(t, mine, "latency_sample", "live-s") {
		t.Error("the live latency sample was pruned")
	}
}

// Reconcile proposes nothing for a history row that has aged out, on either side
// of the comparison. A row the file still holds is not one the log is missing, so
// it is not re-appended; a head row the file no longer holds is one that aged out
// here or on the machine that wrote it, so it is not deleted. Either entry would
// be a thirty-day-old observation travelling between machines forever, and the
// delete would land on a table every importer refuses a delete of.
func TestReconcileIgnoresAgedHistoryRows(t *testing.T) {
	t.Parallel()

	t.Run("a row head names and the file holds is not re-appended", func(t *testing.T) {
		t.Parallel()
		mine, path := exporterFile(t, "m1")
		seed(t, path,
			insertHistoryRow("availability_event", "aged-e", "m1", outsideWindowMS),
			insertHistoryRow("latency_sample", "aged-s", "m1", outsideWindowMS),
		)
		r := &recording{MemTransport: NewMemTransport("m1")}
		got, err := fixedReconciler(mine, r).Reconcile()
		if err != nil {
			t.Fatalf("reconcile a file of aged history rows against an empty head: %v", err)
		}
		if got.Upserts != 0 || got.Deletes != 0 || r.shape() != "" {
			t.Fatalf("reconcile = %+v, appended %q, want the aged rows left alone", got, r.shape())
		}
	})

	t.Run("a head row the file does not hold is not deleted", func(t *testing.T) {
		t.Parallel()
		mine, _ := exporterFile(t, "m1")
		log := NewMemTransport("m1")
		handle := log.OnLog("m1")
		for _, tbl := range historyRows {
			entry, err := NewUpsert("m1", tbl, `["gone"]`, knownVersion(t, mine),
				bodyFor(t, historyBody(tbl, "gone", outsideWindowMS, "m1")), syncClock())
			if err != nil {
				t.Fatalf("build the head entry for %s: %v", tbl, err)
			}
			if _, err := handle.Append([]Entry{entry}); err != nil {
				t.Fatalf("put %s in head: %v", tbl, err)
			}
		}

		r := &recording{MemTransport: handle}
		got, err := fixedReconciler(mine, r).Reconcile()
		if err != nil {
			t.Fatalf("reconcile against a head of aged history rows: %v", err)
		}
		if got.Deletes != 0 || got.Upserts != 0 || r.shape() != "" {
			t.Fatalf("reconcile = %+v, appended %q, want no age delete proposed", got, r.shape())
		}
	})

	t.Run("a row inside the window is still compared", func(t *testing.T) {
		t.Parallel()
		mine, path := exporterFile(t, "m1")
		seed(t, path, insertHistoryRow("availability_event", "live-e", "m1", insideWindowMS))
		r := &recording{MemTransport: NewMemTransport("m1")}
		got, err := fixedReconciler(mine, r).Reconcile()
		if err != nil {
			t.Fatalf("reconcile a file holding a live history row: %v", err)
		}
		if got.Upserts != 1 || r.shape() != `availability_event ["live-e"] upsert` {
			t.Fatalf("reconcile = %+v, appended %q, want the row inside the window proposed", got, r.shape())
		}
	})
}

// What a write to either table does across a real two-machine round: the row
// exports from the machine that wrote it, arrives on the peer, and is not carried
// back. Nothing here is the importer being handed a batch by hand -- it is the
// exporter, the importer and the reconciler running in the order they run in.
func TestHistoryRowsTravelBetweenMachines(t *testing.T) {
	t.Parallel()
	first, firstPath := exporterFile(t, "m1")
	second, _ := exporterFile(t, "m2")
	seed(t, firstPath,
		insertHistoryRow("availability_event", "e1", "m1", recentMS()),
		insertHistoryRow("latency_sample", "s1", "m1", recentMS()),
	)
	log := NewMemTransport("m1")

	crossed, _ := settle(t, log, first, second)
	if !crossed["m2"] {
		t.Fatalf("m2 applied nothing of m1's, want it holding m1's history rows")
	}
	for tbl, id := range historyRowIDs {
		if owner := historyOrigin(t, second, tbl, id); owner != "m1" {
			t.Errorf("%s on m2 has origin %q, want the machine that wrote it", tbl, owner)
		}
	}
	// The echo check: m2 holds rows of m1's origin, and m1 holds nothing but its
	// own. A machine that exported what it imported would carry the pair's whole
	// history back and forth forever.
	for tbl, id := range historyRowIDs {
		if owner := historyOrigin(t, first, tbl, id); owner != "m1" {
			t.Errorf("%s on m1 has origin %q, want its own row and no echo of m2's", tbl, owner)
		}
	}
	if have := sharedRows(t, first); have != `availability_event ["e1"]|latency_sample ["s1"]` {
		t.Errorf("m1 holds %q, want its own two rows and nothing of m2's", have)
	}
}

// historyRowIDs names the row each table's travelling case writes and looks for,
// so a case can cover both tables without repeating the name at every assertion.
var historyRowIDs = map[string]string{"availability_event": "e1", "latency_sample": "s1"}

// historyOrigin is the origin column of one row of one table, or "" when the
// file does not hold the row at all. A read rather than a count, so a case says
// which machine a row belongs to rather than only that a row exists.
func historyOrigin(t *testing.T, d *db.DB, tbl, id string) string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	var owner string
	query := fmt.Sprintf(`SELECT origin FROM %q WHERE id = ?`, tbl)
	if err := raw.QueryRow(query, id).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ""
		}
		t.Fatalf("read %s %s: %v", tbl, id, err)
	}
	return owner
}

// held reports whether the file holds one row of one table.
func held(t *testing.T, d *db.DB, tbl, id string) bool {
	t.Helper()
	return historyOrigin(t, d, tbl, id) != ""
}
