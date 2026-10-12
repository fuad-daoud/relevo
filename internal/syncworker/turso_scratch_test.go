package syncworker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
// real sync connection. The first append inserts the row's head; every later
// append of that row has to move the head it already holds, which is the path
// a sync connection's own change capture refuses for an upsert that updates.
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

	pulled, err := b.Pull(nil)
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
