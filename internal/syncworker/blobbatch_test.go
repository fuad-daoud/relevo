package syncworker

// The batched blob verbs, against a MemBlobStore and no network. Every case
// here is about what a batch is allowed to produce: overlapping transfers that
// never exceed their bound, results that stay in request order however the
// transfers finish, one key's failure leaving the rest successful, and a
// re-sent batch storing exactly its bodies.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// TestPutBlobsUploadsEveryKey pins the ordinary batch put: every object lands
// under its key, each result carries its own byte count in request order, and
// the reply stays a single answer for the whole batch.
func TestPutBlobsUploadsEveryKey(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	var items []BlobItem
	var bodies [][]byte
	for i := 0; i < 3; i++ {
		body := []byte(fmt.Sprintf("batch body number %d", i))
		path, key := staged(t, dir, fmt.Sprintf("body-%d", i), body)
		items = append(items, BlobItem{Key: key, Staging: path})
		bodies = append(bodies, body)
	}

	replies := serve(t, &fakeBackend{},
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlobs, Blobs: items},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("put_blobs refused: %s", rep.Error)
	}
	if len(rep.Results) != len(items) {
		t.Fatalf("results = %d, want one per key", len(rep.Results))
	}
	for n, r := range rep.Results {
		if r.Key != items[n].Key {
			t.Errorf("results[%d].key = %s, want %s", n, r.Key, items[n].Key)
		}
		if r.Error != "" {
			t.Errorf("results[%d] failed: %s", n, r.Error)
			continue
		}
		if r.Bytes != int64(len(bodies[n])) {
			t.Errorf("results[%d].bytes = %d, want %d", n, r.Bytes, len(bodies[n]))
		}
		if r.Skipped {
			t.Errorf("results[%d] skipped a body the store did not hold", n)
		}
		if has, _ := store.Has(t.Context(), items[n].Key); !has {
			t.Errorf("the store is missing %s", items[n].Key)
		}
	}
}

// peakStore wraps a MemBlobStore, recording the peak in-flight Puts and
// holding each one briefly, so a test can prove a batch overlaps its transfers
// without ever exceeding its bound.
type peakStore struct {
	*blobstore.MemBlobStore
	mu       sync.Mutex
	inflight int
	peak     int
}

func (p *peakStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	p.mu.Lock()
	p.inflight++
	if p.inflight > p.peak {
		p.peak = p.inflight
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inflight--
		p.mu.Unlock()
	}()
	time.Sleep(50 * time.Millisecond)
	return p.MemBlobStore.Put(ctx, key, body, size)
}

func (p *peakStore) max() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// TestPutBlobsOverlapsTransfers pins that one batch keeps several transfers in
// flight at once, and never more than the bound: eight keys peak between two
// and four.
//
// The mutation is forcing the bound to one, which serialises the batch and
// peaks at one.
func TestPutBlobsOverlapsTransfers(t *testing.T) {
	peaked := &peakStore{MemBlobStore: blobstore.NewMemBlobStore(nil)}
	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return peaked }
	t.Cleanup(func() { newBlobStore = restore })

	dir := t.TempDir()
	var items []BlobItem
	for i := 0; i < 8; i++ {
		path, key := staged(t, dir, fmt.Sprintf("body-%d", i), []byte(fmt.Sprintf("overlapping body number %d", i)))
		items = append(items, BlobItem{Key: key, Staging: path})
	}

	replies := serve(t, &fakeBackend{},
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlobs, Blobs: items},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("put_blobs refused: %s", rep.Error)
	}
	for n, r := range rep.Results {
		if r.Error != "" {
			t.Errorf("results[%d] failed: %s", n, r.Error)
		}
	}
	if got := peaked.max(); got < 2 || got > blobBatchWorkers {
		t.Errorf("peak in-flight transfers = %d, want between 2 and %d", got, blobBatchWorkers)
	}
}

// orderStore holds one key's upload until another's has started, so a test can
// force the second transfer to finish first and prove the results still come
// back in request order.
type orderStore struct {
	*blobstore.MemBlobStore
	first   string
	second  string
	release chan struct{}
	once    sync.Once
}

