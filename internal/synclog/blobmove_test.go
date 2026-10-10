package synclog

// Which stored values leave the log, and the order the two halves of that
// happen in. Every case runs against a real file and the fake the rest of this
// slice uses, so the body a mover is handed is the body the file stored and the
// entry compared against is one the log really holds.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

// A body is in the store before the entry naming it is appended, and a failed
// upload appends nothing and keeps the outbox rows, exactly as a refused append
// does. The order is the whole rule: an entry pointing at an object that was
// never stored would latch the importer on a body a later attempt could have
// put there.
func TestExportUploadsBeforeAppend(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertRecord("01A", "m1"), insertSizedRoundFile("01A", "body", 50<<10))

	// The mover refuses, so nothing may be appended and the outbox entry has to
	// still be there for the next pass.
	refusing, refusedMover := NewBlobTransport("m1", t.TempDir())
	refusedMover.Err = errors.New("synclog: the bucket refused the put")
	if _, err := blobExporter(d, refusing).Export(); err == nil {
		t.Fatal("Export with a refusing mover returned no error")
	}
	if refusedMover.Puts != 0 {
		t.Fatalf("uploads = %d, want none when the mover refused", refusedMover.Puts)
	}
	if got, err := refusing.Stats(); err != nil || got.Entries != 0 {
		t.Fatalf("the log holds %d entries (err %v), want none", got.Entries, err)
	}
	if n := len(refusing.appended()); n != 0 {
		t.Fatalf("the transport was handed %d entries, want none", n)
	}
	want := `1 binding_record ["01A"] insert|2 round_file ["01A","body"] insert`
	if out := outboxRows(t, d); out != want {
		t.Fatalf("outbox after the failed upload = %q, want %q", out, want)
	}

	// The same file through a mover that works: the object is in the store, and
	// the entry that names it carries a ref rather than the value.
	log, mover := NewBlobTransport("m1", t.TempDir())
	if _, err := blobExporter(d, log).Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if mover.Puts != 1 {
		t.Fatalf("uploads = %d, want the one body the batch carried", mover.Puts)
	}
	if out := outboxRows(t, d); out != "" {
		t.Fatalf("outbox after the export = %q, want it empty", out)
	}
	entries := log.appended()
	if len(entries) != 2 {
		t.Fatalf("the batch carried %d entries, want the record and its round_file", len(entries))
	}
	got, err := DecodeBody(entries[1].Body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	ref, ok := got["body"].(RefValue)
	if !ok {
		t.Fatalf("body column = %#v (%T), want a RefValue", got["body"], got["body"])
	}
	// The codec travels with the ref, and it is read from the row rather than
	// assumed: this body was written plain, so the ref says plain, and an
	// importer that stored the frame under a zstd codec would write a row the
	// writer never had. TestRefColumnsOnlyMovesTheNamedColumns covers the
	// compressed half.
	if ref.Codec != 0 {
		t.Errorf("ref codec = %d, want the 0 the row's codec column carries", ref.Codec)
	}
	have, err := log.Has(t.Context(), blobstore.Key("m1", ref.SHA256))
	if err != nil || !have {
		t.Fatalf("the store holds the body the entry names = %v (err %v)", have, err)
	}
}

// A value under the threshold travels inline and a value over it travels as a
// ref, so the threshold is the only thing separating a small row from a large
// one and nothing else decides.
func TestExportKeepsSmallValuesInline(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertRecord("01A", "m1"),
		insertSizedRoundFile("01A", "small", BlobRefThreshold),
		insertSizedRoundFile("01A", "large", BlobRefThreshold+1),
	)

	log, mover := NewBlobTransport("m1", t.TempDir())
	if _, err := blobExporter(d, log).Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	entries := log.appended()
	if len(entries) != 3 {
		t.Fatalf("the batch carried %d entries, want the record and its two round_file rows", len(entries))
	}
	for _, e := range entries[1:] {
		got, err := DecodeBody(e.Body)
		if err != nil {
			t.Fatalf("DecodeBody %s: %v", e.PK, err)
		}
		switch e.PK {
		case `["01A","small"]`:
			if _, ok := got["body"].([]byte); !ok {
				t.Errorf("a value exactly at the threshold = %#v (%T), want the bytes inline",
					got["body"], got["body"])
			}
		case `["01A","large"]`:
			if _, ok := got["body"].(RefValue); !ok {
				t.Errorf("a value one byte over the threshold = %#v (%T), want a ref",
					got["body"], got["body"])
			}
		default:
			t.Errorf("unexpected entry %s", e.PK)
		}
	}
	if mover.Puts != 1 {
		t.Errorf("uploads = %d, want only the value over the threshold", mover.Puts)
	}
}

