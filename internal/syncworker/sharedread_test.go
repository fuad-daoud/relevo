package syncworker

import (
	"errors"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// TestPullHistoryNoneAnswersFromSharedWithoutStoreOperations pins that in none
// history mode, Pull answers from the shared table only without reading or
// fetching segments from the store, and non-mirrored tables are absent.
func TestPullHistoryNoneAnswersFromSharedWithoutStoreOperations(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)

	writerA := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe, "doc:secrets", "doc:config")
	writerB := backendOnBlobStore(t, &localDriver{path: path}, "origin-b", probe, "doc:secrets", "doc:config")

	if _, err := writerA.Append([]Entry{
		upsertEntry("origin-a", "doc:secrets", `["s1"]`, `{"secret":"key-a"}`),
		upsertEntry("origin-a", "board", `["b1"]`, `{"title":"board-a"}`),
	}); err != nil {
		t.Fatalf("writerA append: %v", err)
	}
	if _, err := writerB.Append([]Entry{
		upsertEntry("origin-b", "doc:config", `["c1"]`, `{"setting":"val-b"}`),
	}); err != nil {
		t.Fatalf("writerB append: %v", err)
	}

	readerProbe := newProbeStore(base)
	reader := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-reader", readerProbe, func(s *Spec) {
		s.History = HistoryNone
	})

	got, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("reader Pull in none mode: %v", err)
	}

	gets := readerProbe.gets.Load()
	lists := readerProbe.lists.Load()
	puts := readerProbe.puts.Load()
	t.Logf("none pull probe counts: gets=%d, lists=%d, puts=%d", gets, lists, puts)

	if gets != 0 || lists != 0 || puts != 0 {
		t.Errorf("store operations: gets=%d, lists=%d, puts=%d; want all zero", gets, lists, puts)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want exactly 2 mirrored entries: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Tbl == "board" {
			t.Errorf("non-mirrored table board present in pull: %+v", e)
		}
	}
}

// TestPullRangeHistoryNoneWithoutStoreOperations pins that in none history mode,
// PullRange answers from shared without reading heads or store segments.
func TestPullRangeHistoryNoneWithoutStoreOperations(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)

	writer := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe, "doc:secrets")
	if _, err := writer.Append([]Entry{
		upsertEntry("origin-a", "doc:secrets", `["s1"]`, `{"secret":"val"}`),
	}); err != nil {
		t.Fatalf("writer append: %v", err)
	}

	readerProbe := newProbeStore(base)
	reader := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-reader", readerProbe, func(s *Spec) {
		s.History = HistoryNone
	})

	got, err := reader.PullRange("origin-a", 0, 100)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}

	gets := readerProbe.gets.Load()
	lists := readerProbe.lists.Load()
	puts := readerProbe.puts.Load()
	t.Logf("none pull_range probe counts: gets=%d, lists=%d, puts=%d", gets, lists, puts)

	if gets != 0 || lists != 0 || puts != 0 {
		t.Errorf("store operations: gets=%d, lists=%d, puts=%d; want all zero", gets, lists, puts)
	}
	if len(got) != 1 || got[0].Tbl != "doc:secrets" {
		t.Fatalf("got %+v, want doc:secrets entry", got)
	}
}

// TestPullRangeOnWorkerWithNoR2ServesSharedAndRefusesOtherVerbs pins that a
// worker opened with R2: nil serves PullRange from shared with zero store
// operations, while its Pull, Head and Append verbs still refuse with ErrNoR2.
func TestPullRangeOnWorkerWithNoR2ServesSharedAndRefusesOtherVerbs(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)

	writer := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", base, "doc:secrets")
	if _, err := writer.Append([]Entry{
		upsertEntry("origin-a", "doc:secrets", `["sec"]`, `{"fleet":"token"}`),
	}); err != nil {
		t.Fatalf("writer append: %v", err)
	}

	joiner := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-joiner", nil, func(s *Spec) {
		s.R2 = nil
	})

	got, err := joiner.PullRange("origin-a", 0, 100)
	if err != nil {
		t.Fatalf("joiner PullRange: %v", err)
	}
	t.Logf("bucketless pull_range probe counts: gets=0, lists=0, puts=0 (no store configured)")

	if len(got) != 1 || got[0].Tbl != "doc:secrets" {
		t.Fatalf("joiner got %+v, want doc:secrets", got)
	}
	if _, err := joiner.Pull(nil); !errors.Is(err, ErrNoR2) {
		t.Errorf("joiner Pull = %v, want ErrNoR2", err)
	}
	if _, err := joiner.Head("origin-a", "", 0); !errors.Is(err, ErrNoR2) {
		t.Errorf("joiner Head = %v, want ErrNoR2", err)
	}
	if _, err := joiner.Append([]Entry{upsertEntry("origin-joiner", "doc:secrets", `["x"]`, `{}`)}); !errors.Is(err, ErrNoR2) {
		t.Errorf("joiner Append = %v, want ErrNoR2", err)
	}
}