func (o *orderStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if key == o.second {
		o.once.Do(func() { close(o.release) })
	}
	if key == o.first {
		<-o.release
		time.Sleep(50 * time.Millisecond)
	}
	return o.MemBlobStore.Put(ctx, key, body, size)
}

// TestPutBlobsKeepsResultsInRequestOrder pins that results come back in
// request order whatever order the transfers finish in: the first key's upload
// waits until the second has landed, so a reply assembled in completion order
// would name the second key first.
func TestPutBlobsKeepsResultsInRequestOrder(t *testing.T) {
	dir := t.TempDir()
	firstBody := []byte("the body that finishes second")
	secondBody := []byte("the body that finishes first")
	path1, key1 := staged(t, dir, "first", firstBody)
	path2, key2 := staged(t, dir, "second", secondBody)
	gated := &orderStore{
		MemBlobStore: blobstore.NewMemBlobStore(nil),
		first:        key1,
		second:       key2,
		release:      make(chan struct{}),
	}
	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return gated }
	t.Cleanup(func() { newBlobStore = restore })

	replies := serve(t, &fakeBackend{},
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlobs, Blobs: []BlobItem{{Key: key1, Staging: path1}, {Key: key2, Staging: path2}}},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("put_blobs refused: %s", rep.Error)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results = %d, want one per key", len(rep.Results))
	}
	if rep.Results[0].Key != key1 || rep.Results[1].Key != key2 {
		t.Fatalf("results name %q then %q, want request order", rep.Results[0].Key, rep.Results[1].Key)
	}
	if rep.Results[0].Bytes != int64(len(firstBody)) || rep.Results[1].Bytes != int64(len(secondBody)) {
		t.Errorf("results carry %d and %d bytes, want %d and %d: the counts must follow their keys",
			rep.Results[0].Bytes, rep.Results[1].Bytes, len(firstBody), len(secondBody))
	}
}

// TestPutBlobsKeepsGoingPastADigestMismatch pins that one key's failure is no
// verdict on the rest: a body that is not its key's digest is refused with the
// single verb's class, and the other keys still upload.
func TestPutBlobsKeepsGoingPastADigestMismatch(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	path1, key1 := staged(t, dir, "one", []byte("the first good body"))
	pathBad, _ := staged(t, dir, "bad", []byte("a body that is not its key"))
	path3, key3 := staged(t, dir, "three", []byte("the third good body"))
	wrongKey := blobstore.Key("origin-a", hexSHA("a different body entirely"))

	replies := serve(t, &fakeBackend{},
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbPutBlobs, Blobs: []BlobItem{
			{Key: key1, Staging: path1},
			{Key: wrongKey, Staging: pathBad},
			{Key: key3, Staging: path3},
		}},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("put_blobs refused the whole call: %s", rep.Error)
	}
	if len(rep.Results) != 3 {
		t.Fatalf("results = %d, want one per key", len(rep.Results))
	}
	if rep.Results[0].Error != "" || rep.Results[2].Error != "" {
		t.Errorf("a good key failed: %+v", rep.Results)
	}
	if rep.Results[1].Error == "" {
		t.Error("the mismatched body was published anyway")
	}
	if rep.Results[1].Code != CodeBlobRefused {
		t.Errorf("code = %q, want %q", rep.Results[1].Code, CodeBlobRefused)
	}
	for _, key := range []string{key1, key3} {
		if has, _ := store.Has(t.Context(), key); !has {
			t.Errorf("the store is missing %s", key)
		}
	}
	if has, _ := store.Has(t.Context(), wrongKey); has {
		t.Error("the wrong body reached the store")
	}
}

