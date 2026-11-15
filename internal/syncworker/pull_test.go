package syncworker

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

func appendRow(t *testing.T, b *TursoBackend, pk string) {
	t.Helper()
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", pk, `{"title":"t"}`)}); err != nil {
		t.Fatalf("append %s: %v", pk, err)
	}
}

func pullBackend(t *testing.T) (*TursoBackend, *localDriver, string) {
	t.Helper()
	path := tempReplica(t)
	driver := &localDriver{path: path}
	return backendFor(t, driver, "origin-a"), driver, path
}

func TestPullRangeVerbOverThePipe(t *testing.T) {
	b, _, _ := pullBackend(t)
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
}

func TestPullRangeBeforeHelloIsRefused(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.PullRange("o", 0, 1); !errors.Is(err, ErrNoHello) {
		t.Errorf("PullRange = %v, want ErrNoHello", err)
	}
}

func TestPullRangeVerbAnswersABackendRefusal(t *testing.T) {
	fb := &fakeBackend{}
	s := startWorker(t, fb)
	s.askHello()
	fb.mu.Lock()
	fb.fail = errors.New("the replica is gone")
	fb.mu.Unlock()
	if resp := s.ask(Request{ID: "r", Verb: VerbPullRange, Origin: "o", AfterSeq: 0, Upto: 1}); resp.OK {
		t.Errorf("pull_range = %+v, want the refusal", resp)
	}
}

// TestPullRangeReportsAMissingHeads pins that a range read over a replica
// without the heads table is reported rather than answered as an empty range.
func TestPullRangeReportsAMissingHeads(t *testing.T) {
	b, _, path := pullBackend(t)
	appendRow(t, b, `["a"]`)
	if err := execOnErr(t, path, "DROP TABLE heads"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := b.PullRange("origin-a", 0, 1); err == nil {
		t.Error("PullRange succeeded without a heads table")
	}
}

// TestPullRangeReadsOneSegment pins the range read inside one segment: only
// the entries in (after, upto] come back, in sequence order, at the cost of
// one listing and one GET -- and no transfer, since the replica is not pulled.
func TestPullRangeReadsOneSegment(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	driver := &localDriver{path: path}
	reader := backendOnBlobStore(t, driver, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 8)}},
		seedHeads{seq: 8, key: blobstore.SegmentKey("origin-b", 1), entries: 8})

	got, err := reader.PullRange("origin-b", 2, 5)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	assertSeqs(t, got, 3, 4, 5)
	if n := probe.gets.Load(); n != 1 {
		t.Errorf("GET count = %d, want 1", n)
	}
	if n := probe.lists.Load(); n != 1 {
		t.Errorf("LIST count = %d, want 1", n)
	}
	if driver.pulls != 0 {
		t.Errorf("driver pulled %d times, want 0: a range read costs no transfer", driver.pulls)
	}
}