// TestJoinerReadsSecretsWithoutR2ThenJoinsWithFullStore tells the join story:
// a joining machine with no R2 bucket reads the fleet secrets from the shared
// table without store operations, then opens a full-history backend on the same
// replica with an R2 store and runs an ordinary pull that may list segments.
func TestJoinerReadsSecretsWithoutR2ThenJoinsWithFullStore(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)

	fleet := backendOnBlobStore(t, &localDriver{path: path}, "origin-fleet", base, "doc:secrets")
	if _, err := fleet.Append([]Entry{
		upsertEntry("origin-fleet", "doc:secrets", `["fleet-key"]`, `{"r2":"token"}`),
		upsertEntry("origin-fleet", "board", `["b1"]`, `{"title":"general"}`),
	}); err != nil {
		t.Fatalf("fleet append: %v", err)
	}

	// 1. Joiner starts without R2 credentials and reads doc:secrets from shared.
	joinerInitial := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-joiner", nil, func(s *Spec) {
		s.R2 = nil
	})
	secrets, err := joinerInitial.PullRange("origin-fleet", 0, 100)
	if err != nil {
		t.Fatalf("joiner initial read: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Tbl != "doc:secrets" {
		t.Fatalf("joiner initial read = %+v, want doc:secrets", secrets)
	}

	// 2. Having obtained secrets, the joiner opens a fresh backend with full R2 store.
	joinerProbe := newProbeStore(base)
	joinerFull := backendOnBlobStore(t, &localDriver{path: path}, "origin-joiner", joinerProbe)

	pulled, err := joinerFull.Pull(nil)
	if err != nil {
		t.Fatalf("joiner full pull: %v", err)
	}
	if len(pulled) != 2 {
		t.Fatalf("joiner full pull = %d entries, want 2", len(pulled))
	}
	if joinerProbe.lists.Load() == 0 {
		t.Errorf("full history pull listed 0 times, want segments listing")
	}
}

// TestPullRangePagesSharedRowsAndBoundCursorAdvances pins that with b.page
// lowered to 2, a paging loop over five entries terminates with all five, never
// repeats a sequence, and detects when a cursor fails to advance.
func TestPullRangePagesSharedRowsAndBoundCursorAdvances(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)

	writer := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", base, "doc:config")
	for i := 1; i <= 5; i++ {
		if _, err := writer.Append([]Entry{
			upsertEntry("origin-a", "doc:config", `["c"]`, `{"step":1}`),
		}); err != nil {
			t.Fatalf("append entry %d: %v", i, err)
		}
	}

	reader := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-reader", base, func(s *Spec) {
		s.History = HistoryNone
	})
	reader.page = 2

	var got []Entry
	cursor := 0
	for {
		page, err := reader.PullRange("origin-a", cursor, 100)
		if err != nil {
			t.Fatalf("PullRange after %d: %v", cursor, err)
		}
		if len(page) == 0 {
			break
		}
		lastSeq := page[len(page)-1].Seq
		if lastSeq <= cursor {
			t.Fatalf("cursor failed to advance: was %d, last page seq %d", cursor, lastSeq)
		}
		for _, e := range page {
			if e.Seq <= cursor {
				t.Fatalf("page repeated sequence %d (cursor was %d)", e.Seq, cursor)
			}
		}
		cursor = lastSeq
		got = append(got, page...)
	}

	if len(got) != 5 {
		t.Fatalf("got %d entries, want 5", len(got))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("entry %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

// TestPullNeverSplitsSharedBatch pins that when a cut falls inside a batch,
// sharedPull finishes the batch so a page never ends inside an atomic batch.
func TestPullNeverSplitsSharedBatch(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)

	writer := backendOnBlobStore(t, &localDriver{path: path}, "origin-b", base, "doc:secrets")
	if _, err := writer.Append([]Entry{
		upsertEntry("origin-b", "doc:secrets", `["s1"]`, `{"k":1}`),
		upsertEntry("origin-b", "doc:secrets", `["s2"]`, `{"k":2}`),
		upsertEntry("origin-b", "doc:secrets", `["s3"]`, `{"k":3}`),
	}); err != nil {
		t.Fatalf("append batch of 3: %v", err)
	}

	reader := backendOnBlobStoreWithSpec(t, &localDriver{path: path}, "origin-reader", base, func(s *Spec) {
		s.History = HistoryNone
	})
	reader.page = 2

	pulled, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("reader Pull: %v", err)
	}
	if len(pulled) != 3 {
		t.Fatalf("pulled %d entries, want all 3 of the batch unbroken (page was 2)", len(pulled))
	}
}

