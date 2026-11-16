package syncworker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// The migrate verb's tests: one Migrate call end to end, the crashes a re-run
// recovers from, the late rows the sweep catches, and the refusals. Every test
// opens through a migrate hello with the two doc tables mirrored, and a reader
// is a second backend on a VACUUM INTO copy sharing the store.

// migrateMirror is the mirror list every Migrate test opens with: the two doc
// tables, so shared holds only those rows.
var migrateMirror = []string{"doc:config", "doc:secrets"}

// openMigrateBackend opens a backend for the migrate verb on the seeded
// format-5 replica at path, sharing store, as origin. The driver is returned
// so the test reads its pushes and pulls.
func openMigrateBackend(t *testing.T, path string, store blobstore.BlobStore, origin string) (*TursoBackend, *localDriver) {
	t.Helper()
	driver := &localDriver{path: path}
	b := NewTursoBackend(driver)
	b.openStore = func(R2Config) blobstore.BlobStore { return store }
	if err := b.Open(Spec{
		Version: ProtocolVersion, Origin: origin, URL: "libsql://scratch.example",
		Token:        "the-token",
		R2:           &R2Config{Bucket: "bucket"},
		MirrorTables: migrateMirror,
		Migrate:      true,
	}); err != nil {
		t.Fatalf("open the format-5 replica for migration: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, driver
}

// installEntry is one installation upsert for origin carrying label: the pk is
// the json array of the origin, so the migration reads it as that origin's
// label.
func installEntry(origin, label string, seq, batch int) Entry {
	e := upsertEntry(origin, "installation", fmt.Sprintf(`["%s"]`, origin), fmt.Sprintf(`{"label":%q}`, label))
	e.Seq, e.Batch = seq, batch
	return e
}

// docEntry is one doc row in table for origin.
func docEntry(origin, table, pk, body string, seq, batch int) Entry {
	e := upsertEntry(origin, table, pk, body)
	e.Seq, e.Batch = seq, batch
	return e
}

// seedMigrateRemote seeds a three-origin format-5 remote: origin-a is the
// migrating machine's own. Origin-b is compacted with floor 5000 and sparse
// seqs below it. Installation rows label two origins, with the newest of two
// upserts winning for origin-a. Doc rows cover both doc tables. MACs,
// dependencies and deletes ride along so the writer is pinned carrying them.
func seedMigrateRemote(t *testing.T, path string) []Entry {
	t.Helper()
	seedFormatFiveRemote(t, path)
	var seeded []Entry
	// Origin-a: dense rows with the label upserts newest last.
	seeded = append(seeded,
		legacyLogEntry("origin-a", 1, 1),
		legacyLogEntry("origin-a", 2, 1),
		docEntry("origin-a", "doc:config", `["c1"]`, `{"v":"one"}`, 3, 1),
		installEntry("origin-a", "old laptop", 4, 4),
		docEntry("origin-a", "doc:secrets", `["s1"]`, `{"v":"two"}`, 5, 5),
		installEntry("origin-a", "laptop", 6, 6),
		legacyLogEntry("origin-a", 7, 7),
	)
	// Origin-b: sparse seqs below the floor, then dense above it.
	for _, seq := range []int{3000, 4000, 5000, 5001, 5002} {
		e := legacyLogEntry("origin-b", seq, seq)
		seeded = append(seeded, e)
	}
	seeded = append(seeded,
		docEntry("origin-b", "doc:config", `["c2"]`, `{"v":"three"}`, 5003, 5003),
		installEntry("origin-b", "desk", 5004, 5004),
	)
	// Origin-c: dense rows with no installation row, so its label stays empty.
	seeded = append(seeded, legacyLogEntries("origin-c", 1, 5)...)
	seeded = append(seeded, docEntry("origin-c", "doc:secrets", `["s3"]`, `{"v":"four"}`, 6, 6))
	seedFormatFiveLog(t, path, seeded)
	execOn(t, path,
		"INSERT INTO meta (key, value) VALUES ('floor.origin-b', '5000')",
		"INSERT INTO meta (key, value) VALUES ('signed_from.origin-a', '1 abc')",
		"INSERT INTO meta (key, value) VALUES ('epoch_cut.origin-a', '2 7 abc')",
	)
	return seeded
}

// headsRow is one heads row as the migration test reads it back.
type headsRow struct {
	seq, entries, floor, legacyUpto int
	key, label, at                  string
	size                            int64
}

// readHeadsRow reads one origin's heads row.
func readHeadsRow(t *testing.T, path, origin string) headsRow {
	t.Helper()
	var h headsRow
	if err := openAt(t, path).QueryRow(
		"SELECT seq, key, entries, bytes, floor, legacy_upto, label, at FROM heads WHERE origin = ?",
		origin).Scan(&h.seq, &h.key, &h.entries, &h.size, &h.floor, &h.legacyUpto, &h.label, &h.at); err != nil {
		t.Fatalf("read heads %s: %v", origin, err)
	}
	return h
}

// readSharedRows reads every shared row in origin then sequence order.
func readSharedRows(t *testing.T, path string) []Entry {
	t.Helper()
	rows, err := openAt(t, path).Query(
		"SELECT origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq FROM shared ORDER BY origin, seq")
	if err != nil {
		t.Fatalf("read shared: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var e Entry
		var body, afterOrigin sql.NullString
		var at string
		var afterSeq sql.NullInt64
		if err := rows.Scan(&e.Origin, &e.Seq, &e.Batch, &e.Tbl, &e.PK, &e.Op,
			&e.SchemaVersion, &body, &at, &e.MAC, &afterOrigin, &afterSeq); err != nil {
			t.Fatalf("scan a shared row: %v", err)
		}
		if body.Valid {
			e.Body = json.RawMessage(body.String)
		}
		parsed, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			t.Fatalf("parse a shared time: %v", err)
		}
		e.At = parsed.UTC()
		if afterOrigin.Valid {
			e.AfterOrigin = afterOrigin.String
		}
		if afterSeq.Valid {
			e.AfterSeq = int(afterSeq.Int64)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read shared: %v", err)
	}
	return out
}

// storeBytes maps every object key to its exact bytes, so a test states that a
// crashed run's retry produced exactly the clean run's objects.
func storeBytes(t *testing.T, store *blobstore.MemBlobStore) map[string][]byte {
	t.Helper()
	objs, err := store.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list the store: %v", err)
	}
	out := make(map[string][]byte, len(objs))
	for _, o := range objs {
		var buf bytes.Buffer
		if _, err := store.Get(context.Background(), o.Key, &buf); err != nil {
			t.Fatalf("read object %s: %v", o.Key, err)
		}
		out[o.Key] = buf.Bytes()
	}
	return out
}

// countStore wraps a store and counts every call, so a test states that a
// re-run touched nothing.
type countStore struct {
	blobstore.BlobStore
	calls int64
}

func (s *countStore) PutIfAbsent(ctx context.Context, key string, body io.Reader, size int64) (bool, error) {
	s.calls++
	return s.BlobStore.PutIfAbsent(ctx, key, body, size)
}

func (s *countStore) Get(ctx context.Context, key string, w io.Writer) (int64, error) {
	s.calls++
	return s.BlobStore.Get(ctx, key, w)
}

func (s *countStore) List(ctx context.Context, prefix string) ([]blobstore.Object, error) {
	s.calls++
	return s.BlobStore.List(ctx, prefix)
}

func (s *countStore) ListAfter(ctx context.Context, prefix, startAfter string) ([]blobstore.Object, error) {
	s.calls++
	return s.BlobStore.ListAfter(ctx, prefix, startAfter)
}

func (s *countStore) Has(ctx context.Context, key string) (bool, error) {
	s.calls++
	return s.BlobStore.Has(ctx, key)
}

// totalObjects counts every object in the store.
func totalObjects(t *testing.T, store *blobstore.MemBlobStore) int {
	t.Helper()
	objs, err := store.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list the store: %v", err)
	}
	return len(objs)
}

func marksCopy(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func equalMarks(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