// TestPullRangeSpansThreeSegments pins a range over three segments: the
// listing is narrowed to the key the mark's arithmetic names, only the
// segments the range needs are fetched -- an orphan past the ceiling is not
// -- and every sequence in the range comes back once, in order, over the verb
// as well as the call.
func TestPullRangeSpansThreeSegments(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{
			{entries: seedEntries("origin-b", 1, 256)},
			{entries: seedEntries("origin-b", 257, 256)},
			{entries: seedEntries("origin-b", 513, 256)},
			{entries: seedEntries("origin-b", 769, 256)},
		},
		seedHeads{seq: 768, key: blobstore.SegmentKey("origin-b", 513), entries: 768})

	const after, upto = 100, 700
	got, err := reader.PullRange("origin-b", after, upto)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	want := make([]int, 0, upto-after)
	for seq := after + 1; seq <= upto; seq++ {
		want = append(want, seq)
	}
	assertSeqs(t, got, want...)
	if n := probe.gets.Load(); n != 3 {
		t.Errorf("GET count = %d, want 3: only the three segments the range needs", n)
	}
	if n := probe.lists.Load(); n != 1 {
		t.Errorf("LIST count = %d, want 1", n)
	}
	calls := probe.listCalls()
	if len(calls) != 1 {
		t.Fatalf("recorded %d listings, want 1", len(calls))
	}
	if calls[0].prefix != "origin-b/log/" {
		t.Errorf("listed prefix %q, want origin-b/log/", calls[0].prefix)
	}
	if startAfter := blobstore.SegmentKey("origin-b", max(after-255, 0)); calls[0].startAfter != startAfter {
		t.Errorf("startAfter = %q, want %q: the listing is narrowed to the mark", calls[0].startAfter, startAfter)
	}
	t.Logf("three-segment range (%d, %d]: GET=%d LIST=%d", after, upto, probe.gets.Load(), probe.lists.Load())

	w := startWorker(t, reader)
	w.askHello()
	resp := w.ask(Request{ID: "r", Verb: VerbPullRange, Origin: "origin-b", AfterSeq: after, Upto: upto})
	if !resp.OK || len(resp.Entries) != upto-after {
		t.Fatalf("pull_range over the pipe = %d entries, ok=%v, want %d", len(resp.Entries), resp.OK, upto-after)
	}
	if resp.Entries[0].Seq != after+1 || resp.Entries[len(resp.Entries)-1].Seq != upto {
		t.Errorf("pull_range over the pipe spans %d..%d, want %d..%d",
			resp.Entries[0].Seq, resp.Entries[len(resp.Entries)-1].Seq, after+1, upto)
	}
}

// TestPullRangeClampsUptoToTheHead pins that the head bounds the range, not
// the bucket: an upto above the head reads to the head and never fetches a
// segment past it, and an after already at the head answers empty without a
// listing even when upto names more.
func TestPullRangeClampsUptoToTheHead(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}, {entries: seedEntries("origin-b", 9, 4)}},
		seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4})

	got, err := reader.PullRange("origin-b", 0, 100)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	assertSeqs(t, got, 1, 2, 3, 4)
	if n := probe.gets.Load(); n != 1 {
		t.Errorf("GET count = %d, want 1: the segment past the head is never fetched", n)
	}
	got, err = reader.PullRange("origin-b", 4, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("PullRange at the head = %+v, %v; want empty", got, err)
	}
	if n := probe.lists.Load(); n != 1 {
		t.Errorf("LIST count = %d, want 1: an after at the head costs no listing", n)
	}
}

// TestPullRangeDoesNotFetchASegmentPastUpto pins that a segment whose first
// sequence is past upto is not fetched: the ceiling cuts the selection, not
// just the entries handed back.
func TestPullRangeDoesNotFetchASegmentPastUpto(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{
			{entries: seedEntries("origin-b", 1, 4)},
			{entries: seedEntries("origin-b", 5, 4)},
			{entries: seedEntries("origin-b", 9, 4)},
		},
		seedHeads{seq: 12, key: blobstore.SegmentKey("origin-b", 9), entries: 12})

	got, err := reader.PullRange("origin-b", 0, 6)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	assertSeqs(t, got, 1, 2, 3, 4, 5, 6)
	if n := probe.gets.Load(); n != 2 {
		t.Errorf("GET count = %d, want 2: the segment starting past upto is not fetched", n)
	}
}

// TestPullRangeReadsSparseLegacySeqs pins that a legacy segment's sparse
// sequences read as they are: the range keeps the seqs it names, including a
// ceiling that cuts inside the segment, and the listing stays whole while
// after is inside the legacy range.
func TestPullRangeReadsSparseLegacySeqs(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedBatch("origin-b", 1, 1, 300, 600), legacy: true}},
		seedHeads{seq: 600, key: blobstore.SegmentKey("origin-b", 1), entries: 3, legacyUpto: 600})

	got, err := reader.PullRange("origin-b", 0, 1000)
	if err != nil {
		t.Fatalf("PullRange over the whole legacy segment: %v", err)
	}
	assertSeqs(t, got, 1, 300, 600)
	got, err = reader.PullRange("origin-b", 100, 400)
	if err != nil {
		t.Fatalf("PullRange inside the legacy segment: %v", err)
	}
	assertSeqs(t, got, 300)
	got, err = reader.PullRange("origin-b", 0, 350)
	if err != nil {
		t.Fatalf("PullRange cut inside the legacy segment: %v", err)
	}
	assertSeqs(t, got, 1, 300)
	for i, c := range probe.listCalls() {
		if c.startAfter != "" {
			t.Errorf("listing %d started after %q, want the whole prefix inside the legacy range", i+1, c.startAfter)
		}
	}
}