// TestWorkerPipePullHistoryNone pins the pull verb over the pipe in none
// history mode: hello establishes HistoryNone and pull answers with OK and
// the mirrored shared entries.
func TestWorkerPipePullHistoryNone(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)

	writer := backendOnBlobStore(t, &localDriver{path: path}, "origin-b", base, "doc:config")
	if _, err := writer.Append([]Entry{
		upsertEntry("origin-b", "doc:config", `["cfg"]`, `{"v":42}`),
	}); err != nil {
		t.Fatalf("writer append: %v", err)
	}

	b := NewTursoBackend(&localDriver{path: path})
	w := startWorker(t, b)
	w.askHello(func(r *Request) {
		r.History = HistoryNone
	})

	resp := w.ask(Request{ID: "p1", Verb: VerbPull})
	if !resp.OK {
		t.Fatalf("pull verb = %+v, want OK", resp)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Tbl != "doc:config" {
		t.Fatalf("pull verb entries = %+v, want doc:config entry", resp.Entries)
	}
}

// TestSharedEmptyAnswersEmptyAndCorruptAtRefuses pins edge cases: an empty
// shared table answers an empty list rather than an error, and a shared row
// with an unparseable at timestamp produces a remote refusal.
func TestSharedEmptyAnswersEmptyAndCorruptAtRefuses(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendOnBlobStoreWithSpec(t, driver, "origin-reader", nil, func(s *Spec) {
		s.History = HistoryNone
		s.R2 = nil
	})

	emptyPull, err := b.Pull(nil)
	if err != nil {
		t.Fatalf("empty Pull: %v", err)
	}
	if !reflect.DeepEqual(emptyPull, []Entry{}) {
		t.Errorf("empty Pull = %+v, want empty []Entry{}", emptyPull)
	}

	emptyRange, err := b.PullRange("origin-x", 0, 10)
	if err != nil {
		t.Fatalf("empty PullRange: %v", err)
	}
	if !reflect.DeepEqual(emptyRange, []Entry{}) {
		t.Errorf("empty PullRange = %+v, want empty []Entry{}", emptyRange)
	}

	// Insert corrupt time into shared and a heads row so readOtherHeads finds it.
	execOn(t, path,
		"INSERT INTO heads (origin, seq, key, entries, bytes, at) VALUES ('origin-bad', 1, 'origin-bad/log/000000000001', 1, 1, '2026-10-08T09:30:00Z')")
	execOn(t, path,
		"INSERT INTO shared (origin, seq, batch, tbl, pk, op, schema_version, body, at)"+
			" VALUES ('origin-bad', 1, 1, 'doc:config', '[\"k\"]', 'upsert', 1, '{}', 'not-a-valid-timestamp')")

	_, pullErr := b.Pull(nil)
	if pullErr == nil {
		t.Fatal("Pull with corrupt time succeeded, want refusal")
	}
	if code := RefusalCodeOf(pullErr); code != CodeRemote {
		t.Errorf("Pull corrupt time refusal code = %q, want %q (err = %v)", code, CodeRemote, pullErr)
	}

	_, rangeErr := b.PullRange("origin-bad", 0, 10)
	if rangeErr == nil {
		t.Fatal("PullRange with corrupt time succeeded, want refusal")
	}
	if code := RefusalCodeOf(rangeErr); code != CodeRemote {
		t.Errorf("PullRange corrupt time refusal code = %q, want %q (err = %v)", code, CodeRemote, rangeErr)
	}
}
