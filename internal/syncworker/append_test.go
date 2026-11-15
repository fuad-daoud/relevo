package syncworker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

type hookedStore struct {
	blobstore.BlobStore
	beforePutIfAbsentOnce func(ctx context.Context, key string)
	beforePutIfAbsent     func(ctx context.Context, key string)
	failPutIfAbsent       error
	failGet               error
}

func (h *hookedStore) PutIfAbsent(ctx context.Context, key string, body io.Reader, size int64) (bool, error) {
	if h.failPutIfAbsent != nil {
		return false, h.failPutIfAbsent
	}
	if h.beforePutIfAbsentOnce != nil {
		fn := h.beforePutIfAbsentOnce
		h.beforePutIfAbsentOnce = nil
		fn(ctx, key)
	}
	if h.beforePutIfAbsent != nil {
		h.beforePutIfAbsent(ctx, key)
	}
	return h.BlobStore.PutIfAbsent(ctx, key, body, size)
}

func (h *hookedStore) Get(ctx context.Context, key string, w io.Writer) (int64, error) {
	if h.failGet != nil {
		return 0, h.failGet
	}
	return h.BlobStore.Get(ctx, key, w)
}

// TestAppendWritesOneSegmentAndMovesHeads pins that a batch of entries produces
// exactly one segment object, numbers sequence contiguous from the replica's
// high-water mark, and records the new head and byte count in heads and log.
// Mutant: stop writing heads.
func TestAppendWritesOneSegmentAndMovesHeads(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b, store := backendWithStore(t, driver, "origin-a")

	e1 := upsertEntry("origin-a", "board", `["b1"]`, `{"title":"one"}`)
	e2 := upsertEntry("origin-a", "board", `["b2"]`, `{"title":"two"}`)
	written, err := b.Append([]Entry{e1, e2})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if len(written) != 2 || written[0].Seq != 1 || written[1].Seq != 2 {
		t.Fatalf("written = %+v, want seqs 1 and 2", written)
	}

	key1 := blobstore.SegmentKey("origin-a", 1)
	var buf bytes.Buffer
	n1, err := store.Get(context.Background(), key1, &buf)
	if err != nil {
		t.Fatalf("get segment %s: %v", key1, err)
	}
	header, entries, err := DecodeSegment(key1, buf.Bytes())
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	if header.Count != 2 || header.First != 1 || header.Last != 2 || len(entries) != 2 {
		t.Fatalf("decoded header = %+v, entries = %d", header, len(entries))
	}

	db := openAt(t, driver.path)
	var seq, entriesCount, bytesCount, floor int
	var headKey string
	if err := db.QueryRow("SELECT seq, key, entries, bytes, floor FROM heads WHERE origin = 'origin-a'").
		Scan(&seq, &headKey, &entriesCount, &bytesCount, &floor); err != nil {
		t.Fatalf("read heads: %v", err)
	}
	if seq != 2 || headKey != key1 || entriesCount != 2 || bytesCount != int(n1) || floor != 0 {
		t.Errorf("heads = (seq %d, key %s, entries %d, bytes %d, floor %d), want (2, %s, 2, %d, 0)",
			seq, headKey, entriesCount, bytesCount, floor, key1, n1)
	}

	logRows := snapshotLog(t, driver.path)
	if len(logRows) != 2 || logRows[0].seq != 1 || logRows[1].seq != 2 {
		t.Errorf("log rows = %+v, want 2 rows", logRows)
	}

	e3 := upsertEntry("origin-a", "board", `["b3"]`, `{"title":"three"}`)
	written2, err := b.Append([]Entry{e3})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if len(written2) != 1 || written2[0].Seq != 3 {
		t.Fatalf("second written = %+v, want seq 3", written2)
	}

	key3 := blobstore.SegmentKey("origin-a", 3)
	var buf3 bytes.Buffer
	n3, err := store.Get(context.Background(), key3, &buf3)
	if err != nil {
		t.Fatalf("get second segment %s: %v", key3, err)
	}
	if err := db.QueryRow("SELECT seq, key, entries, bytes FROM heads WHERE origin = 'origin-a'").
		Scan(&seq, &headKey, &entriesCount, &bytesCount); err != nil {
		t.Fatalf("read heads after second append: %v", err)
	}
	if seq != 3 || headKey != key3 || entriesCount != 3 || bytesCount != int(n1+n3) {
		t.Errorf("heads after second append = (seq %d, key %s, entries %d, bytes %d), want (3, %s, 3, %d)",
			seq, headKey, entriesCount, bytesCount, key3, n1+n3)
	}
}

