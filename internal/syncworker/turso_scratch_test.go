package syncworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestScratchRemoteExportPullRoundTrip is the one test that reaches a real
// remote. It is skipped unless both scratch variables are set, because CI has
// no network and no Turso, and it is the only place the real driver runs: the
// rest of the round serves the pipe with a local file and a driver double.
//
// It proves the shape the backend is for: one installation creates the log on
// an empty remote and pushes, a second joins with its own replica, pulls the
// first's entry, appends its own, and the first pulls that back.
func TestScratchRemoteExportPullRoundTrip(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	dir := t.TempDir()
	// A fresh origin per run keeps one run's entries out of the next one's
	// assertions; the log itself accumulates across runs, which is what a real
	// remote does.
	aOrigin := "scratch-a-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	bOrigin := "scratch-b-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	a := openScratch(t, filepath.Join(dir, "a.db"), aOrigin, url, token)
	written, err := a.Append([]Entry{
		upsertEntry(aOrigin, "board", `["scratch"]`, `{"title":"round trip"}`),
	})
	if err != nil {
		t.Fatalf("append to the scratch remote: %v", err)
	}
	if len(written) != 1 || written[0].Seq < 1 {
		t.Fatalf("append = %+v, want one numbered entry", written)
	}

	b := openScratch(t, filepath.Join(dir, "b.db"), bOrigin, url, token)
	pulled, err := b.Pull(nil)
	if err != nil {
		t.Fatalf("pull from the scratch remote: %v", err)
	}
	if !carries(pulled, aOrigin, written[0].Seq) {
		t.Errorf("the other installation pulled %d entries but not %s seq %d",
			len(pulled), aOrigin, written[0].Seq)
	}

	back, err := b.Append([]Entry{
		upsertEntry(bOrigin, "board", `["scratch-b"]`, `{"title":"answered"}`),
	})
	if err != nil {
		t.Fatalf("append back: %v", err)
	}
	pulled, err = a.Pull(nil)
	if err != nil {
		t.Fatalf("pull back: %v", err)
	}
	if !carries(pulled, bOrigin, back[0].Seq) {
		t.Errorf("the first installation pulled %d entries but not %s seq %d",
			len(pulled), bOrigin, back[0].Seq)
	}

	head, err := b.Head(bOrigin, "", 0)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(head) != 1 || head[0].Seq != back[0].Seq {
		t.Errorf("head = %+v, want the row the append wrote", head)
	}
	stats, err := b.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Entries < 2 || stats.Origins < 2 {
		t.Errorf("stats = %+v, want the log to hold both origins' entries", stats)
	}
}

// TestScratchRemoteMovesAHeadRowTwice pins the second write of one row on a
// real sync connection. The second append moves the row's derived latest
// state, which is the path a sync connection's own change capture exercises
// for the log write.
func TestScratchRemoteMovesAHeadRowTwice(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	origin := "scratch-head-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	b := openScratch(t, filepath.Join(t.TempDir(), "h.db"), origin, url, token)

	first, err := b.Append([]Entry{upsertEntry(origin, "board", `["twice"]`, `{"title":"first"}`)})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := b.Append([]Entry{upsertEntry(origin, "board", `["twice"]`, `{"title":"second"}`)})
	if err != nil {
		t.Fatalf("second append of the same row: %v", err)
	}
	head, err := b.Head(origin, "", 0)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(head) != 1 || head[0].Seq != second[0].Seq || head[0].Seq == first[0].Seq {
		t.Errorf("head = %+v, want one row at the second append's seq %d", head, second[0].Seq)
	}
}

