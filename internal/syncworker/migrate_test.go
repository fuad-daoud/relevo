package syncworker

// The migration's tests: the format-5 reads and the legacy segment writer,
// each pinned against a seeded format-5 replica and the memfake through
// openStore. A probe wrapping the memfake injects the crash a re-run must
// recover from.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// seedFormatFiveLog inserts entries as format-5 log rows, the inverse of what
// readLogChunk reads back: a delete stores NULL body, an unsigned entry NULL
// mac, and an entry with no dependency NULL after columns. There is no older
// helper that writes full rows, so the tests seed through this one.
func seedFormatFiveLog(t *testing.T, path string, entries []Entry) {
	t.Helper()
	db := openAt(t, path)
	// One transaction for the whole seed: thousands of rows in autocommit
	// would each pay a sync, and only the committed log is what the test
	// reads back.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin the format-5 seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, e := range entries {
		var body any
		if e.Op == opUpsert {
			body = string(e.Body)
		}
		var mac any
		if len(e.MAC) > 0 {
			mac = e.MAC
		}
		var afterOrigin any
		if e.AfterOrigin != "" {
			afterOrigin = e.AfterOrigin
		}
		var afterSeq any
		if e.AfterSeq > 0 {
			afterSeq = e.AfterSeq
		}
		if _, err := tx.Exec(
			"INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			e.Origin, e.Seq, e.Batch, e.Tbl, e.PK, e.Op, e.SchemaVersion, body,
			e.At.UTC().Format(time.RFC3339Nano), mac, afterOrigin, afterSeq); err != nil {
			t.Fatalf("seed the format-5 log: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the format-5 seed: %v", err)
	}
}

// backendOnFormatFive opens a backend on the seeded format-5 replica at path
// for the migrate verb, sharing store through openStore. The migrate hello
// opens format 5 without pulling, so the log rows the test seeded are what
// the migration reads.
func backendOnFormatFive(t *testing.T, path string, store blobstore.BlobStore) *TursoBackend {
	t.Helper()
	b := NewTursoBackend(&localDriver{path: path})
	b.openStore = func(R2Config) blobstore.BlobStore { return store }
	if err := b.Open(Spec{
		Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://scratch.example",
		Token: "the-token", R2: &R2Config{Bucket: "bucket"}, Migrate: true,
	}); err != nil {
		t.Fatalf("open the format-5 replica for migration: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// legacyLogEntry is one format-5 row of the origin under test: a MAC on every
// fourth row, a dependency on the next, a delete on the next, so the writer
// is pinned carrying each shape verbatim rather than only plain upserts.
func legacyLogEntry(origin string, seq, batch int) Entry {
	e := upsertEntry(origin, "board", fmt.Sprintf(`["k%d"]`, seq), fmt.Sprintf(`{"title":"%d"}`, seq))
	e.Seq, e.Batch = seq, batch
	switch seq % 4 {
	case 1:
		e.MAC = []byte(fmt.Sprintf("mac-%d", seq))
	case 2:
		e.AfterOrigin, e.AfterSeq = "origin-a", seq-1
	case 3:
		e = Entry{Origin: origin, Seq: seq, Batch: batch, Tbl: "board",
			PK: fmt.Sprintf(`["k%d"]`, seq), Op: opDelete, SchemaVersion: 3, At: e.At}
	}
	return e
}

// legacyLogEntries builds n dense rows starting at first, one batch.
func legacyLogEntries(origin string, first, n int) []Entry {
	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, legacyLogEntry(origin, first+i, first))
	}
	return out
}

// assertMigratedEntries pins that got carries want field for field: every
// column the log held, the MAC bytes, the dependency, a delete's missing
// body, and the time in UTC.
func assertMigratedEntries(t *testing.T, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("migrated %d entries, want %d", len(got), len(want))
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.Origin != w.Origin || g.Seq != w.Seq || g.Batch != w.Batch ||
			g.Tbl != w.Tbl || g.PK != w.PK || g.Op != w.Op ||
			g.SchemaVersion != w.SchemaVersion ||
			string(g.Body) != string(w.Body) ||
			!g.At.Equal(w.At) || g.At.Location() != time.UTC ||
			!bytes.Equal(g.MAC, w.MAC) ||
			g.AfterOrigin != w.AfterOrigin || g.AfterSeq != w.AfterSeq {
			t.Errorf("entry %d = %+v, want %+v: the object must carry the log's row field for field", i, g, w)
		}
	}
}

