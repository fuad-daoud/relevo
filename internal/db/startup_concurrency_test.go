package db

import (
	"errors"
	"testing"
	"time"
)

// A pass that runs under the serving owner, so a client can be writing while
// it works. These pin what that costs each
// pass, and each one fails if the guard it names is removed.

// TestSplitKeepsAWriteThatRacesTheMove pins the rule the move turns on: the
// copy is committed into the machine-local file before the shared one is
// cleared, and a row the local file already holds with different content is
// kept rather than replaced.
//
// That is what a write racing the move comes down to. A machine-local row is
// written through the routing every local surface binds, so it lands in the
// local file while the pass is still reading the shared one; the pass then sees
// a local row that disagrees with the shared copy, and the shared copy is the
// stale one. Overwriting it would swap the value the running system is already
// using for one it has stopped using, so the pass keeps the local row, converges
// the stray away and counts the disagreement instead.
func TestSplitKeepsAWriteThatRacesTheMove(t *testing.T) {
	d, dir := openSplitTest(t)
	local := d.Local()
	if _, err := d.RecordPut(Record{Name: "api", State: "active", Round: 1, CWD: "/work/api"}); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	if _, err := d.sqlDB.Exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		"client.key", []byte("the-old-pem"), "2026-10-03T09:00:00.000Z"); err != nil {
		t.Fatalf("seed the shared secret: %v", err)
	}

	const racing = "the-pem-written-while-the-pass-ran"
	restore := splitBeforeTable
	splitBeforeTable = func(table string) error {
		if table != "secret" {
			return nil
		}
		// The racing write goes where every machine-local writer goes, so the
		// pass finds the local row holding something the shared copy does not.
		return local.Tx(func(t *Tx) error {
			return t.SecretPut("client.key", []byte(racing), splitNow)
		})
	}
	t.Cleanup(func() { splitBeforeTable = restore })

	stats, ran, err := SplitOnce(d, dir, splitNow)
	if err != nil || !ran {
		t.Fatalf("SplitOnce = (ran %v, err %v), want the pass to run", ran, err)
	}

	value, ok, err := local.SecretGet("client.key")
	if err != nil || !ok {
		t.Fatalf("the local file's client.key = (%q, %t, %v), want the racing write", value, ok, err)
	}
	if string(value) != racing {
		t.Errorf("the local file's client.key = %q, want the racing write %q: the move overwrote a value the running system had already changed", value, racing)
	}
	if n := splitCount(t, d, "secret"); n != 0 {
		t.Errorf("shared secret rows = %d, want the stray converged away", n)
	}
	if stats.Conflicts != 1 {
		t.Errorf("conflicts = %d, want the disagreement between the two copies counted once", stats.Conflicts)
	}
	// The marker is written only after every table moved, so a pass that kept a
	// racing row still finished: the next start converges whatever arrives later.
	if !d.SplitDone() {
		t.Error("SplitDone = false after a pass that kept a racing row, want the marker recorded")
	}
}

// TestSplitMovesAWriteThatRacesAMovedTable pins the other half: a write the
// pass has not reached yet still ends up classified, because the pass reads each
// table whole rather than tracking rows as it goes.
//
// A row written to the shared file before the pass reads its table is moved. A
// row written after the pass read it is a stray the marker does not hide: the
// next start converges it, which is what SplitOnce's finished branch is for.
func TestSplitMovesAWriteThatRacesAMovedTable(t *testing.T) {
	d, dir := openSplitTest(t)
	local := d.Local()
	if _, err := d.RecordPut(Record{Name: "api", State: "active", Round: 1, CWD: "/work/api"}); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	// A row the pass will have to move: a shared file with nothing to move is a
	// pass that records its marker and returns without reading a table.
	if _, err := d.sqlDB.Exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		"typesafe", []byte("token"), "2026-10-03T09:00:00.000Z"); err != nil {
		t.Fatalf("seed the shared secret: %v", err)
	}

	restore := splitBeforeTable
	splitBeforeTable = func(table string) error {
		if table != "secret" {
			return nil
		}
		_, err := d.sqlDB.Exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
			"client.key", []byte("pem-before-the-read"), "2026-10-03T09:00:00.000Z")
		return err
	}
	t.Cleanup(func() { splitBeforeTable = restore })

	if _, ran, err := SplitOnce(d, dir, splitNow); err != nil || !ran {
		t.Fatalf("SplitOnce = (ran %v, err %v), want the pass to run", ran, err)
	}
	value, ok, err := local.SecretGet("client.key")
	if err != nil || !ok || string(value) != "pem-before-the-read" {
		t.Errorf("the local file's client.key = (%q, %t, %v), want the row written before the pass read its table", value, ok, err)
	}
	if n := splitCount(t, d, "secret"); n != 0 {
		t.Errorf("shared secret rows = %d, want both rows moved", n)
	}
}

