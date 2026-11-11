package syncworker

// The blob verbs, against a MemBlobStore and no network. Every case here is
// about what a staging folder and a key are allowed to produce: a body published
// under a digest it does not have, a half-written file where a whole one was
// expected, and a missing object answered as an ordinary fault.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// staged writes body to the staging folder under name and returns its path and
// the key that names it, which is the pair every call in this file takes.
func staged(t *testing.T, dir, name string, body []byte) (path, key string) {
	t.Helper()
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	path = filepath.Join(dir, digest)
	if err := os.WriteFile(path, body, stagingMode); err != nil {
		t.Fatalf("stage %s: %v", path, err)
	}
	return path, blobstore.Key("origin-a", digest)
}

// withStore hands every blob verb the same fake the tests use, and puts the
// store back afterwards so no test leaks a substituted constructor into another.
func withStore(t *testing.T) *blobstore.MemBlobStore {
	t.Helper()
	store := blobstore.NewMemBlobStore(nil)
	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return store }
	t.Cleanup(func() { newBlobStore = restore })
	return store
}

// serve drives the requests through one server, the way the pipe does: a single
// server holds the handshake and the counters between the verbs, so a test that
// rebuilt one per request would be testing a pipe that resets after every line.
func serve(t *testing.T, b Backend, requests ...Request) []Response {
	t.Helper()
	s := &server{backend: b}
	out := make([]Response, 0, len(requests))
	for _, req := range requests {
		out = append(out, s.dispatch(req))
	}
	return out
}

// hello is the handshake every blob call follows: a bucket is configured, so
// the store withStore installed is the one the verbs reach.
func hello() Request {
	return Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}}
}

// oneBlob sends verb a batch of the one item after a hello and returns that
// item's result. A refusal of the whole call fails the test: past the hello,
// a key's own failure rides in its result.
func oneBlob(t *testing.T, verb Verb, item BlobItem) BlobResult {
	t.Helper()
	rep := serve(t, &fakeBackend{}, hello(), Request{ID: "2", Verb: verb, Blobs: []BlobItem{item}})[1]
	if !rep.OK {
		t.Fatalf("%s refused the whole call: %s", verb, rep.Error)
	}
	if len(rep.Results) != 1 {
		t.Fatalf("%s results = %+v, want one", verb, rep.Results)
	}
	return rep.Results[0]
}

// TestPutBlobUploadsTheStagedBody pins the ordinary put: the object lands under
// its key, the result carries the exact byte count, and Skipped is false
// because nothing was there before.
func TestPutBlobUploadsTheStagedBody(t *testing.T) {
	store := withStore(t)
	body := []byte("the bytes of one stored value")
	path, key := staged(t, t.TempDir(), "one", body)

	res := oneBlob(t, VerbPutBlobs, BlobItem{Key: key, Staging: path})
	if res.Error != "" {
		t.Fatalf("put refused: %s", res.Error)
	}
	if res.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", res.Bytes, len(body))
	}
	if res.Skipped {
		t.Error("a first put skipped, want an upload")
	}
	got, err := store.Get(t.Context(), key, discard{})
	if err != nil || got != int64(len(body)) {
		t.Errorf("stored %d bytes (err %v), want %d", got, err, len(body))
	}
}

// discard throws a fetched body away; the store's own copy is what a test reads.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// TestPutBlobRefusesDigestMismatch is the check that stands between a wrong file
// and a published object. The staged file is named for one digest and the key
// names another, which is what a caller that reused a staging path would produce.
//
// The mutation is dropping the digest check: with it gone the body is stored
// under the key, and every machine that later fetched it would find bytes that
// do not match the entry pointing at them.
func TestPutBlobRefusesDigestMismatch(t *testing.T) {
	store := withStore(t)
	path, _ := staged(t, t.TempDir(), "one", []byte("the bytes one machine meant to publish"))
	wrongKey := blobstore.Key("origin-a", hexSHA("a different body entirely"))

	res := oneBlob(t, VerbPutBlobs, BlobItem{Key: wrongKey, Staging: path})
	if res.Error == "" {
		t.Fatal("a body that is not its key's digest was published anyway")
	}
	if res.Code != CodeBlobRefused {
		t.Errorf("code = %q, want %q", res.Code, CodeBlobRefused)
	}
	if has, _ := store.Has(t.Context(), wrongKey); has {
		t.Error("the wrong body reached the store")
	}
}