// TestPullRangeCutsToThePage pins the page bound: a longer range answers
// exactly b.page entries even when the fetched group holds more, and the
// caller pages on with after until the range is drained.
func TestPullRangeCutsToThePage(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	reader.page = 2
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{
			{entries: seedEntries("origin-b", 1, 4)},
			{entries: seedEntries("origin-b", 5, 4)},
			{entries: seedEntries("origin-b", 9, 4)},
			{entries: seedEntries("origin-b", 13, 4)},
		},
		seedHeads{seq: 16, key: blobstore.SegmentKey("origin-b", 13), entries: 16})

	first, err := reader.PullRange("origin-b", 0, 16)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	assertSeqs(t, first, 1, 2)
	if n := probe.gets.Load(); n != 4 {
		t.Errorf("GET count = %d, want 4: the whole group is fetched, then cut to the page", n)
	}
	got := append([]Entry(nil), first...)
	after := first[len(first)-1].Seq
	for {
		page, err := reader.PullRange("origin-b", after, 16)
		if err != nil {
			t.Fatalf("page after %d: %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 2 {
			t.Fatalf("page after %d = %d entries, want exactly 2", after, len(page))
		}
		got = append(got, page...)
		after = page[len(page)-1].Seq
	}
	want := make([]int, 16)
	for i := range want {
		want[i] = i + 1
	}
	assertSeqs(t, got, want...)
}

// TestPullRangeAnswersAnUnknownOriginWithoutAListing pins that an origin with
// no heads row answers empty without touching the store.
func TestPullRangeAnswersAnUnknownOriginWithoutAListing(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}},
		seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4})

	got, err := reader.PullRange("origin-x", 0, 10)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("PullRange of an unknown origin = %+v, %v; want an empty answer", got, err)
	}
	if n := probe.lists.Load(); n != 0 {
		t.Errorf("LIST count = %d, want 0: an unknown origin is not listed", n)
	}
	if n := probe.gets.Load(); n != 0 {
		t.Errorf("GET count = %d, want 0", n)
	}
}

// TestPullRangeWithoutR2 pins the two answers a backend whose hello named no
// bucket gives: a heads row is refused with ErrNoR2 before any transfer, and
// an origin with no row answers empty without a store.
func TestPullRangeWithoutR2(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := NewTursoBackend(driver)
	if err := b.Open(Spec{
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://scratch.example",
		Token:   "the-token",
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	execOn(t, driver.path,
		"INSERT INTO heads (origin, seq, key, entries, bytes, legacy_upto, at)"+
			" VALUES ('origin-b', 4, 'origin-b/log/000000000001', 4, 100, 0, '2026-10-08T09:30:00Z')")

	if _, err := b.PullRange("origin-b", 0, 4); !errors.Is(err, ErrNoR2) {
		t.Errorf("PullRange with a head and no bucket = %v, want ErrNoR2", err)
	}
	got, err := b.PullRange("origin-x", 0, 4)
	if err != nil || len(got) != 0 {
		t.Errorf("PullRange with no head and no bucket = %+v, %v; want empty", got, err)
	}
	if driver.pulls != 0 {
		t.Errorf("driver pulled %d times, want 0: a range read costs no transfer", driver.pulls)
	}
}

// TestPullRangeRefusesAListedSegmentTheStoreDoesNotHold pins the refusal a
// listed-missing key gets, through the verb as well as the call: the class the
// daemon latches on, naming the key.
func TestPullRangeRefusesAListedSegmentTheStoreDoesNotHold(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}, {entries: seedEntries("origin-b", 5, 4)}},
		seedHeads{seq: 8, key: blobstore.SegmentKey("origin-b", 5), entries: 8})
	key := blobstore.SegmentKey("origin-b", 5)
	probe.missing[key] = true

	_, err := reader.PullRange("origin-b", 0, 8)
	if code := RefusalCodeOf(err); code != CodeBlobMissing {
		t.Fatalf("PullRange code = %q, want %q (err = %v)", code, CodeBlobMissing, err)
	}
	if !strings.Contains(err.Error(), key) {
		t.Errorf("refusal %q does not name the missing key %s", err, key)
	}
	w := startWorker(t, reader)
	w.askHello()
	resp := w.ask(Request{ID: "r", Verb: VerbPullRange, Origin: "origin-b", AfterSeq: 0, Upto: 8})
	if resp.OK || resp.Code != CodeBlobMissing {
		t.Fatalf("pull_range over the pipe = %+v, want a %s refusal", resp, CodeBlobMissing)
	}
	if !strings.Contains(resp.Error, key) {
		t.Errorf("pipe refusal %q does not name the missing key %s", resp.Error, key)
	}
}