// Reconcile compares the ref form, so a row holding a large body is proposed
// once and never again. Hashing the local value instead would compare a digest
// of the bytes against a digest of a ref, and the row would be re-proposed on
// every run for the life of the file.
func TestReconcileStableWithRefs(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertRecord("01A", "m1"), insertSizedRoundFile("01A", "body", 50<<10))

	log, _ := NewBlobTransport("m1", t.TempDir())
	if _, err := blobReconciler(d, log).Reconcile(); err != nil {
		t.Fatalf("reconcile against an empty log: %v", err)
	}

	before, err := log.Stats()
	if err != nil {
		t.Fatalf("read the log's size: %v", err)
	}
	again, err := blobReconciler(d, log).Reconcile()
	if err != nil {
		t.Fatalf("reconcile the unchanged file: %v", err)
	}
	if again.Batches != 0 || again.Upserts != 0 || again.Deletes != 0 {
		t.Fatalf("reconcile = %+v, want nothing proposed for a row head already carries", again)
	}
	after, err := log.Stats()
	if err != nil {
		t.Fatalf("read the log's size: %v", err)
	}
	if after.Entries != before.Entries {
		t.Errorf("the log grew from %d to %d entries, want the second run to append nothing",
			before.Entries, after.Entries)
	}
}