// TestScratchRemoteRoundTripsAcrossACheckpoint takes entries both ways through
// a real remote with each replica checkpointed partway, so the WAL bound is
// proven against the engine rather than the fake: a checkpoint must empty the
// WAL, and every append and pull after it must still land.
func TestScratchRemoteRoundTripsAcrossACheckpoint(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	dir := t.TempDir()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	aOrigin, bOrigin := "scratch-ckpt-a-"+stamp, "scratch-ckpt-b-"+stamp
	a := openScratch(t, filepath.Join(dir, "a.db"), aOrigin, url, token)
	b := openScratch(t, filepath.Join(dir, "b.db"), bOrigin, url, token)

	appendOne := func(be *TursoBackend, origin, pk string) Entry {
		t.Helper()
		got, err := be.Append([]Entry{upsertEntry(origin, "board", `["`+pk+`"]`, `{"title":"`+pk+`"}`)})
		if err != nil {
			t.Fatalf("append %s: %v", pk, err)
		}
		return got[0]
	}
	checkpoint := func(be *TursoBackend, name string) {
		t.Helper()
		ctx := t.Context()
		if err := be.driver.Checkpoint(ctx); err != nil {
			t.Fatalf("checkpoint %s: %v", name, err)
		}
		stats, err := be.driver.Stats(ctx)
		if err != nil {
			t.Fatalf("stats %s: %v", name, err)
		}
		if stats.MainWalSize != 0 {
			t.Errorf("%s WAL = %d bytes after a checkpoint, want 0", name, stats.MainWalSize)
		}
	}
	pullFinds := func(be *TursoBackend, origin string, want Entry) {
		t.Helper()
		pulled, err := be.Pull(nil)
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		if !carries(pulled, origin, want.Seq) {
			t.Errorf("pulled %d entries but not %s seq %d", len(pulled), origin, want.Seq)
		}
	}

	first := appendOne(a, aOrigin, "before")
	checkpoint(a, "a")
	pullFinds(b, aOrigin, first)
	pullFinds(b, aOrigin, appendOne(a, aOrigin, "after"))

	answer := appendOne(b, bOrigin, "answer")
	checkpoint(b, "b")
	pullFinds(a, bOrigin, answer)
	pullFinds(b, aOrigin, appendOne(a, aOrigin, "last"))
}

