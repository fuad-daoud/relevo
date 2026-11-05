package syncpipe

// The client's blob half, over the same real pipe every other client test uses:
// this binary respawned as a worker, one call at a time. The bucket is an
// httptest server, so the worker's real S3 store runs against it -- what is
// pinned is the whole path the daemon takes, not a stub of it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// testBucket is an S3-shaped endpoint held in memory: enough of PUT, GET and
// HEAD for the real store to work against, and no more. The bodies are the only
// state, so a test can assert that what was staged is what was stored.
type testBucket struct {
	mu      sync.Mutex
	bodies  map[string][]byte
	srv     *httptest.Server
	Methods []string
	// FaultGets and FaultPuts name keys the bucket answers with a 500: a fault
	// the store can read neither as an absent object nor as a marked refusal,
	// which is how a test reaches a per-key failure a later attempt can get past.
	FaultGets map[string]bool
	FaultPuts map[string]bool
}

// newTestBucket starts a bucket on a local port and stops it with the test.
func newTestBucket(t *testing.T) *testBucket {
	t.Helper()
	b := &testBucket{bodies: make(map[string][]byte)}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(b.srv.Close)
	return b
}

// handle is the three verbs the store uses. The key is the path after the
// bucket's own name, which is what the store builds: path-style addressing.
func (b *testBucket) handle(w http.ResponseWriter, r *http.Request) {
	const prefix = "/bucket/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, prefix)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Methods = append(b.Methods, r.Method+" "+key)

	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if b.FaultPuts[key] {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>InternalError</Code></Error>`))
			return
		}
		b.bodies[key] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		body, ok := b.bodies[key]
		if !ok {
			// The shape the store reads a 404 by: an S3 error document.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code></Error>`))
			return
		}
		if b.FaultGets[key] {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>InternalError</Code></Error>`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case http.MethodHead:
		body, ok := b.bodies[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// body returns what the bucket holds under key.
func (b *testBucket) body(key string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.bodies[key]
	return body, ok
}

// count is how many bodies the bucket holds: a rerun of an idempotent batch
// must not leave more behind than the batch first put there.
func (b *testBucket) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.bodies)
}

// keyFor is the store's key for a body: the digest of what was staged.
func keyFor(body []byte) string {
	sum := sha256.Sum256(body)
	return relevosyncSyncworkerKey("origin-a", hex.EncodeToString(sum[:]))
}

// stagedPath writes body to a staging file and returns its path and its key,
// which is the pair every blob call takes.
func stagedPath(t *testing.T, body []byte) (path, key string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "staged")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("stage: %v", err)
	}
	return path, keyFor(body)
}

// stagedBatch writes each body into dir under its digest and returns the mover
// items naming them: the shape uploadPending hands the mover, so a client test
// drives the same request the exporter builds.
func stagedBatch(t *testing.T, dir string, bodies ...[]byte) []synclog.BlobBatchItem {
	t.Helper()
	items := make([]synclog.BlobBatchItem, len(bodies))
	for n, body := range bodies {
		digest := digestHex(body)
		path := filepath.Join(dir, digest)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("stage: %v", err)
		}
		items[n] = synclog.BlobBatchItem{Key: blobstore.Key("origin-a", digest), Staging: path}
	}
	return items
}

// itoa avoids pulling strconv in for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// blobHandshake is the handshake with a bucket this test controls. The R2 block
// is what the worker builds its store from, so the whole call path under test is
// the production one.
func blobHandshake(bucket *testBucket) Config {
	cfg := handshake()
	cfg.R2 = &syncworker.R2Config{
		Endpoint: bucket.srv.URL,
		Bucket:   "bucket",
		KeyID:    "key-1",
		Secret:   "secret-1",
	}
	return cfg
}

// TestPipeClientMovesBlobsOverThePipe pins the client's blob calls end to end
// against a real store: a staged batch stores every body under its digest, a
// second put of it answers every key as a skip with no bytes, and a fetched
// batch writes every whole body back out.
func TestPipeClientMovesBlobsOverThePipe(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	first := []byte("a body this machine is about to publish")
	second := []byte("another body riding the same call")

	c, err := startFakeWorker(t, modeBlobs, blobHandshake(bucket))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	results, err := c.PutBlobs(stagedBatch(t, dir, first, second))
	if err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want one verdict per key", len(results))
	}
	for n, body := range [][]byte{first, second} {
		if results[n].Bytes != int64(len(body)) || results[n].Skipped || results[n].Err != nil {
			t.Errorf("verdict %d = %+v, want the body's bytes moved with no failure", n, results[n])
		}
	}
	for n, body := range [][]byte{first, second} {
		if stored, ok := bucket.body(keyFor(body)); !ok || string(stored) != string(body) {
			t.Errorf("the bucket holds %q at verdict %d's key, want the staged body", stored, n)
		}
	}

	// The second put is the case worth pinning: the objects are already there,
	// so it must move no bytes and answer every key as a skip. A caller that
	// uploaded the same bodies twice would otherwise bill the month for the
	// second copies.
	results, err = c.PutBlobs(stagedBatch(t, dir, first, second))
	if err != nil {
		t.Fatalf("the second PutBlobs: %v", err)
	}
	for n, r := range results {
		if !r.Skipped || r.Bytes != 0 {
			t.Errorf("the second verdict %d = %+v, want a skip moving nothing", n, r)
		}
	}

	items := stagedBatch(t, dir, first, second)
	fetched := make([]synclog.BlobBatchItem, len(items))
	for n, item := range items {
		fetched[n] = synclog.BlobBatchItem{
			Key:     item.Key,
			Staging: filepath.Join(dir, "fetched-"+filepath.Base(item.Staging)),
			Max:     64 << 20,
		}
	}
	results, err = c.GetBlobs(fetched)
	if err != nil {
		t.Fatalf("GetBlobs: %v", err)
	}
	for n, body := range [][]byte{first, second} {
		if results[n].Err != nil || results[n].Bytes != int64(len(body)) {
			t.Fatalf("fetched verdict %d = %+v, want the body's bytes", n, results[n])
		}
		reread, err := os.ReadFile(fetched[n].Staging)
		if err != nil {
			t.Fatalf("read the fetched body: %v", err)
		}
		if string(reread) != string(body) {
			t.Errorf("the fetched body is %q, want %q", reread, body)
		}
	}
}

// TestPipeClientMapsAMissingBodyToTheSentinel pins that a batch key whose body
// the bucket does not hold answers as synclog.ErrBlobMissing under ErrRefused
// while its sibling keys answer with their bodies: the importer latches the
// origin on that sentinel, and a missing sibling must not collapse the keys
// the store did serve.
func TestPipeClientMapsAMissingBodyToTheSentinel(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	present := []byte("a body this machine did upload")
	c, err := startFakeWorker(t, modeBlobs, blobHandshake(bucket))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.PutBlobs(stagedBatch(t, dir, present)); err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}

	_, absent := stagedPath(t, []byte("a body that was never uploaded"))
	items := []synclog.BlobBatchItem{
		{Key: keyFor(present), Staging: filepath.Join(dir, "out-present"), Max: 64 << 20},
		{Key: absent, Staging: filepath.Join(dir, "out-absent"), Max: 64 << 20},
	}
	results, err := c.GetBlobs(items)
	if err == nil {
		t.Fatal("GetBlobs with a missing key = nil, want the batch refused")
	}
	if !errors.Is(err, synclog.ErrBlobMissing) || !errors.Is(err, ErrRefused) {
		t.Errorf("GetBlobs = %v, want synclog.ErrBlobMissing under ErrRefused", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want one verdict per key even when a sibling is missing", len(results))
	}
	if results[0].Err != nil || results[0].Bytes != int64(len(present)) {
		t.Errorf("the served sibling = %+v, want its body with no failure", results[0])
	}
	if !errors.Is(results[1].Err, synclog.ErrBlobMissing) || !errors.Is(results[1].Err, ErrRefused) {
		t.Errorf("the missing key's verdict = %v, want the sentinel mapped per key", results[1].Err)
	}
}

// TestPipeClientMapsADigestRefusalToItsSentinelPerKey pins that a put whose
// staged file is not the body its key names is refused under synclog.ErrInvalid
// as that key's verdict alone: the sibling bodies still publish, and the call
// stays a refusal rather than a broken pipe, because the worker answered.
func TestPipeClientMapsADigestRefusalToItsSentinelPerKey(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	good := []byte("a body stored under the key naming it")
	wrong := []byte("a body whose staged file is not its key's digest")
	mismatchKey := blobstore.Key("origin-a", digestHex([]byte("some other body entirely")))

	c, err := startFakeWorker(t, modeBlobs, blobHandshake(bucket))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	items := stagedBatch(t, dir, good, wrong)
	items[1].Key = mismatchKey

	results, err := c.PutBlobs(items)
	if err == nil {
		t.Fatal("PutBlobs with a mismatched key = nil, want the batch refused")
	}
	if !errors.Is(err, ErrRefused) || relevosync.IsPermanentRefusal(err) {
		t.Errorf("PutBlobs = %v, want a refusal a later staging rewrite can clear", err)
	}
	if results[0].Err != nil || results[0].Bytes != int64(len(good)) {
		t.Errorf("the good sibling = %+v, want its body published", results[0])
	}
	if !errors.Is(results[1].Err, synclog.ErrInvalid) {
		t.Errorf("the mismatched key's verdict = %v, want synclog.ErrInvalid mapped per key", results[1].Err)
	}
	if _, ok := bucket.body(keyFor(good)); !ok {
		t.Error("the bucket lost the sibling body a failed key did not poison")
	}
	if _, ok := bucket.body(mismatchKey); ok {
		t.Error("the bucket took the body that disagreed with its key")
	}
}

// TestPipeClientCarriesTheByteCounters pins that all four totals reach the
// daemon through the batch verbs. A reply that lost them would leave the month
// reading as a cost of nothing, which is the failure the counting exists to
// prevent.
func TestPipeClientCarriesTheByteCounters(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	c, err := startFakeWorker(t, modeBlobs, blobHandshake(bucket))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	bodies := [][]byte{
		[]byte("a body counted on its way out"),
		[]byte("a second body counted on the same call"),
	}
	if _, err := c.PutBlobs(stagedBatch(t, dir, bodies...)); err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	items := stagedBatch(t, dir, bodies...)
	for n := range items {
		items[n].Staging = filepath.Join(dir, "back-"+filepath.Base(items[n].Staging))
		items[n].Max = 64 << 20
	}
	if _, err := c.GetBlobs(items); err != nil {
		t.Fatalf("GetBlobs: %v", err)
	}

	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	// The log's own size comes from the fake backend, and the R2 totals from what
	// the two blob calls moved. Nothing doubles them: the counters are cumulative
	// and stats must not add to them a second time.
	if stats.Entries != 11 || stats.Origins != 2 || stats.Seq != 9 {
		t.Errorf("the log's size is %+v, want the fake worker's", stats)
	}
	var want int64
	for _, body := range bodies {
		want += int64(len(body))
	}
	if stats.R2Put != want || stats.R2Get != want {
		t.Errorf("the R2 totals are put=%d get=%d, want %d each", stats.R2Put, stats.R2Get, want)
	}
}

// TestPipeClientDropsAWorkerThatAnswersABatchShort pins that a batch reply
// answering some other number of keys than were asked ends the pipe: ids match
// and the reply decodes, so only the count says the worker lost step, and a
// verdict the client had to invent would be acted on as the mover's.
func TestPipeClientDropsAWorkerThatAnswersABatchShort(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	missed := 0
	cfg := blobHandshake(bucket)
	cfg.OnMiss = func(error) { missed++ }
	c, err := startFakeWorker(t, modeBatchShort, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	results, err := c.PutBlobs(stagedBatch(t, dir, []byte("one body"), []byte("a second body")))
	if err == nil {
		t.Fatal("PutBlobs over a short batch reply = nil, want the miss")
	}
	if errors.Is(err, ErrRefused) {
		t.Errorf("PutBlobs = %v, want a dead worker, not a refusal", err)
	}
	if results != nil {
		t.Errorf("results = %v, want no verdicts a caller could read", results)
	}
	if missed != 1 {
		t.Errorf("the miss was reported %d times, want once", missed)
	}
	// The worker is killed on the miss; wait for the exit so the next call
	// reliably reads the gone worker rather than racing a pipe write.
	if !waitForExit(t, c, 5*time.Second) {
		t.Fatal("the short-answering worker is still running after the miss")
	}
	if _, err := c.GetBlobs(stagedBatch(t, dir, []byte("third"))); err == nil ||
		!strings.Contains(err.Error(), "worker gone") {
		t.Errorf("a call after the short reply = %v, want the killed worker reported as gone", err)
	}
}

// TestBlobStagingDirSitsBesideTheReplica pins where bodies are written. It is
// derived from the shared path the same way the replica is, so the daemon and
// the worker cannot disagree about which state directory they are in.
func TestBlobStagingDirSitsBesideTheReplica(t *testing.T) {
	t.Parallel()
	shared := filepath.Join(t.TempDir(), "relevo.db")
	got := BlobStagingDir(shared)
	want := filepath.Join(filepath.Dir(shared), "sync-blobs")
	if got != want {
		t.Errorf("BlobStagingDir = %q, want %q", got, want)
	}
	if filepath.Dir(got) != filepath.Dir(ReplicaPath(shared)) {
		t.Errorf("the staging folder %q is not beside the replica %q", got, ReplicaPath(shared))
	}
}

// TestEnsureBlobStagingDirIsPrivate pins the mode. A body in that folder is a
// user's content that has not been stored anywhere else yet, so another account
// on this machine must not read it out of a directory its own user does not own.
// The check reads the real mode rather than the MkdirAll call, because an
// existing folder keeps the mode it already had.
func TestEnsureBlobStagingDirIsPrivate(t *testing.T) {
	t.Parallel()
	shared := filepath.Join(t.TempDir(), "relevo.db")
	dir, err := EnsureBlobStagingDir(shared)
	if err != nil {
		t.Fatalf("EnsureBlobStagingDir: %v", err)
	}
	if perm := modeOf(t, dir); perm != 0o700 {
		t.Errorf("the staging folder is %#o, want 0700", perm)
	}
	// A second call on an existing folder tightens it rather than trusting it: it
	// was created by an earlier run under whatever umask that run had.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("loosen the folder: %v", err)
	}
	if _, err := EnsureBlobStagingDir(shared); err != nil {
		t.Fatalf("EnsureBlobStagingDir on an existing folder: %v", err)
	}
	if perm := modeOf(t, dir); perm != 0o700 {
		t.Errorf("an existing folder stayed %#o, want it tightened to 0700", perm)
	}
}

// TestOpenSupervisorRefusesWithoutR2 pins that a machine holding no bucket is
// refused rather than handed a worker whose every blob call would refuse. It
// happens before a process is started, which is what keeps a refused enable from
// leaving a child behind.
func TestOpenSupervisorRefusesWithoutR2(t *testing.T) {
	t.Parallel()
	shared, local := wiringPair(t)
	seedRemoteAndToken(t, local)
	if _, err := OpenSupervisor(shared, local); !errors.Is(err, relevosync.ErrNoR2) {
		t.Errorf("OpenSupervisor = %v, want ErrNoR2", err)
	}
}

// TestOpenSupervisorRefusesAnIncompleteR2Set pins that three of the four
// credentials is not a configuration. A machine holding some of them would
// otherwise start a worker that fails at the first upload, with the enable
// already having imported everything.
func TestOpenSupervisorRefusesAnIncompleteR2Set(t *testing.T) {
	t.Parallel()
	shared, local := wiringPair(t)
	seedRemoteAndToken(t, local)
	partial := wiringR2
	partial.Secret = ""
	// Written field by field rather than through SetR2, which refuses exactly
	// this: the half-configured machine is the case, not a set the API allows.
	seedPartialR2(t, local, partial)
	if _, err := OpenSupervisor(shared, local); !errors.Is(err, relevosync.ErrNoR2) {
		t.Errorf("OpenSupervisor = %v, want ErrNoR2 for a half-configured set", err)
	}
}

// TestOpenSupervisorHandsOverNoCredentialOnTheCommandLine pins the shape of the
// worker's arguments. The token and the bucket credentials travel in the first
// request over the pipe, so nothing in argv names either: an argument list is
// readable by every process on this machine.
func TestOpenSupervisorHandsOverNoCredentialOnTheCommandLine(t *testing.T) {
	t.Parallel()
	shared, local := wiringPair(t)
	seedRemoteAndToken(t, local)
	if err := relevosync.SetR2(local, wiringR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	sup, err := OpenSupervisor(shared, local)
	if err != nil {
		t.Fatalf("OpenSupervisor: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close() })
	for _, arg := range sup.cfg.Args {
		if arg == wiringR2.Secret || arg == wiringR2.KeyID {
			t.Errorf("the worker arguments name a credential: %v", sup.cfg.Args)
		}
	}
}

// TestSupervisorMovesBodiesThroughTheStagingFolder pins the wiring the exporter
// and reconcile depend on: the supervisor is the mover, it names the same folder
// the daemon writes bodies into, and it answers a batch per key -- a sibling of
// a missing body still returns its verdict while the missing key carries the
// sentinel. The exporter takes its mover from the transport it was handed, so a
// supervisor that did not carry both would leave every value travelling inline
// with nothing to say why.
func TestSupervisorMovesBodiesThroughTheStagingFolder(t *testing.T) {
	t.Parallel()
	bucket := newTestBucket(t)
	dir := t.TempDir()
	sup := newSupervisor(t, blobWorkerCfg(bucket, dir), breakerOver(newMemKV()))

	// The folder the daemon stages into is the one the supervisor names, which is
	// what lets the exporter write files the worker will be told to read.
	if sup.BlobStagingDir() != dir {
		t.Fatalf("BlobStagingDir = %q, want %q", sup.BlobStagingDir(), dir)
	}

	first := bytes.Repeat([]byte("stored body "), 400)
	second := bytes.Repeat([]byte("another stored body "), 250)
	items := stagedBatch(t, dir, first, second)

	results, err := sup.PutBlobs(items)
	if err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	for n, body := range [][]byte{first, second} {
		if results[n].Skipped || results[n].Bytes != int64(len(body)) || results[n].Err != nil {
			t.Errorf("verdict %d = %+v, want the body moved with no failure", n, results[n])
		}
	}
	// A second batch of the same digests is the skip, which is what lets two rows
	// holding one value upload it once.
	results, err = sup.PutBlobs(items)
	if err != nil {
		t.Fatalf("the second PutBlobs: %v", err)
	}
	for n, r := range results {
		if !r.Skipped || r.Bytes != 0 || r.Err != nil {
			t.Errorf("the second verdict %d = %+v, want a skip moving nothing", n, r)
		}
	}

	fetched := []synclog.BlobBatchItem{
		{Key: items[0].Key, Staging: filepath.Join(dir, "fetched-first"), Max: 64 << 20},
		{Key: items[1].Key, Staging: filepath.Join(dir, "fetched-second"), Max: 64 << 20},
	}
	results, err = sup.GetBlobs(fetched)
	if err != nil {
		t.Fatalf("GetBlobs: %v", err)
	}
	for n, body := range [][]byte{first, second} {
		if results[n].Err != nil || results[n].Bytes != int64(len(body)) {
			t.Fatalf("fetched verdict %d = %+v, want the body's bytes", n, results[n])
		}
		got, err := os.ReadFile(fetched[n].Staging)
		if err != nil {
			t.Fatalf("read the fetched body: %v", err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("the fetched body %d is not the one that was stored", n)
		}
	}

	// The missing key is one verdict, not the batch's: its sibling still answers
	// with its body, and only the sentinel under the call's error marks the pair
	// the bucket lost.
	_, absent := stagedPath(t, []byte("a body nobody uploaded"))
	results, err = sup.GetBlobs([]synclog.BlobBatchItem{
		{Key: items[0].Key, Staging: filepath.Join(dir, "again-first"), Max: 64 << 20},
		{Key: absent, Staging: filepath.Join(dir, "absent"), Max: 64 << 20},
	})
	if !errors.Is(err, synclog.ErrBlobMissing) {
		t.Fatalf("a batch with a missing key = %v, want the sentinel", err)
	}
	if len(results) != 2 || results[0].Err != nil || results[0].Bytes != int64(len(first)) {
		t.Errorf("the served sibling = %+v, want its verdict beside the failed key", results)
	}
	if results[1].Err == nil || !errors.Is(results[1].Err, synclog.ErrBlobMissing) {
		t.Errorf("the missing key = %v, want its own sentinel verdict", results[1].Err)
	}
}

// blobWorkerCfg is the supervisor config a blob test drives: this binary as a
// blob worker pointed at the test's bucket, staging in dir.
func blobWorkerCfg(bucket *testBucket, dir string) Config {
	return blobWorkerCfgMode(modeBlobs, bucket, dir)
}

// blobWorkerCfgMode is blobWorkerCfg over a named worker mode, so a test of a
// worker that dies mid-batch drives the same bucket handshake.
func blobWorkerCfgMode(mode string, bucket *testBucket, dir string) Config {
	cfg := fakeWorkerCfg(mode)
	cfg.R2 = &syncworker.R2Config{
		Endpoint: bucket.srv.URL,
		Bucket:   "bucket",
		KeyID:    "key-1",
		Secret:   "secret-1",
	}
	cfg.BlobStaging = dir
	return cfg
}

// seedBreakerDeaths records a count the settling of a batch can change or leave
// alone: Success resets Deaths, a refusal leaves it, and a miss counts a death.
func seedBreakerDeaths(t *testing.T, kv *memKV, st relevosync.BreakerState) {
	t.Helper()
	body, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal the breaker state: %v", err)
	}
	if err := kv.KVPut(relevosync.KeyDeaths, body); err != nil {
		t.Fatalf("seed the breaker state: %v", err)
	}
}

// TestSupervisorLatchesSyncOnABatchWithOneMissingKey pins the rule a batch
// settles under: the pipe call answered, so nothing about the transport says
// the machine is broken -- but a reply naming a body the bucket lost refused
// the call, so the breaker latches on it exactly as it latches on a lone fetch
// of that key, and the death is not counted as a transport miss.
func TestSupervisorLatchesSyncOnABatchWithOneMissingKey(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	present := []byte("a body the bucket holds")
	kv := newMemKV()
	b := breakerOver(kv)
	sup := newSupervisor(t, blobWorkerCfg(bucket, dir), b)

	if _, err := sup.PutBlobs(stagedBatch(t, dir, present)); err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	_, absent := stagedPath(t, []byte("a body nobody uploaded"))
	_, err := sup.GetBlobs([]synclog.BlobBatchItem{
		{Key: keyFor(present), Staging: filepath.Join(dir, "out-present"), Max: 64 << 20},
		{Key: absent, Staging: filepath.Join(dir, "out-absent"), Max: 64 << 20},
	})
	if !errors.Is(err, synclog.ErrBlobMissing) {
		t.Fatalf("GetBlobs = %v, want the missing key's sentinel", err)
	}
	if latched, _ := b.Latched(); !latched {
		t.Fatal("a batch with one missing key did not latch sync, want it settled as a refusal")
	}
	if cause, _ := b.LatchCause(); cause != "sync: a body the sync log names is missing from the bucket" {
		t.Errorf("latch cause = %q, want the missing-body cause", cause)
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths = %d, want the answered call latched instead of counted", got)
	}
}

// TestSupervisorLatchesOnThePermanentKeyOfASettledBatch pins which failure
// settles the call when several keys fail: the first permanent one, even when a
// failure a retry could clear stands before it in key order. A batch reported
// only by its first failed key would let a permanently lost body hide behind a
// bucket that 500s.
func TestSupervisorLatchesOnThePermanentKeyOfASettledBatch(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	body := []byte("a body the bucket holds but will not serve today")
	kv := newMemKV()
	b := breakerOver(kv)
	sup := newSupervisor(t, blobWorkerCfg(bucket, dir), b)

	if _, err := sup.PutBlobs(stagedBatch(t, dir, body)); err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	bucket.mu.Lock()
	bucket.FaultGets = map[string]bool{keyFor(body): true}
	bucket.mu.Unlock()
	_, absent := stagedPath(t, []byte("a body nobody uploaded"))

	results, err := sup.GetBlobs([]synclog.BlobBatchItem{
		{Key: keyFor(body), Staging: filepath.Join(dir, "faulted"), Max: 64 << 20},
		{Key: absent, Staging: filepath.Join(dir, "absent"), Max: 64 << 20},
	})
	if !errors.Is(err, synclog.ErrBlobMissing) {
		t.Fatalf("GetBlobs = %v, want the permanent failure of the batch, results %+v", err, results)
	}
	if results[0].Err == nil || errors.Is(results[0].Err, synclog.ErrBlobMissing) {
		t.Errorf("the faulted sibling's own verdict = %v, want its unmarked failure", results[0].Err)
	}
	if latched, _ := b.Latched(); !latched {
		t.Error("a batch whose later key named a permanently missing body did not latch sync")
	}
}

// TestSupervisorClasslessBatchFailureNeitherLatchesNorResets pins a batch a
// retry can still get past: the failure is a refusal, so the worker is kept and
// the call is not a death, but it is not permanent, so nothing latches -- and
// because a key did fail, the call does not settle as a success that would
// start the death count over.
func TestSupervisorClasslessBatchFailureNeitherLatchesNorResets(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	good := []byte("a body that publishes cleanly")
	faulted := []byte("a body the bucket refuses with a 500")
	kv := newMemKV()
	seedBreakerDeaths(t, kv, relevosync.BreakerState{Deaths: 1, Seen: 1})
	b := breakerOver(kv)
	sup := newSupervisor(t, blobWorkerCfg(bucket, dir), b)
	bucket.mu.Lock()
	bucket.FaultPuts = map[string]bool{keyFor(faulted): true}
	bucket.mu.Unlock()

	results, err := sup.PutBlobs(stagedBatch(t, dir, good, faulted))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("PutBlobs = %v, want the refused classless key", err)
	}
	if relevosync.IsPermanentRefusal(err) {
		t.Errorf("PutBlobs = %v, want a failure a later attempt can get past", err)
	}
	if results[0].Err != nil || results[0].Bytes != int64(len(good)) {
		t.Errorf("the clean sibling = %+v, want its body published", results[0])
	}
	if results[1].Err == nil || relevosync.IsPermanentRefusal(results[1].Err) {
		t.Errorf("the faulted key = %v, want its own unmarked failure", results[1].Err)
	}
	if latched, _ := b.Latched(); latched {
		t.Error("a failure a retry can clear latched the machine")
	}
	if got, _ := b.Deaths(); got != 1 {
		t.Errorf("deaths = %d, want a failed batch to leave the count alone: it is no success", got)
	}
	if s := sup.current(); s == nil {
		t.Error("the answered worker was dropped for a refusal, want it kept")
	}
	if _, ok := bucket.body(keyFor(good)); !ok {
		t.Error("a batch stopped publishing at its failed key, want the clean sibling stored")
	}
}

// TestSupervisorCleanBatchStartsTheDeathCountOver pins the other half of the
// settle rule: a batch with no failed key is the call that succeeds, and
// Success is what resets the consecutive deaths the backoff is measured from.
func TestSupervisorCleanBatchStartsTheDeathCountOver(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	kv := newMemKV()
	seedBreakerDeaths(t, kv, relevosync.BreakerState{Deaths: 1, Seen: 1})
	b := breakerOver(kv)
	sup := newSupervisor(t, blobWorkerCfg(bucket, dir), b)

	if _, err := sup.PutBlobs(stagedBatch(t, dir,
		[]byte("a body that publishes"), []byte("a second body"))); err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths = %d after a clean batch, want the count started over", got)
	}
}

// TestSupervisorReportsALatchWriteFailureFromABatch pins that a latch the
// breaker cannot write reaches the caller beside the refusal it was meant to
// record: the machine must not be told a permanent loss was settled when the
// record of it failed. The per-key verdicts still ride back with it, because
// they are what the mover's caller reads.
func TestSupervisorReportsALatchWriteFailureFromABatch(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	present := []byte("a body the bucket holds")
	kv := &failKV{inner: newMemKV(), failPutKey: relevosync.KeyAttention}
	sup := newSupervisor(t, blobWorkerCfg(bucket, dir), breakerOver(kv))

	if _, err := sup.PutBlobs(stagedBatch(t, dir, present)); err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	_, absent := stagedPath(t, []byte("a body nobody uploaded"))
	results, err := sup.GetBlobs([]synclog.BlobBatchItem{
		{Key: keyFor(present), Staging: filepath.Join(dir, "out-present"), Max: 64 << 20},
		{Key: absent, Staging: filepath.Join(dir, "out-absent"), Max: 64 << 20},
	})
	if !errors.Is(err, synclog.ErrBlobMissing) {
		t.Fatalf("GetBlobs = %v, want the refusal beside the failed latch write", err)
	}
	if !errors.Is(err, errKV) {
		t.Errorf("GetBlobs = %v, want the latch write's own failure to reach the caller", err)
	}
	if len(results) != 2 {
		t.Errorf("results = %d, want the verdicts held out to the caller with the error", len(results))
	}
}

// TestSupervisorBatchOnADeadWorkerKeepsNoPartialResults pins the whole-call
// path of a batch: a worker killed mid-batch is a miss, not a reply, so the
// caller gets no verdicts to half-act on, and the death is counted. Retrying
// the same batch through a fresh worker loses and duplicates nothing, because
// the mover re-sends the whole chunk and puts are idempotent by digest.
func TestSupervisorBatchOnADeadWorkerKeepsNoPartialResults(t *testing.T) {
	bucket := newTestBucket(t)
	dir := t.TempDir()
	bodies := [][]byte{
		[]byte("a body the dying worker was asked to publish"),
		[]byte("a second body on the same call"),
	}
	kv := newMemKV()
	b := breakerOver(kv)
	cfg := blobWorkerCfgMode(modeDie, bucket, dir)
	sup := newSupervisor(t, cfg, b)

	results, err := sup.PutBlobs(stagedBatch(t, dir, bodies...))
	if err == nil {
		t.Fatal("a batch over a worker that died = nil, want the miss")
	}
	if errors.Is(err, ErrRefused) {
		t.Errorf("the dead worker's call = %v, want a miss rather than a refusal", err)
	}
	if results != nil {
		t.Errorf("results = %+v, want no verdicts from a call that never answered", results)
	}
	if got, _ := b.Deaths(); got != 1 {
		t.Errorf("deaths = %d, want the death counted through Observe", got)
	}
	if sup.current() != nil {
		t.Error("the dead worker was kept for the next call")
	}

	// The retry: the same batch through a worker that lives, on the same
	// bucket. Everything the first call was asked arrives, and none of it
	// arrives twice -- the second send of the same bodies skips.
	retry := newSupervisor(t, blobWorkerCfg(bucket, dir), breakerOver(newMemKV()))
	items := stagedBatch(t, dir, bodies...)
	if _, err := retry.PutBlobs(items); err != nil {
		t.Fatalf("the retried batch: %v", err)
	}
	if n := bucket.count(); n != len(bodies) {
		t.Errorf("the bucket holds %d bodies after the retry, want the %d the batch named", n, len(bodies))
	}
	results, err = retry.PutBlobs(items)
	if err != nil {
		t.Fatalf("the third send of the same bodies: %v", err)
	}
	for n, r := range results {
		if !r.Skipped || r.Bytes != 0 {
			t.Errorf("verdict %d = %+v, want the retry to duplicate nothing", n, r)
		}
	}
}

// TestOpenSupervisorNamesTheStagingFolderItCreated pins that the folder the
// daemon hands the exporter is the one it made, beside the replica and already
// there. A supervisor naming a path it never created would have the exporter
// stage a body into a folder nothing created.
func TestOpenSupervisorNamesTheStagingFolderItCreated(t *testing.T) {
	t.Parallel()
	shared, local := wiringPair(t)
	seedRemoteAndToken(t, local)
	if err := relevosync.SetR2(local, wiringR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	sup, err := OpenSupervisor(shared, local)
	if err != nil {
		t.Fatalf("OpenSupervisor: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close() })

	dir := sup.BlobStagingDir()
	if want := BlobStagingDir(shared.Path()); dir != want {
		t.Fatalf("BlobStagingDir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the staging folder the supervisor names is not there: %v", err)
	}
}

// digestHex is the hex sha256 of a value, which is the name its object is stored
// under.
func digestHex(stored []byte) string {
	sum := sha256.Sum256(stored)
	return hex.EncodeToString(sum[:])
}

// seedRemoteAndToken gives a machine the two rows every sync install has before
// the bucket is configured, so a test about R2 is not refused for something else.
func seedRemoteAndToken(t *testing.T, local relevosync.Local) {
	t.Helper()
	now := time.Unix(0, 0).UTC()
	if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: "libsql://x.invalid"}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := relevosync.SetToken(local, []byte("FIXTURE-TOKEN-0123456789abcdef0123456789"), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
}

// seedPartialR2 writes whatever the struct carries, field by field.
func seedPartialR2(t *testing.T, local relevosync.Local, r relevosync.R2Secrets) {
	t.Helper()
	now := time.Unix(0, 0).UTC()
	for _, s := range []struct{ name, value string }{
		{relevosync.SecretR2Endpoint, r.Endpoint},
		{relevosync.SecretR2Bucket, r.Bucket},
		{relevosync.SecretR2KeyID, r.KeyID},
	} {
		if s.value == "" {
			continue
		}
		if err := setSecret(local, s.name, s.value, now); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}
}

// setSecret writes one machine-local secret.
func setSecret(local relevosync.Local, name, value string, now time.Time) error {
	return local.Tx(func(tx *db.Tx) error { return tx.SecretPut(name, []byte(value), now.UTC()) })
}

// modeOf is the permission bits of a path, for the staging folder's check.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// relevosyncSyncworkerKey is blobstore.Key, spelled through the package the
// client already depends on rather than importing blobstore here: the key format
// is what the wire carries, and duplicating the builder would let the two drift.
func relevosyncSyncworkerKey(origin, digest string) string {
	return blobstore.Key(origin, digest)
}