// TestGetBlobsWritesEveryStagedBody pins the ordinary batch fetch: every body
// lands at its own staging path with its own byte count, and no .part is left
// behind.
func TestGetBlobsWritesEveryStagedBody(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	var items []BlobItem
	var bodies [][]byte
	for i := 0; i < 3; i++ {
		body := []byte(fmt.Sprintf("fetched batch body number %d", i))
		key := blobstore.Key("origin-b", hexSHA(string(body)))
		if err := store.Put(t.Context(), key, bytesReader(body), int64(len(body))); err != nil {
			t.Fatalf("seed the store: %v", err)
		}
		staging := filepath.Join(dir, fmt.Sprintf("body-%d", i))
		items = append(items, BlobItem{Key: key, Staging: staging, Max: 64 << 20})
		bodies = append(bodies, body)
	}

	replies := serve(t, &fakeBackend{},
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbGetBlobs, Blobs: items},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("get_blobs refused: %s", rep.Error)
	}
	if len(rep.Results) != len(items) {
		t.Fatalf("results = %d, want one per key", len(rep.Results))
	}
	for n, r := range rep.Results {
		if r.Key != items[n].Key {
			t.Errorf("results[%d].key = %s, want %s", n, r.Key, items[n].Key)
		}
		if r.Error != "" {
			t.Errorf("results[%d] failed: %s", n, r.Error)
			continue
		}
		if r.Bytes != int64(len(bodies[n])) {
			t.Errorf("results[%d].bytes = %d, want %d", n, r.Bytes, len(bodies[n]))
		}
		got, err := os.ReadFile(items[n].Staging)
		if err != nil {
			t.Errorf("read the staged body: %v", err)
			continue
		}
		if string(got) != string(bodies[n]) {
			t.Errorf("staged %q, want %q", got, bodies[n])
		}
		if _, err := os.Stat(items[n].Staging + partSuffix); !os.IsNotExist(err) {
			t.Errorf("the .part file was left behind for %s", items[n].Key)
		}
	}
}

// TestGetBlobsKeepsGoingPastAMissingObject pins that a body the store does not
// hold fails only its own result, with the single get's class, while the other
// keys still land.
func TestGetBlobsKeepsGoingPastAMissingObject(t *testing.T) {
	store := withStore(t)
	dir := t.TempDir()
	var items []BlobItem
	for i, body := range [][]byte{[]byte("first fetched body"), []byte("absent body"), []byte("third fetched body")} {
		key := blobstore.Key("origin-b", hexSHA(string(body)))
		if i != 1 {
			if err := store.Put(t.Context(), key, bytesReader(body), int64(len(body))); err != nil {
				t.Fatalf("seed the store: %v", err)
			}
		}
		items = append(items, BlobItem{Key: key, Staging: filepath.Join(dir, fmt.Sprintf("body-%d", i)), Max: 64 << 20})
	}

	replies := serve(t, &fakeBackend{},
		Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
		Request{ID: "2", Verb: VerbGetBlobs, Blobs: items},
	)
	rep := replies[1]
	if !rep.OK {
		t.Fatalf("get_blobs refused the whole call: %s", rep.Error)
	}
	if len(rep.Results) != 3 {
		t.Fatalf("results = %d, want one per key", len(rep.Results))
	}
	if rep.Results[0].Error != "" || rep.Results[2].Error != "" {
		t.Errorf("a present body failed: %+v", rep.Results)
	}
	if rep.Results[1].Error == "" {
		t.Error("a fetch of an absent object succeeded")
	}
	if rep.Results[1].Code != CodeBlobMissing {
		t.Errorf("code = %q, want %q", rep.Results[1].Code, CodeBlobMissing)
	}
	for _, n := range []int{0, 2} {
		if _, err := os.Stat(items[n].Staging); err != nil {
			t.Errorf("the staged body for %s is missing: %v", items[n].Key, err)
		}
	}
	if _, err := os.Stat(items[1].Staging); !os.IsNotExist(err) {
		t.Error("a failed fetch left a file where the importer would read one")
	}
	if _, err := os.Stat(items[1].Staging + partSuffix); !os.IsNotExist(err) {
		t.Error("a failed fetch left its .part behind")
	}
}

