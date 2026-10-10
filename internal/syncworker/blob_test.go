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

// TestPutBlobUploadsTheStagedBody pins the ordinary put: the object lands under
// its key, the reply carries the exact byte count, and Skipped is false because
// nothing was there before.
func TestPutBlobUploadsTheStagedBody(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	body := []byte("the bytes of one stored value")
	path, key := staged(t, dir, "one", body)

	backend := &fakeBackend{}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlob, Key: key, Staging: path},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("put_blob refused: %s", rep.Error)
	}
	if rep.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", rep.Bytes, len(body))
	}
	if rep.Skipped {
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
	dir := t.TempDir()
	path, _ := staged(t, dir, "one", []byte("the bytes one machine meant to publish"))
	wrongKey := blobstore.Key("origin-a", hexSHA("a different body entirely"))

	backend := &fakeBackend{}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlob, Key: wrongKey, Staging: path},
	)
	rep := replies[1]
	if rep.OK {
		t.Fatal("a body that is not its key's digest was published anyway")
	}
	if rep.Code != CodeBlobRefused {
		t.Errorf("code = %q, want %q", rep.Code, CodeBlobRefused)
	}
	if has, _ := store.Has(t.Context(), wrongKey); has {
		t.Error("the wrong body reached the store")
	}
}

// TestPutBlobSkipsExisting pins that a second put of the same body sends
// nothing: the object is already there, so the reply says skipped and the store
// records no further upload. The mutation is removing the Has check, which would
// re-upload and answer with a byte count a caller would add to the month's total.
func TestPutBlobSkipsExisting(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	body := []byte("a body two machines both hold")
	path, key := staged(t, dir, "one", body)
	if err := store.Put(t.Context(), key, bytesReader(body), int64(len(body))); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	putsBefore := store.Puts

	backend := &fakeBackend{}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlob, Key: key, Staging: path},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("put_blob refused: %s", rep.Error)
	}
	if !rep.Skipped {
		t.Error("Skipped = false for an object the store already held")
	}
	if rep.Bytes != 0 {
		t.Errorf("bytes = %d, want 0: a skip moved nothing", rep.Bytes)
	}
	if store.Puts != putsBefore {
		t.Errorf("the store took %d uploads, want none", store.Puts-putsBefore)
	}
}

// TestGetBlobWritesTheWholeFile pins the fetch: the bytes land at the staging
// path named, no .part is left behind, and the reply carries the length.
func TestGetBlobWritesTheWholeFile(t *testing.T) {
	store := withStore(t)
	body := []byte("a body this machine is about to apply")
	sum := sha256.Sum256(body)
	key := blobstore.Key("origin-b", hex.EncodeToString(sum[:]))
	if err := store.Put(t.Context(), key, bytesReader(body), int64(len(body))); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	staging := filepath.Join(t.TempDir(), hex.EncodeToString(sum[:]))

	backend := &fakeBackend{}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbGetBlob, Key: key, Staging: staging, Max: 64 << 20},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("get_blob refused: %s", rep.Error)
	}
	if rep.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", rep.Bytes, len(body))
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

	backend := &fakeBackend{}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbGetBlob, Key: missing, Staging: staging, Max: 64 << 20},
	)
	rep := replies[1]
	if rep.OK {
		t.Fatal("a fetch of an absent object succeeded")
	}
	if rep.Code != CodeBlobMissing {
		t.Errorf("code = %q, want %q", rep.Code, CodeBlobMissing)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("a failed fetch left a file where the importer would read one")
	}
}

// TestBlobVerbsRefuseWithoutR2Settings pins that a hello with no bucket answers
// the blob verbs with a message that says so, rather than panicking on a nil
// store or silently succeeding without moving anything.
func TestBlobVerbsRefuseWithoutR2Settings(t *testing.T) {
	withStore(t)
	staging := filepath.Join(t.TempDir(), "nothing")

	for _, verb := range []Verb{VerbPutBlob, VerbGetBlob} {
		backend := &fakeBackend{}
		replies := serve(t, backend,
			Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t"},
			Request{ID: "2", Verb: verb, Key: blobstore.Key("origin-a", hexSHA("x")), Staging: staging, Max: 64 << 20},
		)
		rep := replies[1]
		if rep.OK {
			t.Errorf("%s answered without an R2 configuration", verb)
			continue
		}
		if rep.Error == "" {
			t.Errorf("%s refused with no reason", verb)
		}
	}
}