// A ref is the one column value that is not the value itself: it encodes as the
// tagged object, decodes back to the same ref and never to bytes. An importer
// that read it as bytes would install an empty column.
func TestRefValueRoundTripsThroughCodec(t *testing.T) {
	t.Parallel()
	ref := RefFor(bytes.Repeat([]byte("frame "), 900), 1)
	row := db.ExchangeRow{
		Table: "round_file",
		PK:    `["01ABC","body"]`,
		Columns: []db.ExchangeColumn{
			{Name: "record_id", Value: "01ABC"},
			{Name: "name", Value: "body"},
			{Name: "body", Value: RefValue{BlobRef: ref}},
			{Name: "body_codec", Value: int64(1)},
		},
	}
	body, err := EncodeBody(row)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	if !bytes.Contains(body, []byte(`"$ref"`)) {
		t.Errorf("body = %s, want the value under the $ref tag", body)
	}
	got, err := DecodeBody(body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	value, ok := got["body"].(RefValue)
	if !ok {
		t.Fatalf("body column = %#v (%T), want a RefValue", got["body"], got["body"])
	}
	if value.BlobRef != ref {
		t.Errorf("decoded ref = %+v, want %+v", value.BlobRef, ref)
	}
	if _, isBytes := got["body"].([]byte); isBytes {
		t.Error("a ref came back as bytes, which would bind an empty column")
	}
}

// The digest is taken over the ref form and not over the entry's bytes, because
// the worker hashes the appended body: a head hash over one form and a local
// comparison over the other would never agree, and every large row would be
// re-proposed forever. This pins that the two forms really are distinguishable,
// so the stability above is not passing by accident.
func TestRefFormAndLocalValueHashDifferently(t *testing.T) {
	t.Parallel()
	stored := bytes.Repeat([]byte("body "), 2000)
	row := db.ExchangeRow{
		Table:   "round_file",
		PK:      `["01A","body"]`,
		Columns: []db.ExchangeColumn{{Name: "body", Value: stored}, {Name: "body_codec", Value: int64(1)}},
	}
	moved, _ := refColumns("m1", "round_file", row)
	refHash, err := BodyHash(EncodeBodyOrFail(t, moved))
	if err != nil {
		t.Fatalf("BodyHash of the ref form: %v", err)
	}
	localHash, err := BodyHash(EncodeBodyOrFail(t, row))
	if err != nil {
		t.Fatalf("BodyHash of the local value: %v", err)
	}
	if refHash == localHash {
		t.Fatal("the two forms hash the same, so this test cannot tell them apart")
	}
}

// Only the columns the table names move, and only the table they name. A blob
// in some other table stays inline: the list is the whole rule, and a column
// added to it by accident would put objects in the store that no importer is
// told to fetch.
func TestRefColumnsOnlyMovesTheNamedColumns(t *testing.T) {
	t.Parallel()
	big := bytes.Repeat([]byte("x"), BlobRefThreshold+1)
	row := db.ExchangeRow{
		Table: "transcript",
		PK:    `["01A"]`,
		Columns: []db.ExchangeColumn{
			{Name: "id", Value: "01A"},
			{Name: "record_json", Value: big},
			{Name: "rendered", Value: big},
			{Name: "record_json_codec", Value: int64(0)},
		},
	}
	moved, pending := refColumns("m1", "transcript", row)
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want record_json and rendered", len(pending))
	}
	for _, col := range moved.Columns {
		if col.Name == "id" || col.Name == "record_json_codec" {
			continue
		}
		if _, ok := col.Value.(RefValue); !ok {
			t.Errorf("column %s = %#v (%T), want a RefValue", col.Name, col.Value, col.Value)
		}
	}
	// The caller's row is untouched: it belongs to the transaction that read it.
	if _, ok := row.Columns[1].Value.(RefValue); ok {
		t.Error("refColumns edited the row it was given")
	}
	for _, b := range pending {
		if b.Key != blobstore.Key("m1", b.Ref.SHA256) {
			t.Errorf("pending key = %q, want the origin and digest", b.Key)
		}
		if !bytes.Equal(b.Bytes, big) {
			t.Error("pending bytes are not the value the row held")
		}
		if b.Ref.Codec != 0 {
			t.Errorf("ref codec = %d, want the 0 the row's codec column carries", b.Ref.Codec)
		}
	}

	other := db.ExchangeRow{
		Table:   "binding",
		PK:      `["01A"]`,
		Columns: []db.ExchangeColumn{{Name: "note", Value: big}},
	}
	if _, pending := refColumns("m1", "binding", other); pending != nil {
		t.Errorf("pending = %d for a table with no moving column, want none", len(pending))
	}
}

// The ref's codec is the row's own, so the same rule covers both storages: a
// body the file holds as a zstd frame is stored as that frame, and the importer
// needs the codec to put the plaintext back where the column expects it. A ref
// that always said plain would install a frame as text.
func TestRefCarriesTheRowsOwnCodec(t *testing.T) {
	t.Parallel()
	// A frame, and one over the threshold: repetitive text compresses well
	// below it, so a value that repeated would test the threshold rather than
	// the codec.
	frame := zstdBytes(t, incompressible(BlobRefThreshold+1))
	row := db.ExchangeRow{
		Table:   "transcript",
		PK:      `["01A"]`,
		Columns: []db.ExchangeColumn{{Name: "record_json", Value: frame}, {Name: "record_json_codec", Value: int64(1)}},
	}
	_, pending := refColumns("m1", "transcript", row)
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want the one record_json", len(pending))
	}
	if pending[0].Ref.Codec != 1 {
		t.Errorf("ref codec = %d, want the 1 the row carries", pending[0].Ref.Codec)
	}
	if !bytes.Equal(pending[0].Bytes, frame) {
		t.Error("the value staged is not the frame the row holds")
	}
	// The digest is over the stored frame and not the plaintext: it is what the
	// importer checks the fetched object against, and the plaintext never
	// crosses between the two machines.
	if pending[0].Ref.SHA256 != digestOf(frame) {
		t.Error("the ref digest is not the digest of the stored value")
	}
	if pending[0].Ref.Bytes != int64(len(frame)) {
		t.Errorf("ref length = %d, want %d", pending[0].Ref.Bytes, len(frame))
	}
}