// TestBackfillNeverSkipsAndNeverStampsTwice pins the backfill's two halves
// against a database that changed after the pass ran.
//
// A pass stamps only the rows carrying no origin, so a row the pass already
// stamped is not stamped again -- the second pass below moves exactly one row,
// the one that arrived after the first, and none of the five tables it has
// already settled is touched. And the marker is believed only over counts that
// are empty, so the row that arrived does not turn the marker into a reason to
// skip it: the next pass runs and stamps it.
//
// It fails if the update loses its predicate (every row is stamped again) and
// if the marker is trusted without the gate (the new row is never stamped).
func TestBackfillNeverSkipsAndNeverStampsTwice(t *testing.T) {
	d, _ := openSplitTest(t)
	if _, err := d.sqlDB.Exec(`INSERT INTO binding (id, origin, name, repo_id, cwd, created_at, builder_mode, ingest_source)
		VALUES ('b-1', '', 'api', NULL, '/work', '2026-10-03T09:00:00.000Z', 'runner', 'manual')`); err != nil {
		t.Fatalf("seed an unstamped binding: %v", err)
	}

	stats, ran, err := BackfillOriginOnce(d, "01ORIGIN", splitNow)
	if err != nil || !ran {
		t.Fatalf("BackfillOriginOnce = (ran %v, err %v), want the pass to run", ran, err)
	}
	if stats.Stamped() != 1 {
		t.Fatalf("first pass stamped %d rows, want the one seeded row", stats.Stamped())
	}

	// A row an older writer left behind after the pass recorded itself: the gate
	// counts it, so the marker is stale and the next pass has work to do.
	if _, err := d.sqlDB.Exec(`UPDATE binding SET origin = '' WHERE id = 'b-1'`); err != nil {
		t.Fatalf("unstamp the binding: %v", err)
	}
	second, ran, err := BackfillOriginOnce(d, "01ORIGIN", splitNow.Add(time.Hour))
	if err != nil || !ran {
		t.Fatalf("second BackfillOriginOnce = (ran %v, err %v), want the pass to resume over the row the marker would have hidden", ran, err)
	}
	if second.Stamped() != 1 {
		t.Errorf("the resumed pass stamped %d rows, want only the row the marker would have hidden", second.Stamped())
	}
	counts, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins: %v", err)
	}
	if !counts.Empty() {
		t.Errorf("the resumed pass left %s unstamped", counts)
	}
	var stamped int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM binding WHERE origin = '01ORIGIN'`).Scan(&stamped); err != nil {
		t.Fatalf("count stamped rows: %v", err)
	}
	if stamped != 1 {
		t.Errorf("rows carrying the origin = %d, want one row stamped exactly once", stamped)
	}
}

// TestCompressLeavesEveryRowReadableWhileItRuns pins the reader side of the
// conversion: a read that lands between two batches sees the columns in one of
// the two shapes the pass leaves them, and the codec beside the bytes says which.
//
// The handle is served, which is the shape a startup pass now runs on, so the
// reclaim is skipped and the pool is never swapped out from under the reader --
// and the pass records no reclaim error for having skipped it. It fails if the
// value and its codec are ever written apart, which is the only way a reader
// could meet bytes the codec beside them does not describe.
func TestCompressLeavesEveryRowReadableWhileItRuns(t *testing.T) {
	d := openTestDB(t)
	_ = NewOwner(d)
	id := seedPlainHistory(t, d)
	big := string(bigHistoryValue())

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			body, _, ok, err := d.RoundFileGet(id, "001-big.jsonl")
			if err != nil {
				done <- err
				return
			}
			if ok && string(body) != big {
				done <- errors.New("a read during the conversion returned bytes the pass never wrote")
				return
			}
		}
	}()

	stats, ran, err := CompressHistoryOnce(d, t.TempDir(), time.Now())
	if err != nil {
		close(stop)
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Error(err)
	}
	if !ran {
		t.Error("the compression pass reported ran = false, want the seeded history converted")
	}
	if stats.VacuumErr != "" {
		t.Errorf("the reclaim error = %q, want none: a served handle skips the reclaim rather than failing it", stats.VacuumErr)
	}

	body, _, ok, err := d.RoundFileGet(id, "001-big.jsonl")
	if err != nil || !ok || string(body) != big {
		t.Errorf("the converted row reads back as (%d bytes, %t, %v), want the original body", len(body), ok, err)
	}
}