// TestBlobBytesAreCountedInStats pins that what the two verbs move reaches the
// stats reply, and that a skip adds nothing: the month is billed for bytes that
// were sent, and an upload of a body another machine already had sent none.
func TestBlobBytesAreCountedInStats(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	body := []byte("a body counted once")
	path, key := staged(t, dir, "one", body)
	keyForFetch := blobstore.Key("origin-a", hexSHA("a different body to fetch"))

	backend := &fakeBackend{stats: Stats{Entries: 3, Origins: 1, Seq: 2}}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlob, Key: key, Staging: path},
		Request{ID: "3", Verb: VerbPutBlob, Key: key, Staging: path},
		Request{ID: "4", Verb: VerbGetBlob, Key: keyForFetch, Staging: filepath.Join(dir, "fetched"), Max: 64 << 20},
		Request{ID: "5", Verb: VerbStats},
	)
	stats := replies[4].Stats
	if stats == nil {
		t.Fatal("the stats reply carried no stats")
	}
	if stats.R2Put != int64(len(body)) {
		t.Errorf("r2_put = %d, want %d: the skip must add nothing", stats.R2Put, len(body))
	}
	if stats.R2Get != 0 {
		t.Errorf("r2_get = %d, want 0 for a fetch that was refused", stats.R2Get)
	}
	if stats.Entries != 3 {
		t.Errorf("the log's own size was lost: %+v", stats)
	}
	// The store still holds nothing under the fetch key, which is what makes the
	// zero above the right answer rather than an accident.
	if has, _ := store.Has(t.Context(), keyForFetch); has {
		t.Error("the fetch key is in the store before anything fetched it")
	}
}

// TestBlobVerbsRefuseATransferTheStoreCannotMake pins the two failure paths a
// bucket produces: a store that cannot be asked, and a store that cannot be
// written to. Both are refusals rather than successes with a zero byte count --
// a caller that read zero bytes as success would append an entry pointing at an
// object that was never stored.
func TestBlobVerbsRefuseATransferTheStoreCannotMake(t *testing.T) {
	broken := &failingStore{err: errors.New("the bucket is unreachable")}
	s := &server{backend: &fakeBackend{}, blobs: broken}
	s.spec = &Spec{Origin: "origin-a"}
	key := blobstore.Key("origin-a", hexSHA("a body"))
	staging := filepath.Join(t.TempDir(), "staged")

	for _, verb := range []Verb{VerbPutBlob, VerbGetBlob} {
		resp := s.dispatch(Request{ID: "1", Verb: verb, Key: key, Staging: staging, Max: 64 << 20})
		if resp.OK {
			t.Errorf("%s answered OK against a store that failed", verb)
		}
		if resp.Code != "" {
			t.Errorf("%s refused with class %q, want none: a fault a retry can get past", verb, resp.Code)
		}
	}
	// An object the store never held is a different fact from a store that could
	// not answer, and it is the one the daemon latches on.
	absent := &failingStore{notFound: true}
	s.blobs = absent
	resp := s.dispatch(Request{ID: "1", Verb: VerbGetBlob, Key: key, Staging: staging, Max: 64 << 20})
	if resp.Code != CodeBlobMissing {
		t.Errorf("a 404 came back as %q, want %q", resp.Code, CodeBlobMissing)
	}
}

// TestPutBlobRefusesAKeyWithNoDigest pins that a key naming no body is refused
// rather than compared against nothing. Such a key would publish an object that
// no fetch could ever verify, because there is no digest to check the bytes
// against.
func TestPutBlobRefusesAKeyWithNoDigest(t *testing.T) {
	withStore(t)
	body := []byte("a body this machine staged")
	path, _ := staged(t, t.TempDir(), "one", body)
	backend := &fakeBackend{}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a",
			URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlob, Key: "origin-a/", Staging: path},
	)
	rep := replies[1]
	if rep.OK {
		t.Fatal("a key naming no digest was published anyway")
	}
	if rep.Code != CodeBlobRefused {
		t.Errorf("code = %q, want %q", rep.Code, CodeBlobRefused)
	}
}

// TestPutBlobRefusesAStagingFileThatIsNotThere pins that a call naming a file
// this machine does not have is a refusal naming why, rather than a panic on the
// open or an empty success the caller would read as stored.
func TestPutBlobRefusesAStagingFileThatIsNotThere(t *testing.T) {
	withStore(t)
	backend := &fakeBackend{}
	replies := serve(t, backend,
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a",
			URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlob,
			Key:     blobstore.Key("origin-a", hexSHA("never staged")),
			Staging: filepath.Join(t.TempDir(), "absent")},
	)
	rep := replies[1]
	if rep.OK {
		t.Fatal("a put of a file that does not exist succeeded")
	}
	if rep.Error == "" {
		t.Error("the refusal carried no reason")
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