// migrateObjectSizes lists the objects under the origin's log prefix as
// key to size, so a test states which keys a run wrote and what they cost.
func migrateObjectSizes(t *testing.T, store *blobstore.MemBlobStore, origin string) map[string]int64 {
	t.Helper()
	objs, err := store.List(context.Background(), origin+"/log/")
	if err != nil {
		t.Fatalf("list the migrated objects: %v", err)
	}
	out := make(map[string]int64, len(objs))
	for _, o := range objs {
		out[o.Key] = o.Size
	}
	return out
}

// migrateObjectBytes reads one object whole, so a test states that a refused
// or verified object kept its exact bytes.
func migrateObjectBytes(t *testing.T, store *blobstore.MemBlobStore, key string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := store.Get(context.Background(), key, &buf); err != nil {
		t.Fatalf("read object %s: %v", key, err)
	}
	return buf.Bytes()
}

// TestLogOriginMaxesGroupsByOrigin pins the migration's starting map: every
// origin's highest log sequence, and an empty map for a log with no rows.
func TestLogOriginMaxesGroupsByOrigin(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, append(
		legacyLogEntries("origin-b", 1, 3),
		legacyLogEntry("origin-c", 7, 7),
	))
	b := backendOnFormatFive(t, path, blobstore.NewMemBlobStore(nil))

	got, err := b.logOriginMaxes(context.Background())
	if err != nil {
		t.Fatalf("logOriginMaxes: %v", err)
	}
	if len(got) != 2 || got["origin-b"] != 3 || got["origin-c"] != 7 {
		t.Errorf("logOriginMaxes = %v, want origin-b 3 and origin-c 7", got)
	}

	empty := tempReplica(t)
	seedFormatFiveRemote(t, empty)
	eb := backendOnFormatFive(t, empty, blobstore.NewMemBlobStore(nil))
	got, err = eb.logOriginMaxes(context.Background())
	if err != nil {
		t.Fatalf("logOriginMaxes on an empty log: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("logOriginMaxes on an empty log = %v, want no origins", got)
	}
}

// TestReadLogChunkReadsFormatFiveRows pins the chunk read: pages in sequence
// order bounded by the limit, only the origin asked for, and every column
// mapped -- the MAC, the dependency, a delete's missing body, and a time
// stored out of UTC coming back in it.
func TestReadLogChunkReadsFormatFiveRows(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seeded := legacyLogEntries("origin-b", 1, 5)
	seedFormatFiveLog(t, path, append(seeded, legacyLogEntry("origin-c", 1, 1)))
	execOn(t, path, "UPDATE log SET at = '2026-10-08T11:30:00+02:00'"+
		" WHERE origin = 'origin-b' AND seq = 5")
	b := backendOnFormatFive(t, path, blobstore.NewMemBlobStore(nil))
	ctx := context.Background()

	first, err := b.readLogChunk(ctx, "origin-b", 0, 2)
	if err != nil {
		t.Fatalf("read the first chunk: %v", err)
	}
	assertMigratedEntries(t, first, seeded[:2])
	second, err := b.readLogChunk(ctx, "origin-b", 2, 2)
	if err != nil {
		t.Fatalf("read the second chunk: %v", err)
	}
	assertMigratedEntries(t, second, seeded[2:4])
	third, err := b.readLogChunk(ctx, "origin-b", 4, 2)
	if err != nil {
		t.Fatalf("read the last chunk: %v", err)
	}
	assertMigratedEntries(t, third, seeded[4:])
	if fourth, err := b.readLogChunk(ctx, "origin-b", 5, 2); err != nil || len(fourth) != 0 {
		t.Errorf("read past the end = %+v, %v; want nothing and no error", fourth, err)
	}
	other, err := b.readLogChunk(ctx, "origin-c", 0, 10)
	if err != nil {
		t.Fatalf("read the other origin: %v", err)
	}
	if len(other) != 1 || other[0].Origin != "origin-c" {
		t.Errorf("read of origin-c = %+v, want only its row", other)
	}
}