// The staging file is scratch: it is removed whether the put came back or not,
// because a file left behind after a failed put holds a body the store never
// took and that nothing will fetch. It is also the only copy of those bytes
// while it is there, so it is written owner-only.
func TestStagingFileRemovedAfterUpload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stored := bytes.Repeat([]byte("y"), BlobRefThreshold+1)
	blob := pendingBlob{
		Key:   blobstore.Key("m1", RefFor(stored, 1).SHA256),
		Bytes: stored,
		Ref:   RefFor(stored, 1),
	}

	mover := NewMemBlobMover()
	if err := uploadPending(mover, dir, []pendingBlob{blob}); err != nil {
		t.Fatalf("uploadPending: %v", err)
	}
	assertEmptyStaging(t, dir, "after a successful put")

	// The same holds when the put failed: the entry naming that file was never
	// appended, so nothing would fetch it.
	mover.Err = errors.New("synclog: the bucket refused the put")
	if err := uploadPending(mover, dir, []pendingBlob{blob}); err == nil {
		t.Fatal("uploadPending with a refusing mover returned no error")
	}
	assertEmptyStaging(t, dir, "after a refused put")
}

// The mover reads the staged file rather than the caller's bytes, so a staging
// write that silently produced the wrong thing would show up as an object the
// store does not hold. This is what pins that the file is what is uploaded.
func TestUploadStoresTheStagedBytes(t *testing.T) {
	t.Parallel()
	stored := bytes.Repeat([]byte("z"), BlobRefThreshold+1)
	ref := RefFor(stored, 0)

	mover := NewMemBlobMover()
	if err := uploadPending(mover, t.TempDir(), []pendingBlob{{
		Key:   blobstore.Key("m1", ref.SHA256),
		Bytes: stored,
		Ref:   ref,
	}}); err != nil {
		t.Fatalf("uploadPending: %v", err)
	}
	var buf bytes.Buffer
	if _, err := mover.Store().Get(t.Context(), blobstore.Key("m1", ref.SHA256), &buf); err != nil {
		t.Fatalf("read the stored body: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), stored) {
		t.Errorf("the store holds %d bytes, want the %d the row held", buf.Len(), len(stored))
	}
}

// A transport that moves no bodies must not rewrite a row: a ref would name an
// object nobody put, and the importer would latch on it. Every value then
// travels inline, which is what a log with no bucket behind it gets.
func TestNoMoverLeavesEveryValueInline(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertRecord("01A", "m1"), insertSizedRoundFile("01A", "body", 50<<10))

	plain := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(d, plain).Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	entries := plain.appended()
	if len(entries) != 2 {
		t.Fatalf("appended %d entries, want the record and its round_file", len(entries))
	}
	got, err := DecodeBody(entries[1].Body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	body, ok := got["body"].([]byte)
	if !ok {
		t.Fatalf("body column = %#v (%T), want the bytes inline", got["body"], got["body"])
	}
	if len(body) != 50<<10 {
		t.Errorf("inline body is %d bytes, want the whole 50 KiB", len(body))
	}
}

// A delete carries no body, so it can never move a value. This pins that the
// large row that is being removed leaves nothing behind for the importer to
// fetch and nothing in the store for the cleanup to find unreferenced.
func TestADeleteNeverMovesAValue(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertRecord("01A", "m1"), insertSizedRoundFile("01A", "body", 50<<10))
	if _, err := exporter(d, &recording{MemTransport: NewMemTransport("m1")}).Export(); err != nil {
		t.Fatalf("export the file first: %v", err)
	}
	seed(t, path, `DELETE FROM round_file WHERE name = 'body'`)

	log, mover := NewBlobTransport("m1", t.TempDir())
	if _, err := blobExporter(d, log).Export(); err != nil {
		t.Fatalf("export the delete: %v", err)
	}
	if mover.Puts != 0 {
		t.Errorf("uploads = %d on a delete, want none", mover.Puts)
	}
	entries := log.appended()
	if len(entries) != 1 || entries[0].Op != OpDelete || entries[0].Table != "round_file" {
		t.Fatalf("appended %+v, want the one round_file delete", entries)
	}
}

