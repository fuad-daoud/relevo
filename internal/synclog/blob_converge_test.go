package synclog

// Bodies across two machines and one store: what B holds after A's large values
// travelled as refs, and what happens when the object a ref names is gone or is
// not the one that was put.

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

// link is one machine's view of the shared log and the shared store: the shape
// the daemon builds, an object that appends entries and moves the bodies they
// name.
type link struct {
	*MemTransport
	*MemBlobMover
	staging string
}

func (l link) BlobStagingDir() string { return l.staging }

var _ BlobTransport = link{}

// blobPair is the log and store two machines share.
type blobPair struct {
	log   *MemTransport
	mover *MemBlobMover
}

func newBlobPair() blobPair { return blobPair{log: NewMemTransport("A"), mover: NewMemBlobMover()} }

func (p blobPair) link(t *testing.T, origin string) link {
	t.Helper()
	return link{MemTransport: p.log.OnLog(origin), MemBlobMover: p.mover, staging: t.TempDir()}
}

// noise is n bytes that do not compress, so a size is a size on every path.
func noise(seed int64, n int) []byte {
	out := make([]byte, n)
	_, _ = rand.New(rand.NewSource(seed)).Read(out)
	return out
}

// seedBodies gives machine A a record, two round files (one over the threshold,
// one under it) and a transcript with a large rendered column.
func seedBodies(t *testing.T, path string) {
	t.Helper()
	seed(t, path, insertRecord("01A", "A"),
		`INSERT INTO mastermind (id, harness_kind, session_id, first_seen, last_seen, origin) VALUES ('m1', 'agy', 's', 't', 't', 'A')`)
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open for writing: %v", err)
	}
	defer func() { _ = raw.Close() }()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at, body_codec)
			VALUES ('01A', 'big.md', 1, ?, 1, 's', 't', 't', 1)`, []any{noise(1, 200<<10)}},
		{`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at, body_codec)
			VALUES ('01A', 'small.md', 1, ?, 1, 's', 't', 't', 0)`, []any{noise(2, 3<<10)}},
		{`INSERT INTO transcript (id, owner_kind, owner_id, seq, ts, record_json, rendered, rendered_codec)
			VALUES ('tr1', 'mastermind', 'm1', 7, 't', '{}', ?, 1)`, []any{noise(3, 64<<10)}},
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s.sql, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.sql, err)
		}
	}
}

// storedColumns is the bytes and codec columns of the three seeded values, as a
// file holds them.
func storedColumns(t *testing.T, d *db.DB) map[string]string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()
	out := map[string]string{}
	rows, err := raw.Query(`SELECT 'rf:' || name, body, body_codec FROM round_file
		UNION ALL SELECT 'tr:' || id, rendered, rendered_codec FROM transcript`)
	if err != nil {
		t.Fatalf("read the columns: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		var body []byte
		var codec int
		if err := rows.Scan(&key, &body, &codec); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[key] = string(body) + "|" + string(rune('0'+codec))
	}
	return out
}

func exportFrom(t *testing.T, d *db.DB, l link) {
	t.Helper()
	if _, err := NewExporter(d, l).Export(); err != nil {
		t.Fatalf("export from %s: %v", d.Origin(), err)
	}
}

// Large values travel as refs and arrive byte for byte with their codecs; a
// second round moves nothing; and once A's objects are gone a fresh machine
// latches with the file named.
func TestBlobConvergeTwoMachines(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	b, _ := exporterFile(t, "B")
	seedBodies(t, aPath)

	exportFrom(t, a, p.link(t, "A"))
	if p.mover.Puts != 2 {
		t.Fatalf("objects stored = %d, want the 200 KiB body and the large rendered", p.mover.Puts)
	}
	if _, err := NewImporter(b, p.link(t, "B")).Import(); err != nil {
		t.Fatalf("B import: %v", err)
	}
	want, got := storedColumns(t, a), storedColumns(t, b)
	if len(want) != 3 {
		t.Fatalf("A holds %d values, want 3", len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s differs on B (%d bytes vs %d)", k, len(got[k]), len(v))
		}
	}

	// A second round has nothing to move.
	gets := p.mover.Gets
	exportFrom(t, a, p.link(t, "A"))
	res, err := NewImporter(b, p.link(t, "B")).Import()
	if err != nil || res.Applied != 0 || p.mover.Puts != 2 || p.mover.Gets != gets {
		t.Fatalf("second round: applied %d, puts %d, gets %d (was %d), err %v; want nothing moved",
			res.Applied, p.mover.Puts, p.mover.Gets, gets, err)
	}

	// With A's objects gone, a machine that never imported latches.
	ctx := context.Background()
	objects, err := p.mover.Store().List(ctx, "")
	if err != nil || len(objects) != 2 {
		t.Fatalf("store holds %d objects (err %v), want 2", len(objects), err)
	}
	for _, o := range objects {
		if err := p.mover.Store().Delete(ctx, o.Key); err != nil {
			t.Fatalf("delete %s: %v", o.Key, err)
		}
	}
	c, _ := exporterFile(t, "C")
	_, err = NewImporter(c, p.link(t, "C")).Import()
	if !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("C import = %v, want ErrBlobMissing", err)
	}
	if !strings.Contains(err.Error(), "big.md") {
		t.Errorf("the error %q does not name the file", err)
	}
}

// A missing object ends the import with a message that names the table, the
// binding, the round, the file and the origin's label.
func TestImportMissingObjectLatchesWithFile(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	seedBodies(t, aPath)
	exportFrom(t, a, p.link(t, "A"))
	objects, _ := p.mover.Store().List(context.Background(), "")
	for _, o := range objects {
		_ = p.mover.Store().Delete(context.Background(), o.Key)
	}

	c, cPath := exporterFile(t, "C")
	seed(t, cPath, `INSERT INTO installation (id, label, first_seen, last_seen) VALUES ('A', 'laptop', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	_, err := NewImporter(c, p.link(t, "C")).Import()
	if !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("import = %v, want ErrBlobMissing", err)
	}
	for _, part := range []string{"round_file", "big.md", "round 1", "binding", "laptop"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q lacks %q", err, part)
		}
	}
}