// TestReadLogChunkRefusesATimeItCannotParse pins that a log row whose time is
// not RFC3339 is reported rather than read as a zero time.
func TestReadLogChunkRefusesATimeItCannotParse(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 2))
	execOn(t, path, "UPDATE log SET at = 'not a time' WHERE origin = 'origin-b' AND seq = 2")
	b := backendOnFormatFive(t, path, blobstore.NewMemBlobStore(nil))

	if _, err := b.readLogChunk(context.Background(), "origin-b", 0, 10); err == nil {
		t.Error("read a log row whose time is not RFC3339")
	}
}

// TestMigrationReadsReportAnUnreadableLog pins that the reads report a log
// they cannot query: a replica with no log table fails the maxes, the chunk
// and the write alike rather than migrating nothing.
func TestMigrationReadsReportAnUnreadableLog(t *testing.T) {
	b := backendOnStore(t, &localDriver{path: tempReplica(t)}, "origin-a", blobstore.NewMemBlobStore(nil))
	ctx := context.Background()
	if _, err := b.logOriginMaxes(ctx); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("logOriginMaxes = %v, want the missing log table", err)
	}
	if _, err := b.readLogChunk(ctx, "origin-b", 0, 10); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("readLogChunk = %v, want the missing log table", err)
	}
	if _, err := b.writeLegacySegments(ctx, "origin-b", 0); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("writeLegacySegments = %v, want the missing log table", err)
	}
}

// TestWriteLegacySegmentsPacksFullChunks pins the whole write over 4096+10
// rows: two objects keyed by first sequence, each header legacy, the decoded
// entries equal to the seeded rows field for field, and bytes the sum of the
// store's object sizes.
func TestWriteLegacySegmentsPacksFullChunks(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seeded := legacyLogEntries("origin-b", 1, 4106)
	seedFormatFiveLog(t, path, seeded)
	store := blobstore.NewMemBlobStore(nil)
	b := backendOnFormatFive(t, path, store)

	got, err := b.writeLegacySegments(context.Background(), "origin-b", 4106)
	if err != nil {
		t.Fatalf("writeLegacySegments: %v", err)
	}
	if got.rows != 4106 || got.created != 2 || got.verified != 0 {
		t.Errorf("write = %+v, want 4106 rows, 2 created, 0 verified", got)
	}
	if got.last != 4106 || got.lastFirst != 4097 {
		t.Errorf("write = %+v, want last 4106, last first 4097", got)
	}
	firstKey, secondKey := blobstore.SegmentKey("origin-b", 1), blobstore.SegmentKey("origin-b", 4097)
	sizes := migrateObjectSizes(t, store, "origin-b")
	if len(sizes) != 2 || sizes[firstKey] == 0 || sizes[secondKey] == 0 {
		t.Fatalf("objects = %v, want exactly the keys %s and %s", sizes, firstKey, secondKey)
	}
	if want := sizes[firstKey] + sizes[secondKey]; got.bytes != want {
		t.Errorf("bytes = %d, want %d: the sum of the store's object sizes", got.bytes, want)
	}
	if got.putBytes != got.bytes || got.getBytes != 0 {
		t.Errorf("deltas = put %d, get %d; want put %d, get 0", got.putBytes, got.getBytes, got.bytes)
	}
	if b.bytes.R2Put != got.bytes || b.bytes.R2Get != 0 {
		t.Errorf("backend R2 = put %d, get %d; want put %d, get 0", b.bytes.R2Put, b.bytes.R2Get, got.bytes)
	}
	for _, tc := range []struct {
		key   string
		first int
		want  []Entry
	}{
		{firstKey, 1, seeded[:4096]},
		{secondKey, 4097, seeded[4096:]},
	} {
		header, entries, err := DecodeSegment(tc.key, migrateObjectBytes(t, store, tc.key))
		if err != nil {
			t.Fatalf("decode %s: %v", tc.key, err)
		}
		if !header.Legacy {
			t.Errorf("header of %s is not legacy", tc.key)
		}
		if header.First != tc.first || header.Count != len(tc.want) {
			t.Errorf("header of %s = %+v, want first %d, count %d", tc.key, header, tc.first, len(tc.want))
		}
		assertMigratedEntries(t, entries, tc.want)
	}
}