// TestPutBlobSkipsExisting pins that a second put of the same body sends
// nothing: the object is already there, so the result says skipped and the
// store records no further upload. The mutation is removing the Has check,
// which would re-upload and answer with a byte count a caller would add to the
// month's total.
func TestPutBlobSkipsExisting(t *testing.T) {
	store := withStore(t)
	body := []byte("a body two machines both hold")
	path, key := staged(t, t.TempDir(), "one", body)
	if err := store.Put(t.Context(), key, bytesReader(body), int64(len(body))); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	putsBefore := store.Puts

	res := oneBlob(t, VerbPutBlobs, BlobItem{Key: key, Staging: path})
	if res.Error != "" {
		t.Fatalf("put refused: %s", res.Error)
	}
	if !res.Skipped {
		t.Error("Skipped = false for an object the store already held")
	}
	if res.Bytes != 0 {
		t.Errorf("bytes = %d, want 0: a skip moved nothing", res.Bytes)
	}
	if store.Puts != putsBefore {
		t.Errorf("the store took %d uploads, want none", store.Puts-putsBefore)
	}
}

// TestGetBlobWritesTheWholeFile pins the fetch: the bytes land at the staging
// path named, no .part is left behind, and the result carries the length.
func TestGetBlobWritesTheWholeFile(t *testing.T) {
	store := withStore(t)
	body := []byte("a body this machine is about to apply")
	sum := sha256.Sum256(body)
	key := blobstore.Key("origin-b", hex.EncodeToString(sum[:]))
	if err := store.Put(t.Context(), key, bytesReader(body), int64(len(body))); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	staging := filepath.Join(t.TempDir(), hex.EncodeToString(sum[:]))

	res := oneBlob(t, VerbGetBlobs, BlobItem{Key: key, Staging: staging, Max: 64 << 20})
	if res.Error != "" {
		t.Fatalf("get refused: %s", res.Error)
	}
	if res.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", res.Bytes, len(body))
	}
	got, err := os.ReadFile(staging)
	if err != nil {
		t.Fatalf("read the staged body: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("staged %q, want %q", got, body)
	}
	if _, err := os.Stat(staging + partSuffix); !os.IsNotExist(err) {
		t.Error("the .part file was left behind after the rename")
	}
}

// TestGetBlobMissingIsBlobMissing pins that a body the store does not hold is
// its own refusal class rather than a generic error, because the client maps
// that class to the sentinel the importer latches on. The mutation is returning
// a plain error on a 404: the client would then see an ordinary refusal and the
// importer would retry a body that is not going to appear.
func TestGetBlobMissingIsBlobMissing(t *testing.T) {
	withStore(t)
	staging := filepath.Join(t.TempDir(), "absent")
	missing := blobstore.Key("origin-b", hexSHA("nothing stored under this"))

	res := oneBlob(t, VerbGetBlobs, BlobItem{Key: missing, Staging: staging, Max: 64 << 20})
	if res.Error == "" {
		t.Fatal("a fetch of an absent object succeeded")
	}
	if res.Code != CodeBlobMissing {
		t.Errorf("code = %q, want %q", res.Code, CodeBlobMissing)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("a failed fetch left a file where the importer would read one")
	}
}

// TestBatchVerbsRefuseWithoutR2Settings pins that a non-empty batch is a
// whole-call refusal when hello named no bucket: the guard stands before any
// transfer, so neither verb moves a body it has nowhere to put.
func TestBatchVerbsRefuseWithoutR2Settings(t *testing.T) {
	withStore(t)
	dir := t.TempDir()
	path, key := staged(t, dir, "one", []byte("a body with nowhere to go"))

	batches := []Request{
		{ID: "2", Verb: VerbPutBlobs, Blobs: []BlobItem{{Key: key, Staging: path}}},
		{ID: "2", Verb: VerbGetBlobs, Blobs: []BlobItem{{Key: key, Staging: filepath.Join(dir, "fetched"), Max: 64 << 20}}},
	}
	for _, batch := range batches {
		replies := serve(t, &fakeBackend{},
			Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t"},
			batch,
		)
		rep := replies[1]
		if rep.OK {
			t.Errorf("%s answered a non-empty batch without an R2 configuration", batch.Verb)
			continue
		}
		if rep.Error == "" {
			t.Errorf("%s refused with no reason", batch.Verb)
		}
	}
}

