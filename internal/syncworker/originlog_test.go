package syncworker

// The pull's read side against seeded segments: a heads row and a bucket are
// planted directly, so what a pull lists, fetches and returns is pinned without
// depending on Append. The probe counts and records what the reader asks of the
// store, which is how a test states that a segment is fetched once, a listing
// is narrowed, or an orphan is never touched.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// probeStore wraps a store to count and record what a reader asks of it. The
// counters are atomic because Get runs on four goroutines at once; the listing
// record and the missing set are mutex-guarded for the same reason. A key in
// missing still lists but answers Get as not found, which is how a test plants
// a segment the listing promises and the store does not hold.
type probeStore struct {
	blobstore.BlobStore
	gets    atomic.Int64
	lists   atomic.Int64
	mu      sync.Mutex
	listed  []listCall
	missing map[string]bool
	// listErr fails every listing when set, before any pull runs.
	listErr error
}

// listCall is one listing as it arrived, so a test asserts the startAfter the
// reader computed rather than re-deriving it.
type listCall struct{ prefix, startAfter string }

func newProbeStore(base blobstore.BlobStore) *probeStore {
	return &probeStore{BlobStore: base, missing: make(map[string]bool)}
}

func (p *probeStore) Get(ctx context.Context, key string, w io.Writer) (int64, error) {
	p.gets.Add(1)
	p.mu.Lock()
	missing := p.missing[key]
	p.mu.Unlock()
	if missing {
		return 0, fmt.Errorf("probe: get %s: %w", key, blobstore.ErrNotFound)
	}
	return p.BlobStore.Get(ctx, key, w)
}

func (p *probeStore) ListAfter(ctx context.Context, prefix, startAfter string) ([]blobstore.Object, error) {
	p.lists.Add(1)
	p.mu.Lock()
	p.listed = append(p.listed, listCall{prefix: prefix, startAfter: startAfter})
	listErr := p.listErr
	p.mu.Unlock()
	if listErr != nil {
		return nil, listErr
	}
	return p.BlobStore.ListAfter(ctx, prefix, startAfter)
}

// listCalls reads the recorded listings under the lock, so a test asserts what
// the reader asked rather than what raced past.
func (p *probeStore) listCalls() []listCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]listCall(nil), p.listed...)
}

// seedHeads is one origin's heads row as a reader test plants it: the newest
// sequence, the newest segment's key, the entry and byte totals, and the last
// sequence the legacy migration covered.
type seedHeads struct {
	seq        int
	key        string
	entries    int
	bytes      int64
	legacyUpto int
}

// seedSegment is one segment a reader test plants, with the shape that wrote
// it: a legacy segment's seqs need only increase and may cut a batch; a new
// one is contiguous and holds a single batch.
type seedSegment struct {
	entries []Entry
	legacy  bool
}

// seedOrigin puts the encoded segments into the store and inserts the origin's
// heads row into the replica at path, so a reader test does not depend on
// Append. The segments are keyed by their first sequence, the way a writer
// publishes them.
func seedOrigin(t *testing.T, path string, store blobstore.BlobStore, origin string, segments []seedSegment, heads seedHeads) {
	t.Helper()
	for _, seg := range segments {
		var data []byte
		var err error
		if seg.legacy {
			data, _, err = EncodeLegacySegment(origin, seg.entries)
		} else {
			data, _, err = EncodeSegment(origin, seg.entries)
		}
		if err != nil {
			t.Fatalf("encode a segment for %s: %v", origin, err)
		}
		key := blobstore.SegmentKey(origin, seg.entries[0].Seq)
		if err := store.Put(context.Background(), key, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("put segment %s: %v", key, err)
		}
	}
	execOn(t, path, fmt.Sprintf(
		"INSERT INTO heads (origin, seq, key, entries, bytes, legacy_upto, at) VALUES (%s, %d, %s, %d, %d, %d, '2026-10-08T09:30:00Z')",
		sqlString(origin), heads.seq, sqlString(heads.key), heads.entries, heads.bytes, heads.legacyUpto))
}

// sqlString quotes one value for a seeded statement, doubling any quote it
// holds so an origin under test cannot end the literal.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// seedEntries builds n contiguous entries of one batch starting at first,
// which is the shape a new segment holds.
func seedEntries(origin string, first, n int) []Entry {
	out := make([]Entry, n)
	for i := range out {
		out[i] = upsertEntry(origin, "board", fmt.Sprintf(`["k%d"]`, first+i), fmt.Sprintf(`{"title":"%d"}`, first+i))
		out[i].Seq = first + i
		out[i].Batch = first
	}
	return out
}