// TestWriteLegacySegmentsPreservesSparseSeqs pins that sparse sequences below
// a floor migrate verbatim: one object keyed by the first sequence, the gaps
// intact, and the last sequence the max.
func TestWriteLegacySegmentsPreservesSparseSeqs(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seqs := []int{3000, 3003, 3010, 3020, 3050, 3100, 3101, 3102, 3103, 3104, 3105, 3106, 3107, 3108, 3109, 3110}
	seeded := make([]Entry, 0, len(seqs))
	for i, seq := range seqs {
		seeded = append(seeded, legacyLogEntry("origin-b", seq, 3000+i))
	}
	seedFormatFiveLog(t, path, seeded)
	store := blobstore.NewMemBlobStore(nil)
	b := backendOnFormatFive(t, path, store)

	got, err := b.writeLegacySegments(context.Background(), "origin-b", 3110)
	if err != nil {
		t.Fatalf("writeLegacySegments: %v", err)
	}
	if got.rows != len(seqs) || got.created != 1 || got.last != 3110 || got.lastFirst != 3000 {
		t.Errorf("write = %+v, want %d rows, 1 created, last 3110, last first 3000", got, len(seqs))
	}
	key := blobstore.SegmentKey("origin-b", 3000)
	if sizes := migrateObjectSizes(t, store, "origin-b"); len(sizes) != 1 || sizes[key] == 0 {
		t.Fatalf("objects = %v, want exactly the key %s", sizes, key)
	}
	header, entries, err := DecodeSegment(key, migrateObjectBytes(t, store, key))
	if err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	if !header.Legacy || header.First != 3000 || header.Count != len(seqs) {
		t.Errorf("header = %+v, want legacy, first 3000, count %d", header, len(seqs))
	}
	assertMigratedEntries(t, entries, seeded)
}

// failPutStore fails one PutIfAbsent call by count, so a test crashes the
// migration after the first object while the clean retry shares the same
// memfake underneath.
type failPutStore struct {
	blobstore.BlobStore
	calls  atomic.Int64
	failAt int64
	err    error
}

func (s *failPutStore) PutIfAbsent(ctx context.Context, key string, body io.Reader, size int64) (bool, error) {
	if s.calls.Add(1) == s.failAt {
		return false, s.err
	}
	return s.BlobStore.PutIfAbsent(ctx, key, body, size)
}