// TestAppendAdoptsOrphanFromCrashedPut pins that a segment object present at
// the expected key without a matching heads row is adopted and committed before
// the retried export is numbered and written.
// Mutant: skip the adoption commit.
func TestAppendAdoptsOrphanFromCrashedPut(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b, store := backendWithStore(t, driver, "origin-a")

	orphanEntries := []Entry{
		{
			Origin: "origin-a", Seq: 1, Batch: 1, Tbl: "board", PK: `["b1"]`,
			Op: opUpsert, SchemaVersion: 1, Body: json.RawMessage(`{"title":"one"}`),
			At: time.Now().UTC(),
		},
		{
			Origin: "origin-a", Seq: 2, Batch: 1, Tbl: "board", PK: `["b2"]`,
			Op: opUpsert, SchemaVersion: 1, Body: json.RawMessage(`{"title":"two"}`),
			At: time.Now().UTC(),
		},
	}
	orphanData, _, err := EncodeSegment("origin-a", orphanEntries)
	if err != nil {
		t.Fatalf("encode orphan: %v", err)
	}
	key1 := blobstore.SegmentKey("origin-a", 1)
	if err := store.Put(context.Background(), key1, bytes.NewReader(orphanData), int64(len(orphanData))); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	reply, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b3"]`, `{"title":"three"}`)})
	if err != nil {
		t.Fatalf("append with orphan: %v", err)
	}
	if len(reply) != 1 || reply[0].Seq != 3 {
		t.Fatalf("reply = %+v, want 1 entry with seq 3", reply)
	}

	key3 := blobstore.SegmentKey("origin-a", 3)
	for _, k := range []string{key1, key3} {
		has, err := store.Has(context.Background(), k)
		if err != nil || !has {
			t.Errorf("store has %s = %v (%v), want true", k, has, err)
		}
	}

	db := openAt(t, driver.path)
	var seq, entriesCount int
	if err := db.QueryRow("SELECT seq, entries FROM heads WHERE origin = 'origin-a'").Scan(&seq, &entriesCount); err != nil {
		t.Fatalf("read heads: %v", err)
	}
	if seq != 3 || entriesCount != 3 {
		t.Errorf("heads = (seq %d, entries %d), want (3, 3)", seq, entriesCount)
	}

	logRows := snapshotLog(t, driver.path)
	if len(logRows) != 3 || logRows[0].seq != 1 || logRows[1].seq != 2 || logRows[2].seq != 3 {
		t.Errorf("log rows = %+v, want rows 1, 2, 3", logRows)
	}
}

// TestAppendRefusesBadOrphanAdoption pins that an orphan object whose header
// origin or first sequence disagrees with the key refuses with ErrBadSegment
// marked CodeRemote and leaves the replica and store untouched.
func TestAppendRefusesBadOrphanAdoption(t *testing.T) {
	t.Run("foreign origin", func(t *testing.T) {
		driver := &localDriver{path: tempReplica(t)}
		b, store := backendWithStore(t, driver, "origin-a")
		driver.pushes = 0

		e := upsertEntry("other-origin", "board", `["b"]`, `{"title":"one"}`)
		e.Seq = 1
		e.Batch = 1
		data, _, err := EncodeSegment("other-origin", []Entry{e})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		key := blobstore.SegmentKey("origin-a", 1)
		if err := store.Put(context.Background(), key, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("put: %v", err)
		}

		_, err = b.Append([]Entry{upsertEntry("origin-a", "board", `["mine"]`, `{"title":"mine"}`)})
		if !errors.Is(err, ErrBadSegment) {
			t.Fatalf("err = %v, want ErrBadSegment", err)
		}
		if code := RefusalCodeOf(err); code != CodeRemote {
			t.Errorf("refusal code = %q, want %q", code, CodeRemote)
		}

		assertEmptyReplica(t, driver)
		if driver.pushes != 0 {
			t.Errorf("driver pushed %d times, want 0", driver.pushes)
		}
	})

	t.Run("wrong first sequence", func(t *testing.T) {
		driver := &localDriver{path: tempReplica(t)}
		b, store := backendWithStore(t, driver, "origin-a")
		driver.pushes = 0

		e := upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)
		e.Seq = 5
		e.Batch = 5
		data, _, err := EncodeSegment("origin-a", []Entry{e})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		key := blobstore.SegmentKey("origin-a", 1)
		if err := store.Put(context.Background(), key, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("put: %v", err)
		}

		_, err = b.Append([]Entry{upsertEntry("origin-a", "board", `["mine"]`, `{"title":"mine"}`)})
		if !errors.Is(err, ErrBadSegment) {
			t.Fatalf("err = %v, want ErrBadSegment", err)
		}
		if code := RefusalCodeOf(err); code != CodeRemote {
			t.Errorf("refusal code = %q, want %q", code, CodeRemote)
		}

		assertEmptyReplica(t, driver)
		if driver.pushes != 0 {
			t.Errorf("driver pushed %d times, want 0", driver.pushes)
		}
	})
}