// seedBatch builds entries of one batch at the given sequences, which is how a
// legacy segment's sparse seqs are planted.
func seedBatch(origin string, batch int, seqs ...int) []Entry {
	out := make([]Entry, len(seqs))
	for i, seq := range seqs {
		out[i] = upsertEntry(origin, "board", fmt.Sprintf(`["k%d"]`, seq), fmt.Sprintf(`{"title":"%d"}`, seq))
		out[i].Seq = seq
		out[i].Batch = batch
	}
	return out
}

// askPullOverPipe serves the backend over a worker and asks VerbPull, so a
// refusal test pins the code the user sees. hello re-opens the backend through
// the openStore the test set, so the pipe reads the same store.
func askPullOverPipe(t *testing.T, b *TursoBackend) Response {
	t.Helper()
	w := startWorker(t, b)
	w.askHello()
	return w.ask(Request{ID: "p", Verb: VerbPull})
}

// TestListStartAfter pins where a listing starts: the whole prefix while the
// mark is inside the legacy range, whose sparse seqs no arithmetic can narrow,
// and the key of the segment that could end at the mark once past it. The key
// is exclusive, so a segment ending at the mark is skipped, not listed.
func TestListStartAfter(t *testing.T) {
	cases := []struct {
		name       string
		after      int
		legacyUpto int
		want       string
	}{
		{"inside the legacy range lists the whole prefix", 10, 600, ""},
		{"one below the legacy edge lists the whole prefix", 599, 600, ""},
		{"at the legacy edge narrows", 600, 600, blobstore.SegmentKey("origin-b", 345)},
		{"past the legacy range narrows", 700, 600, blobstore.SegmentKey("origin-b", 445)},
		{"a mark below one segment narrows to the first key", 100, 0, blobstore.SegmentKey("origin-b", 0)},
		{"mark zero with no legacy narrows to the first key", 0, 0, blobstore.SegmentKey("origin-b", 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := listStartAfter("origin-b", tc.after, tc.legacyUpto); got != tc.want {
				t.Errorf("listStartAfter(%d, %d) = %q, want %q", tc.after, tc.legacyUpto, got, tc.want)
			}
		})
	}
}