// TestBlobBytesAreCountedInStats pins that what the verbs move reaches the
// stats reply, and that a skip adds nothing: the month is billed for bytes
// that were sent, and an upload of a body another machine already had sent
// none.
func TestBlobBytesAreCountedInStats(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	body := []byte("a body counted once")
	path, key := staged(t, dir, "one", body)
	other := []byte("a second body, counted in a batch")
	otherPath, otherKey := staged(t, dir, "two", other)
	keyForFetch := blobstore.Key("origin-a", hexSHA("a different body to fetch"))
	fetchBody := []byte("a body a batch fetch brings home")
	fetchKey := blobstore.Key("origin-b", hexSHA(string(fetchBody)))
	if err := store.Put(t.Context(), fetchKey, bytesReader(fetchBody), int64(len(fetchBody))); err != nil {
		t.Fatalf("seed the store: %v", err)
	}

	backend := &fakeBackend{stats: Stats{Entries: 3, Origins: 1, Seq: 2}}
	replies := serve(t, backend,
		hello(),
		Request{ID: "2", Verb: VerbPutBlobs, Blobs: []BlobItem{{Key: key, Staging: path}}},
		Request{ID: "3", Verb: VerbPutBlobs, Blobs: []BlobItem{{Key: key, Staging: path}, {Key: otherKey, Staging: otherPath}}},
		Request{ID: "4", Verb: VerbGetBlobs, Blobs: []BlobItem{{Key: keyForFetch, Staging: filepath.Join(dir, "fetched"), Max: 64 << 20}}},
		Request{ID: "5", Verb: VerbGetBlobs, Blobs: []BlobItem{{Key: fetchKey, Staging: filepath.Join(dir, "fetched-ok"), Max: 64 << 20}}},
		Request{ID: "6", Verb: VerbStats},
	)
	batch := replies[2]
	if !batch.OK {
		t.Fatalf("put_blobs refused: %s", batch.Error)
	}
	if len(batch.Results) != 2 || !batch.Results[0].Skipped || batch.Results[1].Skipped {
		t.Errorf("put_blobs results = %+v, want a skip then an upload", batch.Results)
	}
	fetch := replies[3]
	if !fetch.OK {
		t.Fatalf("get_blobs refused the whole call: %s", fetch.Error)
	}
	if len(fetch.Results) != 1 || fetch.Results[0].Code != CodeBlobMissing {
		t.Errorf("get_blobs results = %+v, want one missing body", fetch.Results)
	}
	hit := replies[4]
	if !hit.OK {
		t.Fatalf("get_blobs refused a fetch the store holds: %s", hit.Error)
	}
	if len(hit.Results) != 1 || hit.Results[0].Error != "" {
		t.Fatalf("get_blobs results = %+v, want one fetched body", hit.Results)
	}
	if hit.Results[0].Bytes != int64(len(fetchBody)) {
		t.Errorf("get_blobs bytes = %d, want %d", hit.Results[0].Bytes, len(fetchBody))
	}
	if got, err := os.ReadFile(filepath.Join(dir, "fetched-ok")); err != nil || string(got) != string(fetchBody) {
		t.Errorf("staged %q (err %v), want %q", got, err, fetchBody)
	}
	stats := replies[5].Stats
	if stats == nil {
		t.Fatal("the stats reply carried no stats")
	}
	if stats.R2Put != int64(len(body)+len(other)) {
		t.Errorf("r2_put = %d, want %d: the skip must add nothing", stats.R2Put, len(body)+len(other))
	}
	if stats.R2Get != int64(len(fetchBody)) {
		t.Errorf("r2_get = %d, want %d for the fetched body", stats.R2Get, len(fetchBody))
	}
	if stats.Entries != 3 {
		t.Errorf("the log's own size was lost: %+v", stats)
	}
	// The refused fetch added nothing: the store still holds nothing under its
	// key, so the R2Get count above comes from the successful fetch alone.
	if has, _ := store.Has(t.Context(), keyForFetch); has {
		t.Error("the fetch key is in the store before anything fetched it")
	}
}

