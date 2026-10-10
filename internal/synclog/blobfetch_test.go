package synclog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// A lost transcript body names its owner and seq, and an origin with no
// installation row is named by its id, since there is no label to use.
func TestImportMissingTranscriptNamesOwnerAndSeq(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	seedBodies(t, aPath)
	exportFrom(t, a, p.link(t, "A"))
	sum := sha256.Sum256(noise(3, 64<<10))
	if err := p.mover.Store().Delete(context.Background(), blobstore.Key("A", hex.EncodeToString(sum[:]))); err != nil {
		t.Fatalf("delete the transcript's object: %v", err)
	}

	c, _ := exporterFile(t, "C")
	_, err := NewImporter(c, p.link(t, "C")).Import()
	if !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("import = %v, want ErrBlobMissing", err)
	}
	for _, part := range []string{"import from A", "transcript of owner m1, seq 7", "rendered"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q lacks %q", err, part)
		}
	}
}

func refEntry(t *testing.T, codec json.RawMessage) Entry {
	t.Helper()
	body := map[string]json.RawMessage{"body": EncodeRef(BlobRef{SHA256: strings.Repeat("ab", 32), Bytes: 5000, Codec: 0})}
	if codec != nil {
		body["body_codec"] = codec
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return Entry{Origin: "A", Table: "round_file", PK: `["01A","big.md",1]`, Op: OpUpsert, Body: raw}
}

// A ref whose codec disagrees with the row's codec column is refused before any
// fetch, since no fetch can repair an entry that contradicts itself. A body
// without the column reads as plain, so it agrees with a plain ref.
func TestResolveRefsChecksTheRowsCodec(t *testing.T) {
	t.Parallel()
	for _, codec := range []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"zstd"`)} {
		err := resolveRefs(NewMemBlobMover(), t.TempDir(), []Entry{refEntry(t, codec)})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("codec %s: resolve = %v, want ErrInvalid", codec, err)
		}
	}
	if err := resolveRefs(nil, "", []Entry{refEntry(t, nil)}); !errors.Is(err, errNoMover) {
		t.Fatalf("absent codec column: resolve = %v, want it past the codec check to the missing mover", err)
	}
}

func TestMissingBlobNamesTheColumnAndIsErrBlobMissing(t *testing.T) {
	t.Parallel()
	err := error(&missingBlob{entry: Entry{Table: "round_file", PK: `["01A","big.md",1]`}, column: "body"})
	if !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("%v is not ErrBlobMissing", err)
	}
	for _, part := range []string{"round_file", "big.md", "column body"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q lacks %q", err, part)
		}
	}
}

func TestStallNamesTheMachineAndSaysItIsRetried(t *testing.T) {
	t.Parallel()
	got := Stall{Origin: "A", Label: "laptop", Reason: "the bucket answered 503"}.String()
	for _, part := range []string{"laptop", "the bucket answered 503", "retried"} {
		if !strings.Contains(got, part) {
			t.Errorf("stall %q lacks %q", got, part)
		}
	}
}

// A row the importer has no reader's name for is named by its table and key,
// and so is a body that does not decode.
func TestDescribeRowFallsBackToTableAndKey(t *testing.T) {
	t.Parallel()
	var i Importer
	for _, e := range []Entry{
		{Table: "artifact", PK: `["x"]`, Body: json.RawMessage(`{"id":"x"}`)},
		{Table: "round_file", PK: `["x"]`, Body: json.RawMessage(`not json`)},
	} {
		if got, want := i.describeRow(e), e.Table+" "+e.PK; got != want {
			t.Errorf("describe %s = %q, want %q", e.Table, got, want)
		}
	}
}

// An origin is the first segment of an object key, and a remote entry may carry
// any string there: a key-shaping one is refused as invalid rather than reaching
// the key builder, which stops on it.
func TestResolveRefsRefusesAForgedOrigin(t *testing.T) {
	t.Parallel()
	e := refEntry(t, json.RawMessage(`0`))
	e.Origin = "evil/x"
	if err := resolveRefs(NewMemBlobMover(), t.TempDir(), []Entry{e}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resolve = %v, want ErrInvalid", err)
	}
}

// A forged-origin batch is dropped and its mark moves, so it cannot hold the
// import on every later attempt.
func TestImportDropsAForgedOriginRefAndMovesOn(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	forged := refEntry(t, json.RawMessage(`0`))
	forged.Origin = "evil/x"
	if _, err := p.log.OnLog("evil/x").Append([]Entry{forged}); err != nil {
		t.Fatalf("append the forged entry: %v", err)
	}
	b, _ := exporterFile(t, "B")
	res, err := NewImporter(b, p.link(t, "B")).Import()
	if err != nil || len(res.Dropped) != 1 {
		t.Fatalf("import = (%+v, %v), want the forged batch dropped", res, err)
	}
	if marks, _ := b.ImportMarks(); marks["evil/x"] != 1 {
		t.Errorf("mark = %d, want it past the dropped entry", marks["evil/x"])
	}
}

// The batch cap is checked from the lengths the refs declare, so a forged batch
// naming more than one batch may hold is refused before any download.
func TestResolveRefsRefusesABatchPastTheCapBeforeFetching(t *testing.T) {
	t.Parallel()
	var batch []Entry
	for n := 0; int64(n)*MaxBlobBytes <= MaxBatchBlobBytes; n++ {
		sum := sha256.Sum256([]byte{byte(n)})
		ref := EncodeRef(BlobRef{SHA256: hex.EncodeToString(sum[:]), Bytes: MaxBlobBytes, Codec: 0})
		body, _ := json.Marshal(map[string]json.RawMessage{"body": ref})
		batch = append(batch, Entry{Origin: "A", Table: "round_file", PK: `["x"]`, Op: OpUpsert, Body: body})
	}
	m := NewMemBlobMover()
	if err := resolveRefs(m, t.TempDir(), batch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resolve = %v, want ErrInvalid", err)
	}
	if m.Gets != 0 {
		t.Errorf("fetched %d objects for a batch refused by its declared size", m.Gets)
	}
}

func TestDecodeRefRefusesALengthPastMaxBlobBytes(t *testing.T) {
	t.Parallel()
	raw := EncodeRef(BlobRef{SHA256: strings.Repeat("ab", 32), Bytes: MaxBlobBytes + 1})
	if _, _, err := DecodeRef(raw); !errors.Is(err, ErrInvalid) {
		t.Fatalf("decode = %v, want ErrInvalid", err)
	}
}

// batchesFrom counts the batches origin A appended, as another machine pulls them.
func batchesFrom(t *testing.T, p blobPair) int {
	t.Helper()
	entries, err := p.log.OnLog("B").Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	seen := map[int]bool{}
	for _, e := range entries {
		seen[e.Batch] = true
	}
	return len(seen)
}

// A drain whose bodies pass the cap is appended as smaller batches, each within
// it, and nothing is lost: another machine still converges.
func TestExportSplitsABatchPastTheBlobCap(t *testing.T) {
	t.Parallel()
	whole, split := newBlobPair(), newBlobPair()
	for _, p := range []blobPair{whole, split} {
		a, aPath := exporterFile(t, "A")
		seedBodies(t, aPath)
		e := NewExporter(a, p.link(t, "A"))
		if p == split {
			e.blobCap = 100 << 10
		}
		if _, err := e.Export(); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	if got, base := batchesFrom(t, split), batchesFrom(t, whole); got <= base {
		t.Fatalf("batches with a 100 KiB cap = %d, without = %d; want the capped drain split", got, base)
	}
	b, _ := exporterFile(t, "B")
	if _, err := NewImporter(b, split.link(t, "B")).Import(); err != nil {
		t.Fatalf("import the split batches: %v", err)
	}
	if got := storedColumns(t, b); len(got) != 3 {
		t.Errorf("B holds %d values after the split export, want 3", len(got))
	}
}

// A reconcile chunk ends once its bodies pass the budget, rather than at the row
// count alone.
func TestReconcileChunkEndsAtTheBlobBudget(t *testing.T) {
	t.Parallel()
	var batches [2]int
	for n, budget := range []int64{MaxBatchBlobBytes, 100 << 10} {
		p := newBlobPair()
		a, aPath := exporterFile(t, "A")
		seedBodies(t, aPath)
		r := NewReconciler(a, p.link(t, "A"))
		r.blobBudget = budget
		res, err := r.Reconcile()
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		batches[n] = res.Batches
	}
	if batches[1] <= batches[0] {
		t.Fatalf("batches with a 100 KiB budget = %d, without = %d; want the chunk cut early", batches[1], batches[0])
	}
}