// TestSelectSegmentKeys pins the selection: only this origin's segment keys,
// none past the ceiling, and from the last key that could hold the mark's
// successor -- a superset that stays correct for sparse legacy seqs. With no
// such key, all are kept.
func TestSelectSegmentKeys(t *testing.T) {
	obj := func(origin string, first int) blobstore.Object {
		return blobstore.Object{Key: blobstore.SegmentKey(origin, first)}
	}
	key := func(first int) string { return blobstore.SegmentKey("origin-b", first) }
	cases := []struct {
		name         string
		objs         []blobstore.Object
		after, upper int
		want         []string
	}{
		{
			name:  "keeps from the segment holding the mark's successor",
			objs:  []blobstore.Object{obj("origin-b", 1), obj("origin-b", 5), obj("origin-b", 9)},
			after: 5, upper: 12,
			want: []string{key(5), key(9)},
		},
		{
			name:  "a sparse legacy segment is kept even when its first is far below the mark",
			objs:  []blobstore.Object{obj("origin-b", 1), obj("origin-b", 100), obj("origin-b", 600)},
			after: 350, upper: 700,
			want: []string{key(100), key(600)},
		},
		{
			name:  "drops keys past the ceiling",
			objs:  []blobstore.Object{obj("origin-b", 1), obj("origin-b", 5), obj("origin-b", 9)},
			after: 0, upper: 7,
			want: []string{key(1), key(5)},
		},
		{
			name: "ignores stray objects and other origins' keys",
			objs: []blobstore.Object{
				{Key: "origin-b/log/not-a-key"},
				{Key: "origin-b/" + strings.Repeat("a", 64)},
				obj("origin-c", 1),
				obj("origin-b", 1),
			},
			after: 0, upper: 4,
			want: []string{key(1)},
		},
		{
			name:  "keeps all when no key could hold the mark's successor",
			objs:  []blobstore.Object{obj("origin-b", 9), obj("origin-b", 13)},
			after: 0, upper: 16,
			want: []string{key(9), key(13)},
		},
		{
			name:  "an empty listing selects nothing",
			objs:  nil,
			after: 0, upper: 4,
			want: nil,
		},
		{
			name:  "every key past the ceiling selects nothing",
			objs:  []blobstore.Object{obj("origin-b", 9)},
			after: 0, upper: 4,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectSegmentKeys(tc.objs, "origin-b", tc.after, tc.upper)
			if len(got) != len(tc.want) {
				t.Fatalf("selectSegmentKeys = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("key %d = %s, want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPullDrainsAWeekOfSegments pins the whole read over a backlog: a thousand
// segments of four entries drain in pages, every sequence once in order, no
// segment fetched twice, one listing per pull narrowed to the mark, and the
// byte counter reading exactly what the fetches moved.
func TestPullDrainsAWeekOfSegments(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)

	const segments = 1000
	const perSegment = 4
	segs := make([]seedSegment, segments)
	for i := range segs {
		segs[i] = seedSegment{entries: seedEntries("origin-b", i*perSegment+1, perSegment)}
	}
	const last = segments * perSegment
	seedOrigin(t, path, base, "origin-b", segs, seedHeads{
		seq: last, key: blobstore.SegmentKey("origin-b", last-perSegment+1), entries: last,
	})

	marks := map[string]int{}
	var got []Entry
	var wantStartAfter []string
	pulls, reads := 0, 0
	for {
		mark := marks["origin-b"]
		page, err := reader.Pull(marks)
		if err != nil {
			t.Fatalf("pull %d: %v", pulls+1, err)
		}
		pulls++
		if len(page) == 0 {
			break
		}
		// Only a pull that reads the origin lists: the last pull's mark
		// covers the head, so it answers without a listing.
		reads++
		wantStartAfter = append(wantStartAfter, blobstore.SegmentKey("origin-b", max(mark-255, 0)))
		got = append(got, page...)
		marks["origin-b"] = page[len(page)-1].Seq
		if pulls > 10 {
			t.Fatal("the backlog did not drain")
		}
	}
	if len(got) != last {
		t.Fatalf("pulled %d entries, want %d", len(got), last)
	}
	for i, e := range got {
		if e.Seq != i+1 || e.Origin != "origin-b" {
			t.Fatalf("entry %d = %s seq %d, want origin-b seq %d: every sequence once in order", i, e.Origin, e.Seq, i+1)
		}
	}
	if n := probe.gets.Load(); n != segments {
		t.Errorf("GET count = %d, want %d: no segment fetched twice", n, segments)
	}
	if n := probe.lists.Load(); int(n) != reads {
		t.Errorf("LIST count = %d, want %d: one per pull that reads the origin", n, reads)
	}
	calls := probe.listCalls()
	if len(calls) != reads {
		t.Fatalf("recorded %d listings, want %d", len(calls), reads)
	}
	for i, c := range calls {
		if c.prefix != "origin-b/log/" {
			t.Errorf("pull %d listed prefix %q, want origin-b/log/", i+1, c.prefix)
		}
		if c.startAfter != wantStartAfter[i] {
			t.Errorf("pull %d startAfter = %q, want %q", i+1, c.startAfter, wantStartAfter[i])
		}
	}
	stats, err := reader.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	objs, err := base.List(context.Background(), "origin-b/log/")
	if err != nil {
		t.Fatalf("list the seeded segments: %v", err)
	}
	var wantBytes int64
	for _, o := range objs {
		wantBytes += o.Size
	}
	if stats.R2Get != wantBytes {
		t.Errorf("R2Get = %d, want %d: the sum of the fetched object sizes", stats.R2Get, wantBytes)
	}
	t.Logf("drained %d entries in %d pulls: GET=%d LIST=%d R2Get=%d", len(got), pulls, probe.gets.Load(), probe.lists.Load(), stats.R2Get)
}

// TestPullSkipsAHalfAppliedBatchWhole pins the batch guard: a mark inside a
// batch skips the rest of that batch whole, and the next batch comes back.
// Handing back the marked batch's tail would write an order the exporter never
// produced.
func TestPullSkipsAHalfAppliedBatchWhole(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	entries := append(seedBatch("origin-b", 1, 1, 2, 3), seedBatch("origin-b", 5, 5, 6)...)
	seedOrigin(t, path, store, "origin-b",
		[]seedSegment{{entries: entries, legacy: true}},
		seedHeads{seq: 6, key: blobstore.SegmentKey("origin-b", 1), entries: 5, legacyUpto: 6})

	got, err := reader.Pull(map[string]int{"origin-b": 2})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	assertSeqs(t, got, 5, 6)
	for _, e := range got {
		if e.Batch != 5 {
			t.Errorf("entry %d carries batch %d, want the second batch only", e.Seq, e.Batch)
		}
	}
}

// TestPullListsTheWholePrefixInsideTheLegacyRange pins the listing's two
// shapes: a mark below legacy_upto lists the whole prefix, because legacy seqs
// are sparse and no key arithmetic can narrow them; a mark at the edge narrows
// to the segments that could hold its successor.
func TestPullListsTheWholePrefixInsideTheLegacyRange(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	legacy := append(seedBatch("origin-b", 1, 1), seedBatch("origin-b", 300, 300)...)
	legacy = append(legacy, seedBatch("origin-b", 600, 600)...)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: legacy, legacy: true}, {entries: seedEntries("origin-b", 601, 4)}},
		seedHeads{seq: 604, key: blobstore.SegmentKey("origin-b", 601), entries: 7, legacyUpto: 600})

	got, err := reader.Pull(map[string]int{"origin-b": 599})
	if err != nil {
		t.Fatalf("pull below the edge: %v", err)
	}
	assertSeqs(t, got, 600, 601, 602, 603, 604)
	got, err = reader.Pull(map[string]int{"origin-b": 600})
	if err != nil {
		t.Fatalf("pull at the edge: %v", err)
	}
	assertSeqs(t, got, 601, 602, 603, 604)

	calls := probe.listCalls()
	if len(calls) != 2 {
		t.Fatalf("recorded %d listings, want 2", len(calls))
	}
	if calls[0].startAfter != "" {
		t.Errorf("mark below legacy_upto listed after %q, want the whole prefix", calls[0].startAfter)
	}
	if want := blobstore.SegmentKey("origin-b", 345); calls[1].startAfter != want {
		t.Errorf("mark at legacy_upto listed after %q, want %q", calls[1].startAfter, want)
	}
}

// TestPullCompletesABatchCutByALegacyEdge pins that a batch a legacy segment
// ended inside at the page boundary comes back whole, and what the completion
// costs: the following legacy segments are fetched one at a time until one does
// not continue the batch, while a new segment at the boundary costs no GET --
// its batch starts at its own first sequence and cannot continue the cut one.
func TestPullCompletesABatchCutByALegacyEdge(t *testing.T) {
	// Four legacy segments fill the first group of four; the fourth ends
	// inside batch 7, so the page boundary cuts a batch.
	cut := []seedSegment{
		{entries: seedBatch("origin-b", 1, 1, 2), legacy: true},
		{entries: seedBatch("origin-b", 1, 3, 4), legacy: true},
		{entries: seedBatch("origin-b", 5, 5, 6), legacy: true},
		{entries: seedBatch("origin-b", 7, 7, 8), legacy: true},
	}
	cases := []struct {
		name       string
		fifth      seedSegment
		legacyUpto int
		seq        int
		wantSeqs   []int
		wantGets   int64
		wantCode   RefusalCode
		missing    bool
	}{
		{
			name:       "the cut batch comes back whole",
			fifth:      seedSegment{entries: seedBatch("origin-b", 7, 9, 10), legacy: true},
			legacyUpto: 10, seq: 10,
			wantSeqs: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, wantGets: 5,
		},
		{
			name:       "a new segment at the boundary costs no extra GET",
			fifth:      seedSegment{entries: seedEntries("origin-b", 9, 4)},
			legacyUpto: 8, seq: 12,
			wantSeqs: []int{1, 2, 3, 4, 5, 6, 7, 8}, wantGets: 4,
		},
		{
			name:       "a legacy segment starting a new batch stops the completion",
			fifth:      seedSegment{entries: seedBatch("origin-b", 9, 9, 10), legacy: true},
			legacyUpto: 10, seq: 10,
			wantSeqs: []int{1, 2, 3, 4, 5, 6, 7, 8}, wantGets: 5,
		},
		{
			name: "a segment ending the batch partway stops the completion",
			fifth: seedSegment{entries: append(seedBatch("origin-b", 7, 9), seedBatch("origin-b", 9, 10)...),
				legacy: true},
			legacyUpto: 10, seq: 10,
			wantSeqs: []int{1, 2, 3, 4, 5, 6, 7, 8, 9}, wantGets: 5,
		},
		{
			name:       "a missing continuation is a blob-missing refusal",
			fifth:      seedSegment{entries: seedBatch("origin-b", 7, 9, 10), legacy: true},
			legacyUpto: 10, seq: 10,
			wantCode: CodeBlobMissing, missing: true,
		},
		{
			name:       "a cut batch with no continuation comes back as it is",
			legacyUpto: 10, seq: 10,
			wantSeqs: []int{1, 2, 3, 4, 5, 6, 7, 8}, wantGets: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempReplica(t)
			base := blobstore.NewMemBlobStore(nil)
			probe := newProbeStore(base)
			reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
			reader.page = 3
			segs := cut
			if tc.fifth.entries != nil {
				segs = append(append([]seedSegment(nil), cut...), tc.fifth)
			}
			key := blobstore.SegmentKey("origin-b", 7)
			if tc.fifth.entries != nil {
				key = blobstore.SegmentKey("origin-b", tc.fifth.entries[0].Seq)
			}
			seedOrigin(t, path, base, "origin-b", segs,
				seedHeads{seq: tc.seq, key: key, entries: tc.seq, legacyUpto: tc.legacyUpto})
			if tc.missing {
				probe.missing[blobstore.SegmentKey("origin-b", 9)] = true
			}

			got, err := reader.Pull(nil)
			if tc.wantCode != "" {
				if err == nil {
					t.Fatalf("pull = %+v, want a %s refusal", got, tc.wantCode)
				}
				if code := RefusalCodeOf(err); code != tc.wantCode {
					t.Errorf("code = %q, want %q", code, tc.wantCode)
				}
				resp := askPullOverPipe(t, reader)
				if resp.OK || resp.Code != tc.wantCode {
					t.Errorf("pull over the pipe = %+v, want a %s refusal", resp, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			assertSeqs(t, got, tc.wantSeqs...)
			if n := probe.gets.Load(); n != tc.wantGets {
				t.Errorf("GET count = %d, want %d", n, tc.wantGets)
			}
		})
	}
}

// TestPullPagesWholeSegments pins the page bound in segments: with the default
// page, five full segments answer as exactly four segments' entries, and the
// next pull answers the rest. No segment is split across pages.
func TestPullPagesWholeSegments(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	segs := make([]seedSegment, 5)
	for i := range segs {
		segs[i] = seedSegment{entries: seedEntries("origin-b", i*SegmentMaxEntries+1, SegmentMaxEntries)}
	}
	seedOrigin(t, path, base, "origin-b", segs, seedHeads{
		seq: 5 * SegmentMaxEntries, key: blobstore.SegmentKey("origin-b", 4*SegmentMaxEntries+1),
		entries: 5 * SegmentMaxEntries,
	})

	got, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if len(got) != pullPageSize {
		t.Fatalf("first pull = %d entries, want exactly %d", len(got), pullPageSize)
	}
	if got[0].Seq != 1 || got[len(got)-1].Seq != pullPageSize {
		t.Errorf("first pull spans %d..%d, want 1..%d", got[0].Seq, got[len(got)-1].Seq, pullPageSize)
	}
	if n := probe.gets.Load(); n != 4 {
		t.Errorf("GET count after the first pull = %d, want 4: the page is four segments", n)
	}
	again, err := reader.Pull(map[string]int{"origin-b": pullPageSize})
	if err != nil {
		t.Fatalf("second pull: %v", err)
	}
	if len(again) != SegmentMaxEntries {
		t.Fatalf("second pull = %d entries, want the remaining %d", len(again), SegmentMaxEntries)
	}
	if again[0].Seq != pullPageSize+1 || again[len(again)-1].Seq != 5*SegmentMaxEntries {
		t.Errorf("second pull spans %d..%d, want %d..%d", again[0].Seq, again[len(again)-1].Seq, pullPageSize+1, 5*SegmentMaxEntries)
	}
	if n := probe.gets.Load(); n != 5 {
		t.Errorf("GET count after the second pull = %d, want 5", n)
	}
}

// TestPullReturnsEntriesAcrossAHole pins that a missing segment is not an error
// here: the entries come back as they are, with the sequence jump the importer
// reports a Gap on.
func TestPullReturnsEntriesAcrossAHole(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	seedOrigin(t, path, store, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}, {entries: seedEntries("origin-b", 9, 4)}},
		seedHeads{seq: 12, key: blobstore.SegmentKey("origin-b", 9), entries: 8})

	got, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull across a hole: %v", err)
	}
	assertSeqs(t, got, 1, 2, 3, 4, 9, 10, 11, 12)
	if jump := got[4].Seq - got[3].Seq; jump != 5 {
		t.Errorf("the hole reads as a jump of %d, want 5: the entries come back as they are", jump)
	}
}