// TestWriteLegacySegmentsResumesAfterACrash pins the create-only re-run: the
// crash's error stops the run, and the clean second call creates only the
// remainder -- the first object verifies byte for byte instead, its bytes
// unchanged, and the second call's R2Put counts only the new object.
func TestWriteLegacySegmentsResumesAfterACrash(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 4106))
	base := blobstore.NewMemBlobStore(nil)
	probe := &failPutStore{BlobStore: base, failAt: 2, err: errors.New("bucket unreachable")}
	b := backendOnFormatFive(t, path, probe)
	ctx := context.Background()
	firstKey, secondKey := blobstore.SegmentKey("origin-b", 1), blobstore.SegmentKey("origin-b", 4097)

	if _, err := b.writeLegacySegments(ctx, "origin-b", 4106); err == nil {
		t.Fatal("the crashed run reported no error")
	}
	before := migrateObjectBytes(t, base, firstKey)
	put, get := b.bytes.R2Put, b.bytes.R2Get

	got, err := b.writeLegacySegments(ctx, "origin-b", 4106)
	if err != nil {
		t.Fatalf("the clean re-run: %v", err)
	}
	if got.rows != 4106 || got.created != 1 || got.verified != 1 {
		t.Errorf("re-run = %+v, want 4106 rows, 1 created, 1 verified", got)
	}
	if got.last != 4106 || got.lastFirst != 4097 {
		t.Errorf("re-run = %+v, want last 4106, last first 4097", got)
	}
	if after := migrateObjectBytes(t, base, firstKey); !bytes.Equal(after, before) {
		t.Error("the re-run changed the first object's bytes")
	}
	sizes := migrateObjectSizes(t, base, "origin-b")
	if len(sizes) != 2 || sizes[firstKey] == 0 || sizes[secondKey] == 0 {
		t.Fatalf("objects = %v, want the first object kept and the remainder created", sizes)
	}
	if d := b.bytes.R2Put - put; d != sizes[secondKey] {
		t.Errorf("the re-run's R2Put = %d, want %d: only the new object", d, sizes[secondKey])
	}
	if d := b.bytes.R2Get - get; d != sizes[firstKey] {
		t.Errorf("the re-run's R2Get = %d, want %d: the re-verified first object", d, sizes[firstKey])
	}
	if got.putBytes != sizes[secondKey] || got.getBytes != sizes[firstKey] {
		t.Errorf("re-run deltas = put %d, get %d; want put %d, get %d",
			got.putBytes, got.getBytes, sizes[secondKey], sizes[firstKey])
	}
}

// failGetStore fails every Get, so a test holds a fetch the migration cannot
// finish: the key exists, but the object never arrives.
type failGetStore struct {
	blobstore.BlobStore
	err error
}

func (s *failGetStore) Get(context.Context, string, io.Writer) (int64, error) {
	return 0, s.err
}

// TestWriteLegacySegmentsStopsWhenAGetFails pins that a store error stops the
// run with the error: the objects written so far stay valid for the next run,
// and the failure is left unmarked for a retry rather than refused.
func TestWriteLegacySegmentsStopsWhenAGetFails(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
	base := blobstore.NewMemBlobStore(nil)
	if _, err := backendOnFormatFive(t, path, base).writeLegacySegments(context.Background(), "origin-b", 10); err != nil {
		t.Fatalf("seed the object: %v", err)
	}
	b := backendOnFormatFive(t, path, &failGetStore{BlobStore: base, err: errors.New("bucket unreachable")})

	_, err := b.writeLegacySegments(context.Background(), "origin-b", 10)
	if err == nil {
		t.Fatal("the run reported no error for the failed fetch")
	}
	if code := RefusalCodeOf(err); code != "" {
		t.Errorf("code = %q, want none: a failed fetch is left to a retry", code)
	}
}