// openScratch opens one installation's backend against the real remote through
// the production driver.
func openScratch(t *testing.T, replicaPath, origin, url, token string) *TursoBackend {
	t.Helper()
	b := NewTursoBackend(newTursoDriver(replicaPath))
	if err := b.Open(Spec{
		Version: ProtocolVersion,
		Origin:  origin,
		URL:     url,
		Token:   token,
	}); err != nil {
		t.Fatalf("open %s against the scratch remote: %v", origin, err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// TestScratchRemoteMACSurvivesThePipeAndTheLog signs an entry whose body is not
// compact and whose time is neither UTC nor whole-nanosecond-free, and takes it
// all the way round a real remote: over the pipe, through the JSON encoding, into
// the log, back out on a pull. The signature has to verify on what comes back.
//
// It is the one place the two normalisations -- the compacted body and the UTC
// time -- are checked against a real engine rather than against the fake, because
// both are decisions this build makes about how the bytes are stored and only a
// real store can disagree with them.
func TestScratchRemoteMACSurvivesThePipeAndTheLog(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	origin := "scratch-mac-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	b := openScratch(t, filepath.Join(t.TempDir(), "m.db"), origin, url, token)

	key := []byte("the passphrase this machine stretched")
	// A body written with whitespace and a key in an order of its own, at a time
	// carrying a zone and a nanosecond -- neither of which is how the log stores
	// either.
	e, err := synclog.NewUpsert(origin, "board", `["mac"]`, 3,
		json.RawMessage("{\n  \"title\": \"signed\",\n  \"n\": 1\n}"),
		time.Date(2026, 10, 8, 9, 30, 0, 123456789, time.FixedZone("an hour out", 3600)))
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	e.MAC = synclog.EntryMAC(key, e)

	// Through the pipe's own encoding. The MAC travels as base64 and the body
	// as a raw message, so the round trip is where a signature stops verifying
	// if either is carried as anything but bytes.
	line, err := json.Marshal([]Entry{entryToWire(e)})
	if err != nil {
		t.Fatalf("marshal the entry: %v", err)
	}
	var onWire []Entry
	if err := json.Unmarshal(line, &onWire); err != nil {
		t.Fatalf("decode the entry: %v", err)
	}

	written, err := b.AppendSigned(onWire, boundaryAt(key, origin, 1))
	if err != nil {
		t.Fatalf("append to the scratch remote: %v", err)
	}
	if len(written) != 1 || len(written[0].MAC) == 0 {
		t.Fatalf("append = %+v, want one entry carrying its MAC", written)
	}

	// A pull returns only other origins' entries, so a second installation
	// reads it back.
	reader := openScratch(t, filepath.Join(t.TempDir(), "r.db"), origin+"-reader", url, token)
	pulled, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull from the scratch remote: %v", err)
	}
	var found *Entry
	for i := range pulled {
		if pulled[i].Origin == origin && pulled[i].Seq == written[0].Seq {
			found = &pulled[i]
		}
	}
	if found == nil {
		t.Fatalf("the pull of %d entries did not carry %s seq %d back", len(pulled), origin, written[0].Seq)
	}
	back := entryFromWire(*found)
	if !synclog.VerifyMAC(key, back) {
		t.Errorf("the entry that came back does not verify: %+v", back)
	}
}

// entryToWire renders one daemon-side entry as the wire carries it, so the test
// signs with synclog's own function and appends the worker's own type.
func entryToWire(e synclog.Entry) Entry {
	return Entry{
		Origin:        e.Origin,
		Tbl:           e.Table,
		PK:            e.PK,
		Op:            string(e.Op),
		SchemaVersion: e.SchemaVersion,
		Body:          e.Body,
		At:            e.At,
		MAC:           e.MAC,
	}
}

// entryFromWire is the other direction, for the entry a pull brought back.
func entryFromWire(e Entry) synclog.Entry {
	return synclog.Entry{
		Origin:        e.Origin,
		Seq:           e.Seq,
		Batch:         e.Batch,
		Table:         e.Tbl,
		PK:            e.PK,
		Op:            synclog.Op(e.Op),
		SchemaVersion: e.SchemaVersion,
		Body:          e.Body,
		At:            e.At,
		MAC:           e.MAC,
	}
}

// carries reports whether an origin's entry at seq came back from a pull.
func carries(entries []Entry, origin string, seq int) bool {
	for _, e := range entries {
		if e.Origin == origin && e.Seq == seq {
			return true
		}
	}
	return false
}

// formatFourSeedStatements is the format-4 remote's own schema: the log as
// format 4 wrote it with the stored head beside it. It is spelled out rather
// than built from createStatements, so the seed builds the same remote on
// every checkout the traffic baseline runs against: a positional index names
// another table there.
var formatFourSeedStatements = []string{
	"CREATE TABLE log (" +
		"origin TEXT NOT NULL, seq INTEGER NOT NULL, batch INTEGER NOT NULL, " +
		"tbl TEXT NOT NULL, pk TEXT NOT NULL, op TEXT NOT NULL, " +
		"schema_version INTEGER NOT NULL, body TEXT, at TEXT NOT NULL, mac BLOB, " +
		"after_origin TEXT, after_seq INTEGER, " +
		"PRIMARY KEY (origin, seq))",
	"CREATE TABLE head (" +
		"origin TEXT NOT NULL, tbl TEXT NOT NULL, pk TEXT NOT NULL, " +
		"seq INTEGER NOT NULL, hash TEXT NOT NULL, " +
		"PRIMARY KEY (origin, tbl, pk))",
	"CREATE TABLE meta (key TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL)",
	createMarksStatement,
}

// recordingDriver wraps the production driver and totals what each push
// moved, from the engine's own counters either side of it. The upgrade push
// runs inside Open, so bracketing it from outside would also catch the
// bootstrap; the wrapper brackets the push itself. A join's pull rides
// inside Open too, so its sent side is read from the engine counters right
// after Open, with Bootstrapped as the received cross-check, and pulls need
// no wrapper: an append only pushes.
type recordingDriver struct {
	*tursoDriver
	pushSent, pushRecv int64
}

// Push runs the push and adds what it moved to the running totals.
func (d *recordingDriver) Push(ctx context.Context) error {
	before, err := d.Stats(ctx)
	if err != nil {
		return d.tursoDriver.Push(ctx)
	}
	if err := d.tursoDriver.Push(ctx); err != nil {
		return err
	}
	// A stats read that fails records nothing: the push already happened,
	// and losing a measurement must not fail it.
	after, err := d.Stats(ctx)
	if err != nil {
		return nil
	}
	sent, recv := bytesDelta(before, after)
	d.pushSent += sent
	d.pushRecv += recv
	return nil
}

// assertUpgraded states what the upgrade leaves behind: this build's three
// tables with the new format row and every seeded row. The shape assertions
// do not stop the test: where the remote stays at format 4, the traffic
// phases below still measure it.
func assertUpgraded(t *testing.T, b *TursoBackend, rows int) {
	t.Helper()
	ctx := t.Context()
	tables, err := userTables(ctx, b.db)
	if err != nil {
		t.Fatalf("read the upgraded tables: %v", err)
	}
	for _, name := range tables {
		if name == "head" {
			t.Errorf("the upgrade left %v, want no head table: the head is answered from the log", tables)
		}
	}
	if want := []string{"log", "marks", "meta"}; !slices.Equal(tables, want) {
		t.Errorf("the upgraded remote holds %v, want %v", tables, want)
	}
	var format string
	if err := b.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", metaFormatKey).Scan(&format); err != nil {
		t.Fatalf("read the upgraded format: %v", err)
	}
	if format != strconv.Itoa(logFormatVersion) {
		t.Errorf("meta format = %q, want %d after the upgrade", format, logFormatVersion)
	}
	var entries int
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM log").Scan(&entries); err != nil {
		t.Fatalf("read the upgraded log: %v", err)
	}
	if entries != rows {
		t.Errorf("the upgraded log holds %d entries, want the %d it had", entries, rows)
	}
}