// TestPullRefusesAListedSegmentTheStoreDoesNotHold pins the refusal a listing
// that promises a segment the store does not hold gets: the class the daemon
// latches on, naming the key, with nothing partial returned for any origin.
func TestPullRefusesAListedSegmentTheStoreDoesNotHold(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}, {entries: seedEntries("origin-b", 5, 4)}},
		seedHeads{seq: 8, key: blobstore.SegmentKey("origin-b", 5), entries: 8})
	seedOrigin(t, path, base, "origin-c",
		[]seedSegment{{entries: seedEntries("origin-c", 1, 2)}},
		seedHeads{seq: 2, key: blobstore.SegmentKey("origin-c", 1), entries: 2})
	key := blobstore.SegmentKey("origin-b", 5)
	probe.missing[key] = true

	got, err := reader.Pull(nil)
	if err == nil {
		t.Fatalf("pull = %+v, want a refusal", got)
	}
	if got != nil {
		t.Errorf("pull returned %+v alongside the refusal, want nothing partial for any origin", got)
	}
	if code := RefusalCodeOf(err); code != CodeBlobMissing {
		t.Errorf("code = %q, want %q", code, CodeBlobMissing)
	}
	if !strings.Contains(err.Error(), key) {
		t.Errorf("refusal %q does not name the missing key %s", err, key)
	}
	resp := askPullOverPipe(t, reader)
	if resp.OK || resp.Code != CodeBlobMissing {
		t.Errorf("pull over the pipe = %+v, want a %s refusal", resp, CodeBlobMissing)
	}
	if !strings.Contains(resp.Error, key) {
		t.Errorf("pipe refusal %q does not name the missing key %s", resp.Error, key)
	}
}