// TestAppendRacesAnotherWriterAndPulls pins that when PutIfAbsent loses the race
// to an existing segment, the losing backend adopts the orphan, pulls, and retries.
// Mutant: drop AND seq = ? from the update.
func TestAppendRacesAnotherWriterAndPulls(t *testing.T) {
	path := tempReplica(t)
	driverA := &localDriver{path: path}
	driverB := &localDriver{path: path}

	baseStore := blobstore.NewMemBlobStore(nil)
	var bA *TursoBackend
	hooked := &hookedStore{
		BlobStore: baseStore,
		beforePutIfAbsentOnce: func(ctx context.Context, key string) {
			// Writer A runs first inside B's PutIfAbsent hook.
			_, err := bA.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"A"}`)})
			if err != nil {
				t.Errorf("writer A append: %v", err)
			}
		},
	}

	spec := Spec{
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://scratch.example",
		Token:   "the-token",
		R2:      &R2Config{Bucket: "bucket"},
	}

	// Open backend A with the plain baseStore.
	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return baseStore }
	bA = NewTursoBackend(driverA)
	if err := bA.Open(spec); err != nil {
		t.Fatalf("open A: %v", err)
	}
	t.Cleanup(func() { _ = bA.Close() })

	// Open backend B with the hooked store.
	newBlobStore = func(R2Config) blobstore.BlobStore { return hooked }
	t.Cleanup(func() { newBlobStore = restore })

	bB := NewTursoBackend(driverB)
	if err := bB.Open(spec); err != nil {
		t.Fatalf("open B: %v", err)
	}
	t.Cleanup(func() { _ = bB.Close() })

	writtenB, err := bB.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"B"}`)})
	if err != nil {
		t.Fatalf("writer B append: %v", err)
	}
	if len(writtenB) != 1 || writtenB[0].Seq != 2 {
		t.Fatalf("written B = %+v, want seq 2", writtenB)
	}

	key1 := blobstore.SegmentKey("origin-a", 1)
	key2 := blobstore.SegmentKey("origin-a", 2)
	for _, k := range []string{key1, key2} {
		has, err := baseStore.Has(context.Background(), k)
		if err != nil || !has {
			t.Errorf("store has %s = %v (%v), want true", k, has, err)
		}
	}

	db := openAt(t, path)
	var headsSeq int
	if err := db.QueryRow("SELECT seq FROM heads WHERE origin = 'origin-a'").Scan(&headsSeq); err != nil {
		t.Fatalf("read heads: %v", err)
	}
	if headsSeq != 2 {
		t.Errorf("heads seq = %d, want 2", headsSeq)
	}

	rows := snapshotLog(t, path)
	if len(rows) != 2 || rows[0].seq != 1 || rows[1].seq != 2 {
		t.Errorf("log rows = %+v, want seq 1 and 2 with no duplicates", rows)
	}
	if driverB.pulls != 1 {
		t.Errorf("driver B pulled %d times, want 1", driverB.pulls)
	}

	// Race again when heads already exists: this tests compare-and-set on the UPDATE path.
	hooked.beforePutIfAbsentOnce = func(ctx context.Context, key string) {
		_, err := bA.Append([]Entry{upsertEntry("origin-a", "board", `["a2"]`, `{"title":"A2"}`)})
		if err != nil {
			t.Errorf("writer A second append: %v", err)
		}
	}
	writtenB2, err := bB.Append([]Entry{upsertEntry("origin-a", "board", `["b2"]`, `{"title":"B2"}`)})
	if err != nil {
		t.Fatalf("writer B second append: %v", err)
	}
	if len(writtenB2) != 1 || writtenB2[0].Seq != 4 {
		t.Fatalf("written B2 = %+v, want seq 4", writtenB2)
	}
	if err := db.QueryRow("SELECT seq FROM heads WHERE origin = 'origin-a'").Scan(&headsSeq); err != nil {
		t.Fatalf("read heads: %v", err)
	}
	if headsSeq != 4 {
		t.Errorf("heads seq = %d, want 4", headsSeq)
	}
	rows = snapshotLog(t, path)
	if len(rows) != 4 {
		t.Errorf("log rows count = %d, want 4", len(rows))
	}
	if driverB.pulls != 2 {
		t.Errorf("driver B pulled %d times, want 2", driverB.pulls)
	}
}