// TestScratchFormatFiveUpgradeAndTraffic seeds a format-4 remote, takes it to
// format 5, and measures what the move costs: the one-time upgrade push, a
// second replica's join pull, and per-entry traffic in small and large
// batches. It is skipped unless both scratch variables are set, because CI
// has no network and no Turso, and it runs against the supplied throwaway
// database only, which it resets to format 4 first so every run measures the
// same upgrade.
func TestScratchFormatFiveUpgradeAndTraffic(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	ctx := t.Context()
	dir := t.TempDir()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	origin := "scratch-f5-" + stamp

	// Phase 1: seed a format-4 remote through the production driver in one
	// bulk transaction of small-body log rows plus their head rows and a
	// single push, never one append per row: each append pushes and pulls
	// pages back, which would cost the shared quota hundreds of megabytes.
	const bulkRows = 20000
	seeder := newTursoDriver(filepath.Join(dir, "seed.db"))
	seedDB, err := seeder.Open(ctx, Spec{Version: ProtocolVersion, Origin: origin + "-seed", URL: url, Token: token})
	if err != nil {
		t.Fatalf("open the seeder: %v", err)
	}
	digest, err := bodyHash(json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("hash the bulk body: %v", err)
	}
	tx, err := seedDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the seed transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range append(append([]string{
		"DROP TABLE IF EXISTS head",
		"DROP TABLE IF EXISTS log",
		"DROP TABLE IF EXISTS marks",
		"DROP TABLE IF EXISTS meta",
	}, formatFourSeedStatements...),
		"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '4')") {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed with %q: %v", stmt, err)
		}
	}
	logIns, err := tx.PrepareContext(ctx, "INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at)"+
		" VALUES (?, ?, ?, 'board', ?, 'upsert', 3, '{\"n\":1}', '2026-10-08T09:30:00Z')")
	if err != nil {
		t.Fatalf("prepare the log seed: %v", err)
	}
	headIns, err := tx.PrepareContext(ctx, "INSERT INTO head (origin, tbl, pk, seq, hash) VALUES (?, 'board', ?, ?, ?)")
	if err != nil {
		t.Fatalf("prepare the head seed: %v", err)
	}
	for i := 1; i <= bulkRows; i++ {
		pk := `["bulk-` + strconv.Itoa(i) + `"]`
		if _, err := logIns.ExecContext(ctx, origin, i, i, pk); err != nil {
			t.Fatalf("seed log row %d: %v", i, err)
		}
		if _, err := headIns.ExecContext(ctx, origin, pk, i, digest); err != nil {
			t.Fatalf("seed head row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the seed: %v", err)
	}
	statsBefore, err := seeder.Stats(ctx)
	if err != nil {
		t.Fatalf("read the seed counters: %v", err)
	}
	if err := seeder.Push(ctx); err != nil {
		t.Fatalf("push the seed: %v", err)
	}
	statsAfter, err := seeder.Stats(ctx)
	if err != nil {
		t.Fatalf("read the seed counters: %v", err)
	}
	seedSent, seedRecv := bytesDelta(statsBefore, statsAfter)
	t.Logf("seeded %d rows in one transaction and one push: sent %d, received %d (bootstrap downloaded %d)",
		bulkRows, seedSent, seedRecv, seeder.Bootstrapped())
	_ = seedDB.Close()

	// Phase 2: open the seeded replica with the backend, so ensureLog runs
	// the format-4 upgrade, and read the one-time upgrade push off the
	// wrapper.
	recA := &recordingDriver{tursoDriver: newTursoDriver(filepath.Join(dir, "a.db"))}
	backendA := NewTursoBackend(recA)
	if err := backendA.Open(Spec{Version: ProtocolVersion, Origin: origin, URL: url, Token: token}); err != nil {
		t.Fatalf("open the seeded remote: %v", err)
	}
	t.Cleanup(func() { _ = backendA.Close() })
	t.Logf("upgrade push: sent %d, received %d (bootstrap downloaded %d)", recA.pushSent, recA.pushRecv, recA.Bootstrapped())
	assertUpgraded(t, backendA, bulkRows)

	// Phase 3: a second replica joins the upgraded remote and pulls it.
	recB := &recordingDriver{tursoDriver: newTursoDriver(filepath.Join(dir, "b.db"))}
	backendB := NewTursoBackend(recB)
	if err := backendB.Open(Spec{Version: ProtocolVersion, Origin: origin + "-reader", URL: url, Token: token}); err != nil {
		t.Fatalf("join the upgraded remote: %v", err)
	}
	joinStats, err := recB.Stats(ctx)
	if err != nil {
		t.Fatalf("read the join counters: %v", err)
	}
	joinSent := joinStats.NetworkSentBytes
	t.Cleanup(func() { _ = backendB.Close() })
	if recB.pushSent != 0 || recB.pushRecv != 0 {
		t.Errorf("the join pushed (sent %d, received %d), want none: the remote was already current", recB.pushSent, recB.pushRecv)
	}
	t.Logf("join pull: sent %d, received %d (bootstrap downloaded %d)", joinSent, joinStats.NetworkReceivedBytes, recB.Bootstrapped())
	assertUpgraded(t, backendB, bulkRows)

	// Phase 4: per-entry traffic on the upgraded log, about 200 entries in
	// batches of 6 and about 200 in batches of 32.
	next := bulkRows + 1
	appendBatches := func(batchSize, batchCount int) (sent, recv int64, entries int) {
		sentBefore, recvBefore := recA.pushSent, recA.pushRecv
		for i := 0; i < batchCount; i++ {
			batch := make([]Entry, 0, batchSize)
			for j := 0; j < batchSize; j++ {
				pk := `["t-` + strconv.Itoa(next) + `"]`
				next++
				batch = append(batch, upsertEntry(origin, "board", pk, `{"title":"t"}`))
			}
			if _, err := backendA.Append(batch); err != nil {
				t.Fatalf("append a batch of %d: %v", batchSize, err)
			}
		}
		return recA.pushSent - sentBefore, recA.pushRecv - recvBefore, batchSize * batchCount
	}
	sent6, recv6, n6 := appendBatches(6, 34)
	t.Logf("batches of 6: %d entries, sent %d received %d, per entry %.1f bytes",
		n6, sent6, recv6, float64(sent6+recv6)/float64(n6))
	sent32, recv32, n32 := appendBatches(32, 7)
	t.Logf("batches of 32: %d entries, sent %d received %d, per entry %.1f bytes",
		n32, sent32, recv32, float64(sent32+recv32)/float64(n32))

	total := seeder.Bootstrapped() + seedSent + seedRecv +
		recA.Bootstrapped() + recA.pushSent + recA.pushRecv +
		joinSent + recB.Bootstrapped() + recB.pushSent + recB.pushRecv
	t.Logf("total scratch traffic: %d bytes over %d seeded and %d appended rows", total, bulkRows, n6+n32)
	if total > 100<<20 {
		t.Errorf("scratch traffic = %d bytes, want under 100 MB: the test must stay inside the shared quota", total)
	}
}