// TestPullRefusesAHeadWithNoSegment pins that a heads row past the mark with
// nothing to read refuses rather than stalling the origin silently: there is
// no entry to hand the importer to report a gap on, so the segment the row
// names is reported missing.
func TestPullRefusesAHeadWithNoSegment(t *testing.T) {
	cases := []struct {
		name     string
		segments []seedSegment
		heads    seedHeads
	}{
		{
			name:  "no segment object at all",
			heads: seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4},
		},
		{
			name:     "only objects past the head",
			segments: []seedSegment{{entries: seedEntries("origin-b", 9, 4)}},
			heads:    seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 9), entries: 4},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempReplica(t)
			store := blobstore.NewMemBlobStore(nil)
			reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
			seedOrigin(t, path, store, "origin-b", tc.segments, tc.heads)

			got, err := reader.Pull(nil)
			if err == nil {
				t.Fatalf("pull = %+v, want a refusal", got)
			}
			if code := RefusalCodeOf(err); code != CodeBlobMissing {
				t.Errorf("code = %q, want %q", code, CodeBlobMissing)
			}
			if !strings.Contains(err.Error(), tc.heads.key) {
				t.Errorf("refusal %q does not name the heads key %s", err, tc.heads.key)
			}
			resp := askPullOverPipe(t, reader)
			if resp.OK || resp.Code != CodeBlobMissing {
				t.Errorf("pull over the pipe = %+v, want a %s refusal", resp, CodeBlobMissing)
			}
			if !strings.Contains(resp.Error, tc.heads.key) {
				t.Errorf("pipe refusal %q does not name the heads key %s", resp.Error, tc.heads.key)
			}
		})
	}
}