// TestBlobVerbsRefuseATransferTheStoreCannotMake pins the two failure paths a
// bucket produces: a store that cannot be asked, and a store that cannot be
// written to. Both are failed results rather than successes with a zero byte
// count -- a caller that read zero bytes as success would append an entry
// pointing at an object that was never stored.
func TestBlobVerbsRefuseATransferTheStoreCannotMake(t *testing.T) {
	broken := &failingStore{err: errors.New("the bucket is unreachable")}
	s := &server{backend: &fakeBackend{}, blobs: broken}
	s.spec = &Spec{Origin: "origin-a"}
	key := blobstore.Key("origin-a", hexSHA("a body"))
	staging := filepath.Join(t.TempDir(), "staged")
	item := BlobItem{Key: key, Staging: staging, Max: 64 << 20}

	for _, verb := range []Verb{VerbPutBlobs, VerbGetBlobs} {
		resp := s.dispatch(Request{ID: "1", Verb: verb, Blobs: []BlobItem{item}})
		if len(resp.Results) != 1 || resp.Results[0].Error == "" {
			t.Errorf("%s results = %+v, want one failure against a store that failed", verb, resp.Results)
			continue
		}
		if code := resp.Results[0].Code; code != "" {
			t.Errorf("%s failed with class %q, want none: a fault a retry can get past", verb, code)
		}
	}
	// An object the store never held is a different fact from a store that could
	// not answer, and it is the one the daemon latches on.
	s.blobs = &failingStore{notFound: true}
	resp := s.dispatch(Request{ID: "1", Verb: VerbGetBlobs, Blobs: []BlobItem{item}})
	if len(resp.Results) != 1 || resp.Results[0].Code != CodeBlobMissing {
		t.Errorf("a 404 came back as %+v, want %q", resp.Results, CodeBlobMissing)
	}
}

// TestPutBlobRefusesAKeyWithNoDigest pins that a key naming no body is refused
// rather than compared against nothing. Such a key would publish an object that
// no fetch could ever verify, because there is no digest to check the bytes
// against.
func TestPutBlobRefusesAKeyWithNoDigest(t *testing.T) {
	withStore(t)
	path, _ := staged(t, t.TempDir(), "one", []byte("a body this machine staged"))

	res := oneBlob(t, VerbPutBlobs, BlobItem{Key: "origin-a/", Staging: path})
	if res.Error == "" {
		t.Fatal("a key naming no digest was published anyway")
	}
	if res.Code != CodeBlobRefused {
		t.Errorf("code = %q, want %q", res.Code, CodeBlobRefused)
	}
}

// TestPutBlobRefusesAStagingFileThatIsNotThere pins that a call naming a file
// this machine does not have is a failure naming why, rather than a panic on
// the open or an empty success the caller would read as stored.
func TestPutBlobRefusesAStagingFileThatIsNotThere(t *testing.T) {
	withStore(t)

	res := oneBlob(t, VerbPutBlobs, BlobItem{
		Key:     blobstore.Key("origin-a", hexSHA("never staged")),
		Staging: filepath.Join(t.TempDir(), "absent"),
	})
	if res.Error == "" {
		t.Fatal("a put of a file that does not exist succeeded")
	}
}

// failingStore is a store that cannot be reached, and on demand answers that the
// object is not there instead. It is the two ways a bucket fails a call, which
// call for opposite handling on the far side.
type failingStore struct {
	err      error
	notFound bool
}

func (f *failingStore) Put(context.Context, string, io.Reader, int64) error { return f.err }

func (f *failingStore) Get(context.Context, string, io.Writer) (int64, error) {
	if f.notFound {
		return 0, fmt.Errorf("blobstore: mem get: %w", blobstore.ErrNotFound)
	}
	return 0, f.err
}

func (f *failingStore) Has(context.Context, string) (bool, error) { return false, f.err }

func (f *failingStore) List(context.Context, string) ([]blobstore.Object, error) {
	return nil, f.err
}

func (f *failingStore) Delete(context.Context, string) error { return f.err }

// bytesReader wraps a body as the reader the store's Put takes.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// hexSHA is the digest of s, used to build a key that names no stored object.
func hexSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
