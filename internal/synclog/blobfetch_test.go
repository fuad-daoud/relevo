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