// TestPullNeverReadsAnOrphanPastTheHead pins that an object whose first
// sequence is past the heads row -- an orphan of a lost compare-and-set -- is
// never fetched and its entries never returned.
func TestPullNeverReadsAnOrphanPastTheHead(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{
			{entries: seedEntries("origin-b", 1, 4)},
			{entries: seedEntries("origin-b", 5, 4)},
		},
		seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4})

	got, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	assertSeqs(t, got, 1, 2, 3, 4)
	if n := probe.gets.Load(); n != 1 {
		t.Errorf("GET count = %d, want 1: the orphan past the head is never fetched", n)
	}
}

// TestPullRefusesABadSegment pins that an object which is not a usable segment
// refuses with ErrBadSegment marked CodeRemote, naming the key it was fetched
// under: garbage bytes, another origin's segment under this origin's key, and
// a header whose first sequence is not the key's. The first error in key order
// is the one returned.
func TestPullRefusesABadSegment(t *testing.T) {
	cases := []struct {
		name  string
		seed  func(t *testing.T, store blobstore.BlobStore)
		heads seedHeads
	}{
		{
			name: "garbage bytes under a listed key",
			seed: func(t *testing.T, store blobstore.BlobStore) {
				garbage := []byte("not a segment at all")
				if err := store.Put(context.Background(), blobstore.SegmentKey("origin-b", 1),
					bytes.NewReader(garbage), int64(len(garbage))); err != nil {
					t.Fatalf("put garbage: %v", err)
				}
			},
			heads: seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4},
		},
		{
			name: "another origin's segment under this origin's key",
			seed: func(t *testing.T, store blobstore.BlobStore) {
				data, _, err := EncodeSegment("origin-c", seedEntries("origin-c", 1, 4))
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				if err := store.Put(context.Background(), blobstore.SegmentKey("origin-b", 1),
					bytes.NewReader(data), int64(len(data))); err != nil {
					t.Fatalf("put: %v", err)
				}
			},
			heads: seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4},
		},
		{
			name: "a header whose first is not the key's",
			seed: func(t *testing.T, store blobstore.BlobStore) {
				data, _, err := EncodeSegment("origin-b", seedEntries("origin-b", 5, 4))
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				if err := store.Put(context.Background(), blobstore.SegmentKey("origin-b", 1),
					bytes.NewReader(data), int64(len(data))); err != nil {
					t.Fatalf("put: %v", err)
				}
			},
			heads: seedHeads{seq: 8, key: blobstore.SegmentKey("origin-b", 1), entries: 4},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempReplica(t)
			store := blobstore.NewMemBlobStore(nil)
			reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
			tc.seed(t, store)
			seedOrigin(t, path, store, "origin-b", nil, tc.heads)

			_, err := reader.Pull(nil)
			if !errors.Is(err, ErrBadSegment) {
				t.Fatalf("err = %v, want ErrBadSegment", err)
			}
			if code := RefusalCodeOf(err); code != CodeRemote {
				t.Errorf("code = %q, want %q", code, CodeRemote)
			}
			if !strings.Contains(err.Error(), tc.heads.key) {
				t.Errorf("refusal %q does not name the key %s", err, tc.heads.key)
			}
			resp := askPullOverPipe(t, reader)
			if resp.OK || resp.Code != CodeRemote {
				t.Errorf("pull over the pipe = %+v, want a %s refusal", resp, CodeRemote)
			}
			if !strings.Contains(resp.Error, tc.heads.key) {
				t.Errorf("pipe refusal %q does not name the key %s", resp.Error, tc.heads.key)
			}
		})
	}
}

