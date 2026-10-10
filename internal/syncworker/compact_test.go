package syncworker

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func appendRow(t *testing.T, b *TursoBackend, pk string) {
	t.Helper()
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", pk, `{"title":"t"}`)}); err != nil {
		t.Fatalf("append %s: %v", pk, err)
	}
}

func logSeqs(t *testing.T, path string) []int {
	t.Helper()
	rows, err := openAt(t, path).Query("SELECT seq FROM log ORDER BY seq")
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var seq int
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, seq)
	}
	return out
}

func compactBackend(t *testing.T) (*TursoBackend, *localDriver, string) {
	t.Helper()
	path := tempReplica(t)
	driver := &localDriver{path: path}
	return backendFor(t, driver, "origin-a"), driver, path
}

func TestCompactDropsSupersededAndRecordsTheFloorInOnePush(t *testing.T) {
	b, driver, path := compactBackend(t)
	appendRow(t, b, `["a"]`)
	appendRow(t, b, `["b"]`)
	appendRow(t, b, `["a"]`)
	pushes := driver.pushes

	res, err := b.Compact(3)
	if err != nil || res.Dropped != 1 || res.Floor != 3 {
		t.Fatalf("Compact(3) = %+v, %v; want one dropped and floor 3", res, err)
	}
	if got := logSeqs(t, path); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("log holds %v, want seq 2 and 3", got)
	}
	if driver.pushes != pushes+1 {
		t.Fatalf("pushes = %d, want exactly one for the drop and the floor together", driver.pushes-pushes)
	}
	state, err := b.ReadMarks()
	if err != nil || state.Floors["origin-a"] != 3 {
		t.Fatalf("floors = %v, %v; want origin-a at 3", state.Floors, err)
	}
}

// A row whose only newer entry sits above the floor keeps its older entry.
func TestCompactBoundsTheSupersedingEntryByTheFloor(t *testing.T) {
	b, _, path := compactBackend(t)
	appendRow(t, b, `["p"]`)
	appendRow(t, b, `["c"]`)
	appendRow(t, b, `["p"]`)
	res, err := b.Compact(2)
	if err != nil || res.Dropped != 0 {
		t.Fatalf("Compact(2) = %+v, %v; want nothing dropped", res, err)
	}
	if got := logSeqs(t, path); len(got) != 3 {
		t.Fatalf("log holds %v, want all three", got)
	}
}

func TestCompactFloorNeverMovesDown(t *testing.T) {
	b, _, _ := compactBackend(t)
	for _, pk := range []string{`["a"]`, `["b"]`, `["c"]`} {
		appendRow(t, b, pk)
	}
	if _, err := b.Compact(3); err != nil {
		t.Fatalf("Compact(3): %v", err)
	}
	res, err := b.Compact(2)
	if err != nil || res.Floor != 3 {
		t.Fatalf("Compact(2) after 3 = %+v, %v; want the floor to stay 3", res, err)
	}
}