// tamper replaces every object with the same number of different bytes.
func tamper(t *testing.T, s *blobstore.MemBlobStore) {
	t.Helper()
	ctx := context.Background()
	objects, _ := s.List(ctx, "")
	for _, o := range objects {
		var buf bytes.Buffer
		if _, err := s.Get(ctx, o.Key, &buf); err != nil {
			t.Fatalf("get %s: %v", o.Key, err)
		}
		bad := bytes.Repeat([]byte{0xAB}, buf.Len())
		if err := s.Put(ctx, o.Key, bytes.NewReader(bad), int64(len(bad))); err != nil {
			t.Fatalf("overwrite %s: %v", o.Key, err)
		}
	}
}

// An object whose bytes are not the ref's is never applied, the mark stays put
// and the origin is held for a retry rather than latched.
func TestImportChecksSha256OnFetch(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	b, _ := exporterFile(t, "B")
	seedBodies(t, aPath)
	exportFrom(t, a, p.link(t, "A"))
	tamper(t, p.mover.Store())

	res, err := NewImporter(b, p.link(t, "B")).Import()
	if err != nil {
		t.Fatalf("import = %v, want the origin held and no error", err)
	}
	if len(res.Stalled) != 1 || !strings.Contains(res.Stalled[0].Reason, ErrBlobMismatch.Error()) {
		t.Fatalf("stalled = %+v, want one stall for the digest mismatch", res.Stalled)
	}
	if res.Applied != 0 || len(storedColumns(t, b)) != 0 {
		t.Fatalf("applied %d, rows %v; want nothing applied", res.Applied, storedColumns(t, b))
	}
	marks, _ := b.ImportMarks()
	if marks["A"] != 0 {
		t.Fatalf("mark = %d, want it unmoved", marks["A"])
	}
}

// A fetch that fails for a reason other than a missing object holds the origin
// with nothing applied, and the next attempt applies the batch.
func TestImportHoldsOriginOnFetchError(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	b, _ := exporterFile(t, "B")
	seedBodies(t, aPath)
	exportFrom(t, a, p.link(t, "A"))

	failing := p.link(t, "B")
	failing.MemBlobMover = &MemBlobMover{store: p.mover.Store(), GetErr: errors.New("synclog: the bucket answered 503")}
	res, err := NewImporter(b, failing).Import()
	if err != nil || len(res.Stalled) != 1 || res.Applied != 0 {
		t.Fatalf("import = (%+v, %v), want one stall and nothing applied", res, err)
	}
	if rows := storedColumns(t, b); len(rows) != 0 {
		t.Fatalf("B holds %v after a failed fetch, want no body applied", rows)
	}
	if _, err := NewImporter(b, p.link(t, "B")).Import(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := storedColumns(t, b); len(got) != 3 {
		t.Fatalf("B holds %d values after the retry, want 3", len(got))
	}
}