// TestPullRefusesTheFirstBadSegmentInKeyOrder pins that when several fetched
// segments fail, the error returned is the first in key order -- here a decode
// refusal ahead of a missing object, so the class is the decode's.
func TestPullRefusesTheFirstBadSegmentInKeyOrder(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	garbage := []byte("not a segment at all")
	first := blobstore.SegmentKey("origin-b", 1)
	if err := base.Put(context.Background(), first, bytes.NewReader(garbage), int64(len(garbage))); err != nil {
		t.Fatalf("put garbage: %v", err)
	}
	// The second key lists a real object the probe answers as missing, so both
	// keys are fetched and fail, each with a different class.
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 5, 4)}},
		seedHeads{seq: 8, key: first, entries: 8})
	probe.missing[blobstore.SegmentKey("origin-b", 5)] = true

	_, err := reader.Pull(nil)
	if !errors.Is(err, ErrBadSegment) {
		t.Fatalf("err = %v, want the first key's ErrBadSegment, not the later missing object", err)
	}
	if code := RefusalCodeOf(err); code != CodeRemote {
		t.Errorf("code = %q, want %q", code, CodeRemote)
	}
	if !strings.Contains(err.Error(), first) {
		t.Errorf("refusal %q does not name the first key %s", err, first)
	}
}

// TestPullGuardsTheHeadsItReads pins the guards that run before any segment key
// is built, because SegmentKey panics on what it cannot key: an origin no
// installation can have written is skipped while the rest still read, a
// sequence past the key range is refused, and the worker's own origin is never
// listed at all.
func TestPullGuardsTheHeadsItReads(t *testing.T) {
	t.Run("an origin that cannot be keyed is skipped", func(t *testing.T) {
		path := tempReplica(t)
		base := blobstore.NewMemBlobStore(nil)
		probe := newProbeStore(base)
		reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
		seedOrigin(t, path, base, "origin-b",
			[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}},
			seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4})
		seedOrigin(t, path, base, "bad/origin", nil,
			seedHeads{seq: 5, key: "bad/origin/log/000000000001", entries: 5})

		got, err := reader.Pull(nil)
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		assertSeqs(t, got, 1, 2, 3, 4)
		for _, c := range probe.listCalls() {
			if c.prefix != "origin-b/log/" {
				t.Errorf("listed prefix %q, want only origin-b/log/: the unkeyable origin is skipped", c.prefix)
			}
		}
	})
	t.Run("a sequence past the key range is refused", func(t *testing.T) {
		path := tempReplica(t)
		store := blobstore.NewMemBlobStore(nil)
		reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
		seedOrigin(t, path, store, "origin-c", nil,
			seedHeads{seq: 1000000000000, key: "origin-c/log/000000000001", entries: 1})

		_, err := reader.Pull(nil)
		if err == nil {
			t.Fatal("pull accepted a head whose sequence is past the key range")
		}
		if code := RefusalCodeOf(err); code != CodeRemote {
			t.Errorf("code = %q, want %q", code, CodeRemote)
		}
		if !strings.Contains(err.Error(), "1000000000000") {
			t.Errorf("refusal %q does not name the sequence it refused", err)
		}
		resp := askPullOverPipe(t, reader)
		if resp.OK || resp.Code != CodeRemote {
			t.Errorf("pull over the pipe = %+v, want a %s refusal", resp, CodeRemote)
		}
	})
	t.Run("the own origin's head is never listed", func(t *testing.T) {
		path := tempReplica(t)
		base := blobstore.NewMemBlobStore(nil)
		probe := newProbeStore(base)
		reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
		seedOrigin(t, path, base, "origin-a",
			[]seedSegment{{entries: seedEntries("origin-a", 1, 2)}},
			seedHeads{seq: 2, key: blobstore.SegmentKey("origin-a", 1), entries: 2})
		seedOrigin(t, path, base, "origin-b",
			[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}},
			seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4})

		got, err := reader.Pull(nil)
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		assertSeqs(t, got, 1, 2, 3, 4)
		for _, e := range got {
			if e.Origin != "origin-b" {
				t.Errorf("pull returned the own origin's entry %+v", e)
			}
		}
		for _, c := range probe.listCalls() {
			if c.prefix != "origin-b/log/" {
				t.Errorf("listed prefix %q, want only origin-b/log/: the own origin is never listed", c.prefix)
			}
		}
	})
}