// TestWriteLegacySegmentsRefusesADifferingObject pins that an existing key
// whose bytes are not the log's rows refuses with the key named: garbage, a
// new-style segment, a longer legacy one, one of the same length, and one
// that matches only part of the planned prefix. The object is untouched.
func TestWriteLegacySegmentsRefusesADifferingObject(t *testing.T) {
	garbage := []byte("not a segment at all")
	otherEntries := func(first, n int) []Entry {
		out := make([]Entry, 0, n)
		for i := 0; i < n; i++ {
			seq := first + i
			e := upsertEntry("origin-b", "board", fmt.Sprintf(`["other%d"]`, seq), `{"title":"elsewhere"}`)
			e.Seq, e.Batch = seq, first
			out = append(out, e)
		}
		return out
	}
	encode := func(t *testing.T, legacy bool, entries []Entry) []byte {
		t.Helper()
		var data []byte
		var err error
		if legacy {
			data, _, err = EncodeLegacySegment("origin-b", entries)
		} else {
			data, _, err = EncodeSegment("origin-b", entries)
		}
		if err != nil {
			t.Fatalf("encode the differing object: %v", err)
		}
		return data
	}
	cases := []struct {
		name string
		seed func(t *testing.T) []byte
	}{
		{"garbage under the key", func(t *testing.T) []byte { return garbage }},
		{"a new-style segment under the key", func(t *testing.T) []byte {
			return encode(t, false, seedEntries("origin-b", 1, 4))
		}},
		{"a longer legacy segment under the key", func(t *testing.T) []byte {
			return encode(t, true, otherEntries(1, 20))
		}},
		{"a same-length legacy segment under the key", func(t *testing.T) []byte {
			return encode(t, true, otherEntries(1, 10))
		}},
		{"a legacy segment matching only part of the prefix", func(t *testing.T) []byte {
			return encode(t, true, otherEntries(1, 5))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempReplica(t)
			seedFormatFiveRemote(t, path)
			seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
			store := blobstore.NewMemBlobStore(nil)
			key := blobstore.SegmentKey("origin-b", 1)
			before := tc.seed(t)
			if err := store.Put(context.Background(), key, bytes.NewReader(before), int64(len(before))); err != nil {
				t.Fatalf("plant the differing object: %v", err)
			}
			b := backendOnFormatFive(t, path, store)

			_, err := b.writeLegacySegments(context.Background(), "origin-b", 10)
			if err == nil {
				t.Fatal("the run accepted an object that differs from the log's rows")
			}
			if code := RefusalCodeOf(err); code != CodeRemote {
				t.Errorf("code = %q, want %q", code, CodeRemote)
			}
			for _, want := range []string{key, "differs from the log's rows", "was not touched"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not contain %q", err, want)
				}
			}
			if after := migrateObjectBytes(t, store, key); !bytes.Equal(after, before) {
				t.Error("the refused object was changed")
			}
			if n := len(migrateObjectSizes(t, store, "origin-b")); n != 1 {
				t.Errorf("the store holds %d objects, want only the refused one", n)
			}
		})
	}
}

// TestWriteLegacySegmentsExtendsAShortTail pins the extension: a segment
// written from 4106 rows, then 20 rows more. The second call verifies the
// 10-entry tail as the planned chunk's prefix and writes the 20 new rows as
// a new object at the next sequence, and the entries across all three objects
// equal the log.
func TestWriteLegacySegmentsExtendsAShortTail(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 4106))
	store := blobstore.NewMemBlobStore(nil)
	b := backendOnFormatFive(t, path, store)
	ctx := context.Background()

	if _, err := b.writeLegacySegments(ctx, "origin-b", 4106); err != nil {
		t.Fatalf("migrate the first 4106 rows: %v", err)
	}
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 4107, 20))
	got, err := b.writeLegacySegments(ctx, "origin-b", 4126)
	if err != nil {
		t.Fatalf("migrate the longer log: %v", err)
	}
	if got.rows != 4126 || got.created != 1 || got.verified != 2 {
		t.Errorf("second run = %+v, want 4126 rows, 1 created, 2 verified", got)
	}
	if got.last != 4126 || got.lastFirst != 4107 {
		t.Errorf("second run = %+v, want last 4126, last first 4107", got)
	}
	keys := []string{
		blobstore.SegmentKey("origin-b", 1),
		blobstore.SegmentKey("origin-b", 4097),
		blobstore.SegmentKey("origin-b", 4107),
	}
	if sizes := migrateObjectSizes(t, store, "origin-b"); len(sizes) != len(keys) {
		t.Fatalf("objects = %v, want exactly %v", sizes, keys)
	}
	var across []Entry
	for _, key := range keys {
		header, entries, err := DecodeSegment(key, migrateObjectBytes(t, store, key))
		if err != nil {
			t.Fatalf("decode %s: %v", key, err)
		}
		if !header.Legacy {
			t.Errorf("header of %s is not legacy", key)
		}
		across = append(across, entries...)
	}
	want := append(legacyLogEntries("origin-b", 1, 4106), legacyLogEntries("origin-b", 4107, 20)...)
	assertMigratedEntries(t, across, want)
}