// blobMoverTransport is a log that also moves bodies, which is the one shape the
// daemon's wiring produces and so the one shape a case here runs against: a
// transport the exporter and reconcile take a mover from, rather than a mover
// handed to them beside a separate log.
type blobMoverTransport struct {
	*recording
	*MemBlobMover
	staging string
}

// NewBlobTransport returns a log and the mover that feeds it, over one staging
// folder. A case drives both through the pair.
func NewBlobTransport(origin, staging string) (*blobMoverTransport, *MemBlobMover) {
	mover := NewMemBlobMover()
	return &blobMoverTransport{
		recording:    &recording{MemTransport: NewMemTransport(origin)},
		MemBlobMover: mover,
		staging:      staging,
	}, mover
}

// BlobStagingDir is the folder the mover's staging files go in.
func (b *blobMoverTransport) BlobStagingDir() string { return b.staging }

// Has is one object's presence in the store, for a case asserting that a body
// reached it rather than only that the call was made.
func (b *blobMoverTransport) Has(ctx context.Context, key string) (bool, error) {
	return b.Store().Has(ctx, key)
}

// The shape the daemon builds: one object that appends the entries and stores
// the bodies they point at.
var _ BlobTransport = (*blobMoverTransport)(nil)

// blobExporter is the exporter the blob cases drive: over a transport that both
// appends and moves bodies, with the clock fixed like every other case here.
func blobExporter(d *db.DB, log LogTransport) *Exporter {
	e := NewExporter(d, log)
	e.now = syncClock
	return e
}

// blobReconciler is reconcile over a transport that moves bodies.
func blobReconciler(d *db.DB, log LogTransport) *Reconciler {
	r := NewReconciler(d, log)
	r.now = syncClock
	return r
}

// digestOf is the hex sha256 of a value, which is the name its object is stored
// under.
func digestOf(stored []byte) string { return RefFor(stored, 0).SHA256 }

// incompressible is n bytes a compressor cannot shrink, so a case that needs a
// stored value over the threshold gets one whatever it then does to it. A
// linear congruential sequence is used rather than a fixed literal because a
// literal would have to be written out.
func incompressible(n int) []byte {
	out := make([]byte, n)
	state := uint32(1)
	for i := range out {
		state = state*1664525 + 1013904223
		out[i] = byte(state >> 24)
	}
	return out
}

// EncodeBodyOrFail encodes a row for a test that is about the body it holds.
func EncodeBodyOrFail(t *testing.T, row db.ExchangeRow) []byte {
	t.Helper()
	body, err := EncodeBody(row)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	return body
}

// insertSizedRoundFile writes a round_file whose body is size bytes long. The
// bytes are SQLite's zeroblob rather than a hex literal: a 50 KiB literal in a
// test is unreadable, and what these cases pin is the value's length, not what
// is in it.
func insertSizedRoundFile(recordID, name string, size int) string {
	n := strconv.Itoa(size)
	return `INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		VALUES ('` + recordID + `', '` + name + `', 1, zeroblob(` + n + `), ` + n + `, 's', 't', 't')`
}

// assertEmptyStaging fails when the staging folder still holds a file, naming
// the moment so a failure says which of the two checks it was.
func assertEmptyStaging(t *testing.T, dir, when string) {
	t.Helper()
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the staging folder %s: %v", when, err)
	}
	if len(names) != 0 {
		var left []string
		for _, n := range names {
			left = append(left, n.Name())
		}
		t.Fatalf("the staging folder holds %v %s, want it empty", left, when)
	}
}