// TestScratchRemoteCompact compacts a log on a real remote and reads the
// statement's plan from the engine that runs it, which is where the primary-key
// search has to hold.
func TestScratchRemoteCompact(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	origin := "scratch-c-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	b := openScratch(t, filepath.Join(t.TempDir(), "c.db"), origin, url, token)
	for _, pk := range []string{`["a"]`, `["b"]`, `["a"]`} {
		if _, err := b.Append([]Entry{upsertEntry(origin, "board", pk, `{"title":"t"}`)}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	rows, err := b.db.Query("EXPLAIN QUERY PLAN "+compactStatement, origin, 3, origin, 3)
	if err != nil {
		t.Fatalf("explain on the engine: %v", err)
	}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		_ = rows.Scan(&id, &parent, &unused, &detail)
		t.Logf("plan: %s", detail)
	}
	_ = rows.Close()
	res, err := b.Compact(3)
	if err != nil || res.Dropped != 1 || res.Floor != 3 {
		t.Fatalf("Compact(3) = %+v, %v; want one dropped at floor 3", res, err)
	}
}

// TestScratchRemoteRefusesASignedBatchWithoutABoundary runs the boundary rule
// against a real remote: a signed batch for an origin with no boundary row is
// refused, and one naming the wrong sequence is too.
func TestScratchRemoteRefusesASignedBatchWithoutABoundary(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	origin := "scratch-bound-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	b := openScratch(t, filepath.Join(t.TempDir(), "m.db"), origin, url, token)
	key := []byte("the passphrase this machine stretched")

	e := upsertEntry(origin, "board", `["s"]`, `{"title":"signed"}`)
	e.MAC = []byte("a signature")
	if _, err := b.Append([]Entry{e}); !errors.Is(err, ErrBoundary) {
		t.Fatalf("append with no boundary = %v, want ErrBoundary", err)
	}
	if _, err := b.AppendSigned([]Entry{e}, boundaryAt(key, origin, 9)); !errors.Is(err, ErrBoundary) {
		t.Fatalf("append with a wrong boundary = %v, want ErrBoundary", err)
	}
}