// TestWriteLegacySegmentsRefusesBeforeAnyPut pins the up-front refusals: an
// origin that cannot be keyed, and a sequence past the key range, each refuse
// with CodeRemote before any object exists.
func TestWriteLegacySegmentsRefusesBeforeAnyPut(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 2))
	store := blobstore.NewMemBlobStore(nil)
	b := backendOnFormatFive(t, path, store)
	ctx := context.Background()

	if _, err := b.writeLegacySegments(ctx, "bad/origin", 0); err == nil {
		t.Error("migrated an origin that cannot be keyed")
	} else if code := RefusalCodeOf(err); code != CodeRemote {
		t.Errorf("code = %q, want %q", code, CodeRemote)
	} else if !strings.Contains(err.Error(), "bad/origin") {
		t.Errorf("refusal %q does not name the origin it refused", err)
	}
	if _, err := b.writeLegacySegments(ctx, "origin-b", maxSegmentSeq+1); err == nil {
		t.Error("migrated a sequence past the key range")
	} else if code := RefusalCodeOf(err); code != CodeRemote {
		t.Errorf("code = %q, want %q", code, CodeRemote)
	}
	if n := len(migrateObjectSizes(t, store, "origin-b")); n != 0 {
		t.Errorf("the store holds %d objects, want none: the refusals run before any put", n)
	}
	if b.bytes != (Stats{}) {
		t.Errorf("backend R2 = %+v, want nothing moved before the refusal", b.bytes)
	}
}

// TestWriteLegacySegmentsWritesNothingForAnEmptyOrigin pins that an origin
// with no rows writes nothing: a zero write, no objects, no R2 deltas.
func TestWriteLegacySegmentsWritesNothingForAnEmptyOrigin(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 2))
	store := blobstore.NewMemBlobStore(nil)
	b := backendOnFormatFive(t, path, store)

	got, err := b.writeLegacySegments(context.Background(), "origin-c", 0)
	if err != nil {
		t.Fatalf("writeLegacySegments: %v", err)
	}
	if got != (originWrite{}) {
		t.Errorf("write = %+v, want a zero write for an origin with no rows", got)
	}
	if n := len(migrateObjectSizes(t, store, "origin-b")); n != 0 {
		t.Errorf("the store holds %d objects, want none", n)
	}
	if b.bytes != (Stats{}) {
		t.Errorf("backend R2 = %+v, want nothing moved for an origin with no rows", b.bytes)
	}
}

// TestDropBlockers pins the one rule for what may drop: the origins, sorted,
// whose log max exceeds what the segments cover -- including an origin with
// rows and no coverage, and excluding one the log no longer names.
func TestDropBlockers(t *testing.T) {
	cases := []struct {
		name    string
		logMax  map[string]int
		covered map[string]int
		want    []string
	}{
		{"no origins block nothing", nil, nil, nil},
		{"a covered origin does not block", map[string]int{"a": 4}, map[string]int{"a": 4}, nil},
		{"cover past the max does not block", map[string]int{"a": 4}, map[string]int{"a": 9}, nil},
		{"a log past the cover blocks", map[string]int{"a": 5}, map[string]int{"a": 4}, []string{"a"}},
		{"an origin with rows and no coverage blocks",
			map[string]int{"a": 5}, map[string]int{}, []string{"a"}},
		{"an origin the log no longer names does not block",
			map[string]int{}, map[string]int{"a": 5}, nil},
		{"blockers come back sorted", map[string]int{"b": 2, "a": 3, "c": 1},
			map[string]int{}, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dropBlockers(tc.logMax, tc.covered)
			if len(got) != len(tc.want) {
				t.Fatalf("dropBlockers(%v, %v) = %v, want %v", tc.logMax, tc.covered, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("blocker %d = %s, want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}
