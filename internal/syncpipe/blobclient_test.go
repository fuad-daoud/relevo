package syncpipe

// The client's blob half, over the same real pipe every other client test uses:
// this binary respawned as a worker, one call at a time. The bucket is an
// httptest server, so the worker's real S3 store runs against it -- what is
// pinned is the whole path the daemon takes, not a stub of it.

import (
	"crypto/sha256"
	"encoding/hex"
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
// against a real store: a staged body is stored under its digest, a second put of
// it is a skip with no bytes, and a fetch writes the whole body back out.
func TestPipeClientMovesBlobsOverThePipe(t *testing.T) {
	bucket := newTestBucket(t)
	body := []byte("a body this machine is about to publish")
	path, key := stagedPath(t, body)

	c, err := startFakeWorker(t, modeBlobs, blobHandshake(bucket))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	sent, skipped, err := c.PutBlob(key, path)
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if sent != int64(len(body)) || skipped {
		t.Errorf("PutBlob = %d/%v, want %d bytes and no skip", sent, skipped, len(body))
	}
	if stored, ok := bucket.body(key); !ok || string(stored) != string(body) {
		t.Errorf("the bucket holds %q, want the staged body", stored)
	}

	// The second put is the case worth pinning: the object is already there, so
	// it must move no bytes and answer as a skip. A caller that uploaded the same
	// body twice would otherwise bill the month for the second copy.
	sent, skipped, err = c.PutBlob(key, path)
	if err != nil {
		t.Fatalf("the second PutBlob: %v", err)
	}
	if !skipped || sent != 0 {
		t.Errorf("the second PutBlob = %d/%v, want a skip moving nothing", sent, skipped)
	}

	fetched := filepath.Join(filepath.Dir(path), "fetched")
	got, err := c.GetBlob(key, fetched)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if got != int64(len(body)) {
		t.Errorf("GetBlob = %d, want %d", got, len(body))
	}
	reread, err := os.ReadFile(fetched)
	if err != nil {
		t.Fatalf("read the fetched body: %v", err)
	}
	if string(reread) != string(body) {
		t.Errorf("the fetched body is %q, want %q", reread, body)
	}
}

// TestPipeClientMapsAMissingBodyToTheSentinel pins that a 404 arrives as
// synclog.ErrBlobMissing rather than an ordinary refusal. The importer latches
// the origin on that sentinel; an unclassified refusal would be retried, and
// retrying will not put the object there.
func TestPipeClientMapsAMissingBodyToTheSentinel(t *testing.T) {
	bucket := newTestBucket(t)
	_, absent := stagedPath(t, []byte("a body that was never uploaded"))

	c, err := startFakeWorker(t, modeBlobs, blobHandshake(bucket))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.GetBlob(absent, filepath.Join(t.TempDir(), "out")); !errors.Is(err, synclog.ErrBlobMissing) {
		t.Errorf("GetBlob = %v, want synclog.ErrBlobMissing", err)
	}
	// It stays a refusal as well: a caller that reads only the class still learns
	// the worker declined rather than that the pipe broke.
	if _, err := c.GetBlob(absent, filepath.Join(t.TempDir(), "out")); !errors.Is(err, ErrRefused) {
		t.Error("the mapped error is not a refusal as well")
	}
}

// TestPipeClientCarriesTheByteCounters pins that all four totals reach the
// daemon. A reply that lost them would leave the month reading as a cost of
// nothing, which is the failure the counting exists to prevent.
func TestPipeClientCarriesTheByteCounters(t *testing.T) {
	bucket := newTestBucket(t)
	c, err := startFakeWorker(t, modeBlobs, blobHandshake(bucket))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	body := []byte("a body counted on its way out")
	path, key := stagedPath(t, body)
	if _, _, err := c.PutBlob(key, path); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if _, err := c.GetBlob(key, filepath.Join(filepath.Dir(path), "back")); err != nil {
		t.Fatalf("GetBlob: %v", err)
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
	want := int64(len(body))
	if stats.R2Put != want || stats.R2Get != want {
		t.Errorf("the R2 totals are put=%d get=%d, want %d each", stats.R2Put, stats.R2Get, want)
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