// A floor past the head is refused. One inside a batch comes from a mark no
// honest reader holds, and is moved down to the batch before rather than
// refused, so a forged mark cannot stop compaction for good.
func TestCompactAlignsAFloorNoReaderCanHold(t *testing.T) {
	b, _, _ := compactBackend(t)
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"t"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := b.Append([]Entry{
		upsertEntry("origin-a", "board", `["a"]`, `{"title":"u"}`),
		upsertEntry("origin-a", "board", `["b"]`, `{"title":"t"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	res, err := b.Compact(2)
	if err != nil || res.Floor != 1 || res.Dropped != 0 {
		t.Errorf("Compact(2) inside batch 2 = %+v, %v; want the floor moved down to 1, nothing dropped", res, err)
	}
	if _, err := b.Compact(9); !errors.Is(err, ErrProtocol) {
		t.Errorf("Compact(9) past the head = %v, want a refusal", err)
	}
}

// Compaction is one grouped pass over the origin's log, not a lookup per entry.
// A per-entry lookup took seconds at a few thousand entries and minutes on a
// real log, holding the worker and every verb behind it; the bound here is far
// above the grouped pass and far below the per-entry one.
func TestCompactIsNotQuadratic(t *testing.T) {
	b, _, path := compactBackend(t)
	const n = 8000
	db := openAt(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 1; i <= n; i++ {
		if _, err := tx.Exec("INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at) "+
			"VALUES ('origin-a', ?, ?, 'board', ?, 'upsert', 1, '{}', 't')", i, i, fmt.Sprintf(`["k%d"]`, i%(n/3))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	start := time.Now()
	res, err := b.Compact(n)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("compacting %d entries took %v", n, took)
	}
	if want := n - n/3; res.Dropped != want {
		t.Errorf("dropped %d, want %d", res.Dropped, want)
	}
}

func TestPullRangeVerbAndCompactVerbOverThePipe(t *testing.T) {
	b, _, _ := compactBackend(t)
	appendRow(t, b, `["a"]`)
	appendRow(t, b, `["b"]`)
	appendRow(t, b, `["a"]`)
	s := startWorker(t, b)
	s.askHello()

	resp := s.ask(Request{ID: "r", Verb: VerbPullRange, Origin: "origin-a", AfterSeq: 1, Upto: 2})
	if !resp.OK || len(resp.Entries) != 1 || resp.Entries[0].Seq != 2 {
		t.Fatalf("pull_range = %+v, want only seq 2", resp)
	}
	if resp := s.ask(Request{ID: "bad", Verb: VerbPullRange, Origin: "origin-a", AfterSeq: 3, Upto: 3}); resp.OK {
		t.Fatalf("an empty range was answered: %+v", resp)
	}
	resp = s.ask(Request{ID: "c", Verb: VerbCompact, Floor: 3})
	if !resp.OK || resp.Dropped != 1 || resp.Floor != 3 {
		t.Fatalf("compact = %+v, want one dropped at floor 3", resp)
	}
}

func TestCompactAndPullRangeBeforeHelloAreRefused(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.Compact(1); !errors.Is(err, ErrNoHello) {
		t.Errorf("Compact = %v, want ErrNoHello", err)
	}
	if _, err := b.PullRange("o", 0, 1); !errors.Is(err, ErrNoHello) {
		t.Errorf("PullRange = %v, want ErrNoHello", err)
	}
}

func TestCompactRefusesARecordedFloorItCannotRead(t *testing.T) {
	b, _, path := compactBackend(t)
	appendRow(t, b, `["a"]`)
	if err := execOnErr(t, path, "INSERT INTO meta (key, value) VALUES ('floor.origin-a', 'x')"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := b.Compact(1); err == nil {
		t.Fatal("Compact accepted a floor record that is not a number")
	}
}

func TestCompactAndPullRangeVerbsAnswerABackendRefusal(t *testing.T) {
	fb := &fakeBackend{}
	s := startWorker(t, fb)
	s.askHello()
	fb.mu.Lock()
	fb.fail = errors.New("the replica is gone")
	fb.mu.Unlock()
	if resp := s.ask(Request{ID: "r", Verb: VerbPullRange, Origin: "o", AfterSeq: 0, Upto: 1}); resp.OK {
		t.Errorf("pull_range = %+v, want the refusal", resp)
	}
	if resp := s.ask(Request{ID: "c", Verb: VerbCompact, Floor: 1}); resp.OK {
		t.Errorf("compact = %+v, want the refusal", resp)
	}
}

func TestCompactReportsAFailedPush(t *testing.T) {
	b, driver, _ := compactBackend(t)
	appendRow(t, b, `["a"]`)
	driver.pushErr = errors.New("push refused")
	if _, err := b.Compact(1); err == nil {
		t.Fatal("Compact reported success after the push failed")
	}
	driver.pushErr = nil
	if res, err := b.Compact(1); err != nil || res.Floor != 1 {
		t.Fatalf("the retry = %+v, %v; want it to succeed", res, err)
	}
}

func TestCompactRefusesANegativeFloor(t *testing.T) {
	b, _, _ := compactBackend(t)
	appendRow(t, b, `["a"]`)
	if _, err := b.Compact(-1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Compact(-1) = %v, want a refusal", err)
	}
}

func TestCompactAndPullRangeReportAMissingLog(t *testing.T) {
	b, _, path := compactBackend(t)
	appendRow(t, b, `["a"]`)
	if err := execOnErr(t, path, "DROP TABLE log"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := b.Compact(1); err == nil {
		t.Error("Compact succeeded without a log table")
	}
	if _, err := b.PullRange("origin-a", 0, 1); err == nil {
		t.Error("PullRange succeeded without a log table")
	}
}

func TestCompactReportsAClosedReplica(t *testing.T) {
	b, _, _ := compactBackend(t)
	appendRow(t, b, `["a"]`)
	_ = b.db.Close()
	if _, err := b.Compact(1); err == nil {
		t.Fatal("Compact succeeded on a closed replica")
	}
}

func TestCompactReportsAMissingMetaTable(t *testing.T) {
	b, _, path := compactBackend(t)
	appendRow(t, b, `["a"]`)
	if err := execOnErr(t, path, "DROP TABLE meta"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := b.Compact(1); err == nil {
		t.Fatal("Compact succeeded without a meta table")
	}
	if got := logSeqs(t, path); len(got) != 1 {
		t.Fatalf("log holds %v, want the failed compaction rolled back", got)
	}
}