// TestAppendFailsWhenHeadMovesTwice pins that when the compare-and-set loses
// twice in one export, append returns errHeadMoved without writing log rows.
func TestAppendFailsWhenHeadMovesTwice(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	baseStore := blobstore.NewMemBlobStore(nil)

	hooked := &hookedStore{
		BlobStore: baseStore,
		beforePutIfAbsent: func(ctx context.Context, key string) {
			db := openAt(t, driver.path)
			var count int
			_ = db.QueryRow("SELECT COUNT(*) FROM heads WHERE origin = 'origin-a'").Scan(&count)
			if count == 0 {
				_, _ = db.Exec("INSERT INTO heads (origin, seq, key, entries, bytes, at) VALUES ('origin-a', 99, 'k', 1, 10, '2026-11-15T00:00:00Z')")
			} else {
				_, _ = db.Exec("UPDATE heads SET seq = seq + 10 WHERE origin = 'origin-a'")
			}
		},
	}

	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return hooked }
	t.Cleanup(func() { newBlobStore = restore })

	b := NewTursoBackend(driver)
	if err := b.Open(Spec{
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://scratch.example",
		Token:   "the-token",
		R2:      &R2Config{Bucket: "bucket"},
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	driver.pushes = 0

	_, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"b"}`)})
	if !errors.Is(err, errHeadMoved) {
		t.Fatalf("err = %v, want errHeadMoved", err)
	}

	var logCount int
	if err := openAt(t, driver.path).QueryRow("SELECT COUNT(*) FROM log").Scan(&logCount); err != nil {
		t.Fatalf("read log: %v", err)
	}
	if logCount != 0 {
		t.Errorf("log count = %d, want 0", logCount)
	}
	if driver.pulls != 1 {
		t.Errorf("driver pulled %d times, want 1", driver.pulls)
	}
	if driver.pushes != 0 {
		t.Errorf("pushes = %d, want 0", driver.pushes)
	}
}

// TestAppendRefusesOversizeEntries pins that more than SegmentMaxEntries entries
// is refused with ErrProtocol before any store write or replica modification.
// Mutant: > to >=.
func TestAppendRefusesOversizeEntries(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b, store := backendWithStore(t, driver, "origin-a")

	entries257 := make([]Entry, 257)
	for i := range entries257 {
		entries257[i] = upsertEntry("origin-a", "board", fmt.Sprintf(`["k%d"]`, i), `{"title":"x"}`)
	}

	_, err := b.Append(entries257)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("257 entries err = %v, want ErrProtocol", err)
	}
	if store.Puts != 0 {
		t.Errorf("store puts = %d, want 0", store.Puts)
	}
	assertEmptyReplica(t, driver)

	entries256 := entries257[:256]
	written, err := b.Append(entries256)
	if err != nil {
		t.Fatalf("256 entries append: %v", err)
	}
	if len(written) != 256 {
		t.Errorf("written = %d, want 256", len(written))
	}
}

// TestAppendRefusesWhenNoR2 pins that a backend opened without R2 refuses
// Append and Pull with ErrNoR2 without modifying the replica, pushing or
// pulling, while Head and Stats still answer.
func TestAppendRefusesWhenNoR2(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := NewTursoBackend(driver)
	if err := b.Open(Spec{
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://scratch.example",
		Token:   "the-token",
		R2:      nil,
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	driver.pushes = 0

	_, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)})
	if !errors.Is(err, ErrNoR2) || errors.Unwrap(err) != nil {
		t.Fatalf("append err = %v, want unwrapped ErrNoR2 sentinel", err)
	}
	if driver.pushes != 0 {
		t.Errorf("driver pushed %d times, want 0", driver.pushes)
	}
	assertEmptyReplica(t, driver)

	if _, err := b.Pull(nil); !errors.Is(err, ErrNoR2) || errors.Unwrap(err) != nil {
		t.Fatalf("pull err = %v, want unwrapped ErrNoR2 sentinel", err)
	}
	if driver.pulls != 0 {
		t.Errorf("driver pulled %d times, want 0: a pull with no bucket costs no transfer", driver.pulls)
	}
	if _, err := b.Head("origin-a", "", 0); err != nil {
		t.Errorf("Head failed: %v", err)
	}
	if _, err := b.Stats(); err != nil {
		t.Errorf("Stats failed: %v", err)
	}

	// Through the pipe
	driver2 := &localDriver{path: tempReplica(t)}
	b2 := NewTursoBackend(driver2)
	w := startWorker(t, b2)
	helloReq := Request{
		ID:      "1",
		Verb:    VerbHello,
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://remote.example",
		Token:   "the-token",
		R2:      nil,
	}
	if resp := w.ask(helloReq); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	exportReq := Request{
		ID:      "2",
		Verb:    VerbExport,
		Entries: []Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)},
	}
	resp := w.ask(exportReq)
	if resp.OK {
		t.Fatalf("export succeeded, want refusal: %+v", resp)
	}
	if !strings.Contains(resp.Error, ErrNoR2.Error()) {
		t.Errorf("export error = %q, want containing %q", resp.Error, ErrNoR2.Error())
	}
	pullResp := w.ask(Request{ID: "3", Verb: VerbPull})
	if pullResp.OK {
		t.Fatalf("pull succeeded, want refusal: %+v", pullResp)
	}
	if !strings.Contains(pullResp.Error, ErrNoR2.Error()) {
		t.Errorf("pull error = %q, want containing %q", pullResp.Error, ErrNoR2.Error())
	}
	if driver2.pulls != 0 {
		t.Errorf("driver pulled %d times over the pipe, want 0: a pull with no bucket costs no transfer", driver2.pulls)
	}
}

// TestAppendThroughPipeWithMirrorTables pins that when MirrorTables is configured,
// shared rows are inserted only for matching tables, with body NULL on deletes.
// Mutant: mirror every table.
func TestAppendThroughPipeWithMirrorTables(t *testing.T) {
	withStore(t)
	driver := &localDriver{path: tempReplica(t)}
	b := NewTursoBackend(driver)
	w := startWorker(t, b)

	helloReq := Request{
		ID:           "1",
		Verb:         VerbHello,
		Version:      ProtocolVersion,
		Origin:       "origin-a",
		URL:          "libsql://remote.example",
		Token:        "the-token",
		R2:           &R2Config{Bucket: "bucket"},
		MirrorTables: []string{"doc:config"},
	}
	if resp := w.ask(helloReq); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}

	exportReq := Request{
		ID:   "2",
		Verb: VerbExport,
		Entries: []Entry{
			{Origin: "origin-a", Tbl: "doc:config", PK: `["cfg1"]`, Op: opUpsert, SchemaVersion: 1, Body: json.RawMessage(`{"v":1}`)},
			{Origin: "origin-a", Tbl: "board", PK: `["b1"]`, Op: opUpsert, SchemaVersion: 1, Body: json.RawMessage(`{"title":"b"}`)},
			{Origin: "origin-a", Tbl: "doc:config", PK: `["cfg1"]`, Op: opDelete, SchemaVersion: 1},
		},
	}
	resp := w.ask(exportReq)
	if !resp.OK {
		t.Fatalf("export: %+v", resp)
	}
	if len(resp.Entries) != 3 || resp.Entries[0].Seq != 1 || resp.Entries[1].Seq != 2 || resp.Entries[2].Seq != 3 {
		t.Fatalf("resp entries = %+v, want seqs 1-3", resp.Entries)
	}

	db := openAt(t, driver.path)
	rows, err := db.Query("SELECT seq, batch, tbl, pk, op, schema_version, body, mac FROM shared ORDER BY seq")
	if err != nil {
		t.Fatalf("query shared: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type sharedRow struct {
		seq, batch, schemaVersion int
		tbl, pk, op               string
		body                      sql.NullString
		mac                       []byte
	}
	var shared []sharedRow
	for rows.Next() {
		var r sharedRow
		if err := rows.Scan(&r.seq, &r.batch, &r.tbl, &r.pk, &r.op, &r.schemaVersion, &r.body, &r.mac); err != nil {
			t.Fatalf("scan shared: %v", err)
		}
		shared = append(shared, r)
	}
	if len(shared) != 2 {
		t.Fatalf("shared rows = %d, want 2", len(shared))
	}
	if shared[0].seq != 1 || shared[0].tbl != "doc:config" || shared[0].op != opUpsert || !shared[0].body.Valid || shared[0].body.String != `{"v":1}` {
		t.Errorf("shared row 0 = %+v", shared[0])
	}
	if shared[1].seq != 3 || shared[1].tbl != "doc:config" || shared[1].op != opDelete || shared[1].body.Valid {
		t.Errorf("shared row 1 = %+v (body valid=%v)", shared[1], shared[1].body.Valid)
	}

	// With empty MirrorTables, shared stays empty.
	driverEmpty := &localDriver{path: tempReplica(t)}
	bEmpty := NewTursoBackend(driverEmpty)
	wEmpty := startWorker(t, bEmpty)
	helloReqEmpty := Request{
		ID:      "1",
		Verb:    VerbHello,
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://remote.example",
		Token:   "the-token",
		R2:      &R2Config{Bucket: "bucket"},
	}
	wEmpty.ask(helloReqEmpty)
	respEmpty := wEmpty.ask(exportReq)
	if !respEmpty.OK {
		t.Fatalf("export empty mirror: %+v", respEmpty)
	}
	var count int
	if err := openAt(t, driverEmpty.path).QueryRow("SELECT COUNT(*) FROM shared").Scan(&count); err != nil {
		t.Fatalf("count shared: %v", err)
	}
	if count != 0 {
		t.Errorf("shared count with empty MirrorTables = %d, want 0", count)
	}
}

// TestAppendSignedBoundaryThroughAdoption pins that an adopted orphan holding
// a signed entry records signed_from when the request boundary matches its first
// signed entry, and refuses with ErrBoundary leaving heads unmoved when it disagrees.
func TestAppendSignedBoundaryThroughAdoption(t *testing.T) {
	key := []byte("the passphrase this machine stretched")
	origin := "origin-a"
	s := signedEntry(`["s"]`)
	s.Seq = 1
	s.Batch = 1
	orphanData, _, err := EncodeSegment(origin, []Entry{s})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	t.Run("matching boundary records signed_from once", func(t *testing.T) {
		driver := &localDriver{path: tempReplica(t)}
		b, store := backendWithStore(t, driver, origin)
		if err := store.Put(context.Background(), blobstore.SegmentKey(origin, 1), bytes.NewReader(orphanData), int64(len(orphanData))); err != nil {
			t.Fatalf("seed orphan: %v", err)
		}

		boundary := boundaryAt(key, origin, 1)
		req := []Entry{upsertEntry(origin, "board", `["req"]`, `{"title":"r"}`)}
		if _, err := b.AppendSigned(req, boundary); err != nil {
			t.Fatalf("AppendSigned: %v", err)
		}

		var val string
		if err := openAt(t, driver.path).QueryRow("SELECT value FROM meta WHERE key = ?", signedFromPrefix+origin).Scan(&val); err != nil {
			t.Fatalf("read signed_from: %v", err)
		}
		if !strings.HasPrefix(val, "1 ") {
			t.Errorf("signed_from = %q, want starting with '1 '", val)
		}
	})

	t.Run("mismatched boundary refuses with ErrBoundary and heads unmoved", func(t *testing.T) {
		driver := &localDriver{path: tempReplica(t)}
		b, store := backendWithStore(t, driver, origin)
		if err := store.Put(context.Background(), blobstore.SegmentKey(origin, 1), bytes.NewReader(orphanData), int64(len(orphanData))); err != nil {
			t.Fatalf("seed orphan: %v", err)
		}

		boundary := boundaryAt(key, origin, 2)
		req := []Entry{upsertEntry(origin, "board", `["req"]`, `{"title":"r"}`)}
		_, err := b.AppendSigned(req, boundary)
		if !errors.Is(err, ErrBoundary) {
			t.Fatalf("err = %v, want ErrBoundary", err)
		}
		assertEmptyReplica(t, driver)
	})
}

// TestAppendStoreFailuresWriteNothing pins that a failed PutIfAbsent or Get on
// an orphan returns without modifying replica tables or pushing.
func TestAppendStoreFailuresWriteNothing(t *testing.T) {
	t.Run("PutIfAbsent error", func(t *testing.T) {
		driver := &localDriver{path: tempReplica(t)}
		baseStore := blobstore.NewMemBlobStore(nil)
		hooked := &hookedStore{
			BlobStore:       baseStore,
			failPutIfAbsent: errors.New("network timeout"),
		}
		restore := newBlobStore
		newBlobStore = func(R2Config) blobstore.BlobStore { return hooked }
		t.Cleanup(func() { newBlobStore = restore })

		b := NewTursoBackend(driver)
		_ = b.Open(Spec{
			Version: ProtocolVersion,
			Origin:  "origin-a",
			URL:     "libsql://scratch.example",
			Token:   "the-token",
			R2:      &R2Config{Bucket: "bucket"},
		})
		t.Cleanup(func() { _ = b.Close() })
		driver.pushes = 0

		_, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)})
		if err == nil || !strings.Contains(err.Error(), "network timeout") {
			t.Fatalf("err = %v, want network timeout", err)
		}
		if code := RefusalCodeOf(err); code != "" {
			t.Errorf("refusal code = %q, want %q", code, "")
		}
		assertEmptyReplica(t, driver)
		if driver.pushes != 0 {
			t.Errorf("pushes = %d, want 0", driver.pushes)
		}
	})

	t.Run("Get error on orphan", func(t *testing.T) {
		driver := &localDriver{path: tempReplica(t)}
		baseStore := blobstore.NewMemBlobStore(nil)
		key1 := blobstore.SegmentKey("origin-a", 1)
		_ = baseStore.Put(context.Background(), key1, bytes.NewReader([]byte("dummy")), 5)

		hooked := &hookedStore{
			BlobStore: baseStore,
			failGet:   io.ErrUnexpectedEOF,
		}
		restore := newBlobStore
		newBlobStore = func(R2Config) blobstore.BlobStore { return hooked }
		t.Cleanup(func() { newBlobStore = restore })

		b := NewTursoBackend(driver)
		_ = b.Open(Spec{
			Version: ProtocolVersion,
			Origin:  "origin-a",
			URL:     "libsql://scratch.example",
			Token:   "the-token",
			R2:      &R2Config{Bucket: "bucket"},
		})
		t.Cleanup(func() { _ = b.Close() })
		driver.pushes = 0

		_, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)})
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
		}
		assertEmptyReplica(t, driver)
		if driver.pushes != 0 {
			t.Errorf("pushes = %d, want 0", driver.pushes)
		}
	})
}

// TestAppendStatsTrackR2Traffic pins that Stats reports the uploaded segment
// size in R2Put, and the fetched orphan size in R2Get.
func TestAppendStatsTrackR2Traffic(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b, store := backendWithStore(t, driver, "origin-a")

	_, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b1"]`, `{"title":"one"}`)})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	st, err := b.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	key1 := blobstore.SegmentKey("origin-a", 1)
	var buf bytes.Buffer
	n1, _ := store.Get(context.Background(), key1, &buf)
	if st.R2Put != n1 {
		t.Errorf("stats R2Put = %d, want %d", st.R2Put, n1)
	}
	if st.R2Get != 0 {
		t.Errorf("stats R2Get = %d, want 0", st.R2Get)
	}

	// Seed an orphan at key 2
	orphanEntries := []Entry{
		{
			Origin: "origin-a", Seq: 2, Batch: 2, Tbl: "board", PK: `["b2"]`,
			Op: opUpsert, SchemaVersion: 1, Body: json.RawMessage(`{"title":"two"}`),
			At: time.Now().UTC(),
		},
	}
	orphanData, _, err := EncodeSegment("origin-a", orphanEntries)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	key2 := blobstore.SegmentKey("origin-a", 2)
	if err := store.Put(context.Background(), key2, bytes.NewReader(orphanData), int64(len(orphanData))); err != nil {
		t.Fatalf("put orphan: %v", err)
	}

	_, err = b.Append([]Entry{upsertEntry("origin-a", "board", `["b3"]`, `{"title":"three"}`)})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}

	st2, err := b.Stats()
	if err != nil {
		t.Fatalf("stats 2: %v", err)
	}
	key3 := blobstore.SegmentKey("origin-a", 3)
	var buf3 bytes.Buffer
	n3, _ := store.Get(context.Background(), key3, &buf3)
	if st2.R2Put != n1+n3 {
		t.Errorf("stats 2 R2Put = %d, want %d", st2.R2Put, n1+n3)
	}
	if st2.R2Get != int64(len(orphanData)) {
		t.Errorf("stats 2 R2Get = %d, want %d", st2.R2Get, len(orphanData))
	}
}