// TestPullRefusesAHeadsRowItCannotScan pins that a heads row whose column types
// do not match heads is reported rather than read as a zero value.
func TestPullRefusesAHeadsRowItCannotScan(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	execOn(t, path, "INSERT INTO heads (origin, seq, key, entries, bytes, at)"+
		" VALUES ('origin-b', 'not a number', 'origin-b/log/000000000001', 1, 1, '2026-10-08T09:30:00Z')")
	if _, err := reader.Pull(nil); err == nil {
		t.Error("pull read a heads row whose sequence is not a number")
	}
}

// TestPullRefusesWhenTheListingFails pins that a listing the store cannot
// answer is reported, unmarked: a bucket that did not answer is a fault a
// retry can get past, not a refusal that repeats.
func TestPullRefusesWhenTheListingFails(t *testing.T) {
	path := tempReplica(t)
	base := blobstore.NewMemBlobStore(nil)
	probe := newProbeStore(base)
	reader := backendOnBlobStore(t, &localDriver{path: path}, "origin-a", probe)
	seedOrigin(t, path, base, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}},
		seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4})
	probe.listErr = errors.New("bucket unreachable")

	_, err := reader.Pull(nil)
	if err == nil || !strings.Contains(err.Error(), "bucket unreachable") {
		t.Fatalf("err = %v, want the listing's failure", err)
	}
	if code := RefusalCodeOf(err); code != "" {
		t.Errorf("code = %q, want none: a failed listing is left to a retry", code)
	}
}

// TestPullReturnsTheTimeInUTC pins that an entry stored with a zone of its own
// comes back in UTC: the MAC covers the UTC form, so a reader handing back the
// writer's zone would break every signature made over it.
func TestPullReturnsTheTimeInUTC(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	e := upsertEntry("origin-b", "board", `["t"]`, `{"title":"zoned"}`)
	e.Seq, e.Batch = 1, 1
	e.At = time.Date(2026, 10, 8, 11, 30, 0, 0, time.FixedZone("two hours out", 7200))
	seedOrigin(t, path, store, "origin-b", []seedSegment{{entries: []Entry{e}}},
		seedHeads{seq: 1, key: blobstore.SegmentKey("origin-b", 1), entries: 1})

	got, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("pull = %d entries, want 1", len(got))
	}
	if got[0].At.Location() != time.UTC {
		t.Errorf("the entry came back with the time in %v, want UTC", got[0].At.Location())
	}
	if !got[0].At.Equal(e.At) {
		t.Errorf("the entry came back at %v, want the same instant %v", got[0].At, e.At)
	}
}

// TestReadOriginAtTheHeadReadsNothing pins that a mark already at the head
// reads nothing and refuses nothing: there is no segment past the mark, and
// nothing missing either.
func TestReadOriginAtTheHeadReadsNothing(t *testing.T) {
	reader := backendOnStore(t, &localDriver{path: tempReplica(t)}, "origin-a", blobstore.NewMemBlobStore(nil))
	h := originHead{origin: "origin-b", key: blobstore.SegmentKey("origin-b", 1), seq: 4}
	got, err := reader.readOrigin(context.Background(), h, 4)
	if err != nil || len(got) != 0 {
		t.Errorf("readOrigin at the head = %+v, %v; want nothing and no refusal", got, err)
	}
}

// TestReadOriginHeadReadsOneOrigin pins the single-origin head read: the row's
// four fields as planted, one origin only, and false for an origin with no
// row.
func TestReadOriginHeadReadsOneOrigin(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	reader := backendOnStore(t, &localDriver{path: path}, "origin-a", store)
	seedOrigin(t, path, store, "origin-b",
		[]seedSegment{{entries: seedEntries("origin-b", 1, 4)}},
		seedHeads{seq: 4, key: blobstore.SegmentKey("origin-b", 1), entries: 4, legacyUpto: 2})
	seedOrigin(t, path, store, "origin-c",
		[]seedSegment{{entries: seedEntries("origin-c", 1, 2)}},
		seedHeads{seq: 2, key: blobstore.SegmentKey("origin-c", 1), entries: 2})

	h, ok, err := reader.readOriginHead(context.Background(), "origin-b")
	if err != nil || !ok {
		t.Fatalf("readOriginHead = %+v, %v, %v; want origin-b's row", h, ok, err)
	}
	want := originHead{origin: "origin-b", key: blobstore.SegmentKey("origin-b", 1), seq: 4, legacyUpto: 2}
	if h != want {
		t.Errorf("readOriginHead = %+v, want %+v", h, want)
	}
	if _, ok, err := reader.readOriginHead(context.Background(), "origin-x"); err != nil || ok {
		t.Errorf("readOriginHead of an origin with no row = %v, %v; want false and no error", ok, err)
	}
}