// TestEmptyBatchAnswersWithNoResults pins that a batch naming no keys is an
// immediate answer: no store is built and none is touched.
func TestEmptyBatchAnswersWithNoResults(t *testing.T) {
	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore {
		t.Error("an empty batch built a store")
		return blobstore.NewMemBlobStore(nil)
	}
	t.Cleanup(func() { newBlobStore = restore })

	for _, verb := range []Verb{VerbPutBlobs, VerbGetBlobs} {
		replies := serve(t, &fakeBackend{},
			Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}},
			Request{ID: "2", Verb: verb},
		)
		rep := replies[1]
		if !rep.OK {
			t.Errorf("%s refused an empty batch: %s", verb, rep.Error)
		}
		if len(rep.Results) != 0 {
			t.Errorf("%s answered %d results for no keys", verb, len(rep.Results))
		}
	}
}

// TestNewBlobStoreKeepsIdleConnectionsForABatch pins that the worker's store
// does not share the default client's two-idle-connections-per-host transport:
// a batch keeps four transfers in flight, and the extra two would otherwise
// each pay a new handshake per round trip.
//
// The mutation is building the store on http.DefaultClient, which answers with
// the default transport and fails both assertions.
func TestNewBlobStoreKeepsIdleConnectionsForABatch(t *testing.T) {
	made := newBlobStore(R2Config{
		Endpoint: "https://acct.r2.cloudflarestorage.com",
		Bucket:   "relevo-sync",
		KeyID:    "key-1",
		Secret:   "s",
	})
	s3, ok := made.(*blobstore.S3Store)
	if !ok {
		t.Fatalf("newBlobStore built %T, want *blobstore.S3Store", made)
	}
	if s3.Client == nil || s3.Client == http.DefaultClient {
		t.Fatal("the worker's store shares http.DefaultClient, which keeps two idle connections per host")
	}
	transport, ok := s3.Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the worker's store rides %T, want *http.Transport", s3.Client.Transport)
	}
	if transport.MaxIdleConnsPerHost < blobBatchWorkers {
		t.Errorf("MaxIdleConnsPerHost = %d, want at least %d", transport.MaxIdleConnsPerHost, blobBatchWorkers)
	}
}

// flakyStore fails one key's first upload, so a test can prove a re-sent batch
// stores exactly its bodies: the daemon discards the partial results and sends
// the whole batch again.
type flakyStore struct {
	*blobstore.MemBlobStore
	mu     sync.Mutex
	fail   string
	failed bool
}

func (f *flakyStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key == f.fail && !f.failed {
		f.failed = true
		return fmt.Errorf("the bucket is unreachable")
	}
	return f.MemBlobStore.Put(ctx, key, body, size)
}

// TestPutBlobsRetryStoresExactlyTheBatchBodies pins that re-sending a batch
// whose first pass failed one key stores every body once and only once: puts
// are idempotent by digest, so the second pass skips what the first stored and
// uploads only what it missed.
func TestPutBlobsRetryStoresExactlyTheBatchBodies(t *testing.T) {
	inner := blobstore.NewMemBlobStore(nil)
	dir := t.TempDir()
	var items []BlobItem
	var bodies [][]byte
	for i := 0; i < 3; i++ {
		body := []byte(fmt.Sprintf("retried batch body number %d", i))
		path, key := staged(t, dir, fmt.Sprintf("body-%d", i), body)
		items = append(items, BlobItem{Key: key, Staging: path})
		bodies = append(bodies, body)
	}
	flaky := &flakyStore{MemBlobStore: inner, fail: items[1].Key}
	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return flaky }
	t.Cleanup(func() { newBlobStore = restore })

	hello := Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}}
	batch := Request{ID: "2", Verb: VerbPutBlobs, Blobs: items}
	first := serve(t, &fakeBackend{}, hello, batch)[1]
	if !first.OK {
		t.Fatalf("the first put_blobs refused the whole call: %s", first.Error)
	}
	if first.Results[1].Error == "" {
		t.Fatal("the first pass stored everything, leaving nothing for the retry to prove")
	}
	if first.Results[1].Code != "" {
		t.Errorf("results[1].code = %q, want none: a fault a retry can get past", first.Results[1].Code)
	}
	if inner.Puts != 2 {
		t.Fatalf("the first pass stored %d bodies, want the two its failure did not touch", inner.Puts)
	}

	second := serve(t, &fakeBackend{}, hello, batch)[1]
	if !second.OK {
		t.Fatalf("the re-sent put_blobs refused the whole call: %s", second.Error)
	}
	for n, r := range second.Results {
		if r.Error != "" {
			t.Errorf("results[%d] failed on retry: %s", n, r.Error)
		}
	}
	for n, key := range []string{items[0].Key, items[1].Key, items[2].Key} {
		var buf bytes.Buffer
		if _, err := inner.Get(t.Context(), key, &buf); err != nil {
			t.Errorf("the store is missing %s after the retry: %v", key, err)
			continue
		}
		if buf.String() != string(bodies[n]) {
			t.Errorf("the store holds %q under %s, want %q", buf.String(), key, bodies[n])
		}
	}
	if inner.Puts != 3 {
		t.Errorf("the two passes stored %d bodies in all, want 3: one per key", inner.Puts)
	}
}