// TestAppendRefusesUnhashableBody pins that an entry whose JSON body cannot be
// hashed refuses the batch before any segment is put or replica row written.
func TestAppendRefusesUnhashableBody(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b, store := backendWithStore(t, driver, "origin-a")
	driver.pushes = 0

	e := Entry{
		Origin:        "origin-a",
		Tbl:           "board",
		PK:            `["b"]`,
		Op:            opUpsert,
		SchemaVersion: 1,
		Body:          json.RawMessage(`not valid json`),
	}
	_, err := b.Append([]Entry{e})
	if err == nil || !strings.Contains(err.Error(), "syncworker: hash board") {
		t.Fatalf("err = %v, want hash failure", err)
	}
	if store.Puts != 0 {
		t.Errorf("store puts = %d, want 0", store.Puts)
	}
	assertEmptyReplica(t, driver)
	if driver.pushes != 0 {
		t.Errorf("driver pushed %d times, want 0", driver.pushes)
	}
}

// TestAppendCasLostPullFailure pins that when the driver pull fails during a
// compare-and-set retry, the pull failure is returned.
func TestAppendCasLostPullFailure(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	baseStore := blobstore.NewMemBlobStore(nil)

	hooked := &hookedStore{
		BlobStore: baseStore,
		beforePutIfAbsentOnce: func(ctx context.Context, key string) {
			db := openAt(t, driver.path)
			_, _ = db.Exec("INSERT INTO heads (origin, seq, key, entries, bytes, at) VALUES ('origin-a', 99, 'k', 1, 10, '2026-11-15T00:00:00Z')")
			driver.pullErr = errors.New("network down")
		},
	}

	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return hooked }
	t.Cleanup(func() { newBlobStore = restore })

	b := NewTursoBackend(driver)
	if err := b.Open(Spec{
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://scratch.example",
		Token:   "the-token",
		R2:      &R2Config{Bucket: "bucket"},
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	_, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"b"}`)})
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("err = %v, want network down", err)
	}
}

// TestReadHeadReturnsErrorOnClosedDB pins that a database read failure is reported.
func TestReadHeadReturnsErrorOnClosedDB(t *testing.T) {
	db := openAt(t, tempReplica(t))
	_ = db.Close()
	_, _, err := readHead(context.Background(), db, "origin-a")
	if err == nil {
		t.Fatal("readHead succeeded on closed DB")
	}
}

func assertEmptyReplica(t *testing.T, driver *localDriver) {
	t.Helper()
	db := openAt(t, driver.path)
	var headsCount, logCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM heads").Scan(&headsCount); err != nil {
		t.Fatalf("read heads count: %v", err)
	}
	if headsCount != 0 {
		t.Errorf("heads count = %d, want 0", headsCount)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM log").Scan(&logCount); err != nil {
		t.Fatalf("read log count: %v", err)
	}
	if logCount != 0 {
		t.Errorf("log count = %d, want 0", logCount)
	}
}