// TestPullRangeRefusesAHeadWithNoSegment pins that a heads row past after with
// nothing listed refuses with the head's key named, the same refusal the pull
// gives: an empty answer would stall the reader silently.
func TestPullRangeRefusesAHeadWithNoSegment(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	heads := seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4}
	seedOrigin(t, path, store, "origin-b", nil, heads)

	_, err := reader.PullRange("origin-b", 0, 4)
	if code := RefusalCodeOf(err); code != CodeBlobMissing {
		t.Fatalf("PullRange code = %q, want %q (err = %v)", code, CodeBlobMissing, err)
	}
	if !strings.Contains(err.Error(), heads.key) {
		t.Errorf("refusal %q does not name the heads key %s", err, heads.key)
	}
}

// TestPullRangeRefusesAHeadsRowItCannotScan pins that a heads row whose column
// types do not match heads is reported rather than read as a zero value.
func TestPullRangeRefusesAHeadsRowItCannotScan(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	execOn(t, path, "INSERT INTO heads (origin, seq, key, entries, bytes, at)"+
		" VALUES ('origin-b', 'not a number', 'origin-b/log/000000000001', 1, 1, '2026-10-08T09:30:00Z')")
	if _, err := reader.PullRange("origin-b", 0, 4); err == nil {
		t.Error("PullRange read a heads row whose sequence is not a number")
	}
}

// TestPullRangeRefusesWhenTheListingFails pins that a listing the store cannot
// answer is reported, unmarked: a bucket that did not answer is a fault a
// retry can get past, not a refusal that repeats.
func TestPullRangeRefusesWhenTheListingFails(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}},
		seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4})
	probe.listErr = errors.New("bucket unreachable")

	_, err := reader.PullRange("origin-b", 0, 4)
	if err == nil || !strings.Contains(err.Error(), "bucket unreachable") {
		t.Fatalf("err = %v, want the listing's failure", err)
	}
	if code := RefusalCodeOf(err); code != "" {
		t.Errorf("code = %q, want none: a failed listing is left to a retry", code)
	}
}

// TestPullRangeReturnsTheTimeInUTC pins that a range read normalises the
// writer's zone to UTC: the MAC covers the UTC form, so a reader handing back
// the writer's zone would break every signature made over it.
func TestPullRangeReturnsTheTimeInUTC(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	e := upsertEntry("origin-b", "board", `["t"]`, `{"title":"zoned"}`)
	e.Seq, e.Batch = 1, 1
	e.At = time.Date(2026, 10, 8, 11, 30, 0, 0, time.FixedZone("two hours out", 7200))
	seedOrigin(t, path, store, "origin-b", []seedSegment{{entries: []Entry{e}}},
		seedHeads{seq: 1, key: blobstore.SegmentKey("origin-b", 1), entries: 1})

	got, err := reader.PullRange("origin-b", 0, 1)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PullRange = %d entries, want 1", len(got))
	}
	if got[0].At.Location() != time.UTC {
		t.Errorf("the entry came back with the time in %v, want UTC", got[0].At.Location())
	}
	if !got[0].At.Equal(e.At) {
		t.Errorf("the entry came back at %v, want the same instant %v", got[0].At, e.At)
	}
}