// TestGetBlobsRetryLeavesNoPartFiles pins that re-sending a batch whose first
// pass missed one body stages exactly its bodies: gets land beside the target
// and are renamed over it, so a failed first pass leaves no .part behind and
// the second pass writes every staged file whole.
func TestGetBlobsRetryLeavesNoPartFiles(t *testing.T) {
	inner := blobstore.NewMemBlobStore(nil)
	restore := newBlobStore
	newBlobStore = func(R2Config) blobstore.BlobStore { return inner }
	t.Cleanup(func() { newBlobStore = restore })

	dir := t.TempDir()
	var items []BlobItem
	var bodies [][]byte
	for i := 0; i < 3; i++ {
		body := []byte(fmt.Sprintf("restaged batch body number %d", i))
		key := blobstore.Key("origin-b", hexSHA(string(body)))
		if i != 1 {
			if err := inner.Put(t.Context(), key, bytesReader(body), int64(len(body))); err != nil {
				t.Fatalf("seed the store: %v", err)
			}
		}
		items = append(items, BlobItem{Key: key, Staging: filepath.Join(dir, fmt.Sprintf("body-%d", i)), Max: 64 << 20})
		bodies = append(bodies, body)
	}

	hello := Request{ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://x", Token: "t", R2: &R2Config{}}
	batch := Request{ID: "2", Verb: VerbGetBlobs, Blobs: items}
	first := serve(t, &fakeBackend{}, hello, batch)[1]
	if !first.OK {
		t.Fatalf("the first get_blobs refused the whole call: %s", first.Error)
	}
	if first.Results[1].Code != CodeBlobMissing {
		t.Fatalf("the first pass did not miss its body the expected way: %+v", first.Results[1])
	}
	assertNoPartFiles(t, dir)

	if err := inner.Put(t.Context(), items[1].Key, bytesReader(bodies[1]), int64(len(bodies[1]))); err != nil {
		t.Fatalf("seed the missing body: %v", err)
	}
	second := serve(t, &fakeBackend{}, hello, batch)[1]
	if !second.OK {
		t.Fatalf("the re-sent get_blobs refused the whole call: %s", second.Error)
	}
	for n, r := range second.Results {
		if r.Error != "" {
			t.Errorf("results[%d] failed on retry: %s", n, r.Error)
		}
	}
	for n, item := range items {
		got, err := os.ReadFile(item.Staging)
		if err != nil {
			t.Errorf("the staged body for %s is missing after the retry: %v", item.Key, err)
			continue
		}
		if string(got) != string(bodies[n]) {
			t.Errorf("staged %q for %s, want %q", got, item.Key, bodies[n])
		}
	}
	assertNoPartFiles(t, dir)
}

// assertNoPartFiles reports the staging files a failed transfer left behind: a
// fetch writes beside its target and renames over it, so any .part still
// present is a body the retry would read as complete.
func assertNoPartFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the staging folder: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partSuffix) {
			t.Errorf("%s left behind after its batch", e.Name())
		}
	}
}
