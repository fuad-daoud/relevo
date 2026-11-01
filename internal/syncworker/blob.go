package syncworker

// The two blob verbs. A body never crosses the pipe: the daemon stages it in a
// file beside the replica and names that file and the object's key, because a
// JSON line cannot carry a few megabytes.
//
// The digest is checked before anything is published: a key names a body by its
// sha256, and a wrong file stored under a key would disagree with the entry on
// every machine that fetched it. Checking here turns that silent corruption
// into a refusal naming both the key and the file.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// ErrNoR2 reports a blob verb on a pipe whose hello named no bucket. It is a
// refusal rather than a failure: the pipe is in step, the machine simply has
// nowhere to put bodies yet, and the message says which setting is missing.
var ErrNoR2 = errors.New("syncworker: no R2 settings; hello named no bucket")

// stagingMode is 0600: a body staged here is a user's content stored nowhere
// else yet, and 0600 keeps another account on this machine from reading it.
const stagingMode = 0o600

// newBlobStore builds the store hello configured. It is a var so a test can
// stand in for it: the real one signs every request and reaches a bucket, and
// no CI test may.
var newBlobStore = func(cfg R2Config) blobstore.BlobStore {
	return blobstore.NewS3Store(blobstore.S3Config{
		Endpoint: cfg.Endpoint,
		Bucket:   cfg.Bucket,
		KeyID:    cfg.KeyID,
		Secret:   cfg.Secret,
	})
}

// partSuffix is the name a fetched body is written under before it is renamed
// into place. The rename makes the arrival atomic: a reader sees the whole
// body or no file, never a truncated body that looks complete.
const partSuffix = ".part"

// putBlob uploads the staged file under key, unless the store already holds
// it. Has runs before Put because two machines exporting the same body would
// otherwise each pay to send it; a skip says the object is already the one
// this entry points at, so the entry may be appended either way.
func putBlob(ctx context.Context, store blobstore.BlobStore, key, staging string) (int64, bool, error) {
	exists, err := store.Has(ctx, key)
	if err != nil {
		return 0, false, fmt.Errorf("syncworker: check %s: %w", key, err)
	}
	if exists {
		return 0, true, nil
	}
	size, sum, err := digestFile(staging)
	if err != nil {
		return 0, false, err
	}
	if err := checkDigest(key, sum); err != nil {
		return 0, false, err
	}
	body, err := os.Open(staging)
	if err != nil {
		return 0, false, fmt.Errorf("syncworker: open the staging file: %w", err)
	}
	defer func() { _ = body.Close() }()
	if err := store.Put(ctx, key, body, size); err != nil {
		return 0, false, fmt.Errorf("syncworker: put %s: %w", key, err)
	}
	return size, false, nil
}

// getBlob fetches the object under key into staging. The bytes land beside the
// target first and are renamed over it, so a failed fetch never leaves a
// partial body the importer would compare against a key it cannot match. A
// missing object is its own refusal class: it repeats, so the daemon latches
// the origin rather than retrying a bucket that has already answered.
func getBlob(ctx context.Context, store blobstore.BlobStore, key, staging string, max int64) (int64, error) {
	if max <= 0 {
		return 0, fmt.Errorf("syncworker: get %s names no size limit", key)
	}
	part := staging + partSuffix
	written, err := fetchTo(ctx, store, key, part, max)
	if err != nil {
		_ = os.Remove(part)
		return 0, err
	}
	if err := os.Rename(part, staging); err != nil {
		// The rename is the last step and its own failure leaves nothing behind
		// to clean up by accident: the caller retries and the part is overwritten.
		return 0, fmt.Errorf("syncworker: move %s into place: %w", staging, err)
	}
	return written, nil
}

// fetchTo writes the object under key to path, fsyncing it before returning:
// without the fsync the rename can land while the bytes are still only in the
// page cache, and a crash would leave a correctly named file holding nothing.
func fetchTo(ctx context.Context, store blobstore.BlobStore, key, path string, max int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, stagingMode)
	if err != nil {
		return 0, fmt.Errorf("syncworker: open the staging file: %w", err)
	}
	written, getErr := store.Get(ctx, key, &capWriter{w: f, left: max})
	if getErr != nil {
		_ = f.Close()
		return 0, blobFetchError(key, getErr)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return written, fmt.Errorf("syncworker: flush the staging file: %w", err)
	}
	if err := f.Close(); err != nil {
		return written, fmt.Errorf("syncworker: close the staging file: %w", err)
	}
	return written, nil
}

// blobFetchError classifies a failed fetch: a 404 is marked with the class the
// daemon latches on, and every other failure is left unmarked for a retry.
func blobFetchError(key string, err error) error {
	if errors.Is(err, blobstore.ErrNotFound) {
		return MarkRefusal(CodeBlobMissing, fmt.Errorf("syncworker: %s is not in the store: %w", key, err))
	}
	return fmt.Errorf("syncworker: get %s: %w", key, err)
}

// digestFile reads a staged file and returns its length and its sha256 in one
// pass: hashing a second read would double the cost of the check standing
// between a wrong file and a published object.
func digestFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("syncworker: open the staging file: %w", err)
	}
	defer func() { _ = f.Close() }()
	digest := sha256.New()
	size, err := io.Copy(digest, f)
	if err != nil {
		return 0, "", fmt.Errorf("syncworker: read the staging file: %w", err)
	}
	return size, hex.EncodeToString(digest.Sum(nil)), nil
}

// checkDigest refuses a staged file that is not the body its key names. The
// digest in the key is its last path segment, because that is how blobstore.Key
// builds one; a key with no digest names no body, and publishing under it
// would leave an object no fetch could verify.
func checkDigest(key, sum string) error {
	want := filepath.Base(key)
	if want == "" || want == "." || want == string(filepath.Separator) {
		return MarkRefusal(CodeBlobRefused,
			fmt.Errorf("syncworker: key %q names no digest", key))
	}
	if want != sum {
		return MarkRefusal(CodeBlobRefused, fmt.Errorf(
			"syncworker: the staged body is %s, but %s is keyed as %s", sum, filepath.Base(key), want))
	}
	return nil
}

// putBlobVerb answers put_blob: the daemon wrote the file and named it, so
// this call decides only whether the object belongs in the store and how many
// bytes that was. The counters live on the server because they count work the
// verbs do, not bytes the replica moved.
func (s *server) putBlobVerb(req Request) Response {
	store, err := s.blobStore(req)
	if err != nil {
		return refuse(req, err)
	}
	sent, skipped, err := putBlob(context.Background(), store, req.Key, req.Staging)
	if err != nil {
		return refuse(req, err)
	}
	s.r2.R2Put += sent
	return Response{ID: req.ID, OK: true, Bytes: sent, Skipped: skipped}
}

// getBlobVerb answers get_blob, writing the body to the staging file it names.
func (s *server) getBlobVerb(req Request) Response {
	store, err := s.blobStore(req)
	if err != nil {
		return refuse(req, err)
	}
	got, err := getBlob(context.Background(), store, req.Key, req.Staging, req.Max)
	if err != nil {
		return refuse(req, err)
	}
	s.r2.R2Get += got
	return Response{ID: req.ID, OK: true, Bytes: got}
}

// blobStore returns the store hello configured, or a refusal when there is
// none. The store is built once and kept, because a client is built per pipe.
// A blob verb before the handshake is refused by the dispatch table, so the
// only case reaching here is a hello that named no bucket.
func (s *server) blobStore(req Request) (blobstore.BlobStore, error) {
	if s.blobs != nil {
		return s.blobs, nil
	}
	if s.spec == nil || s.spec.R2 == nil {
		return nil, ErrNoR2
	}
	if req.Key == "" || req.Staging == "" {
		return nil, fmt.Errorf("%w: a blob verb needs a key and a staging path", ErrProtocol)
	}
	s.blobs = newBlobStore(*s.spec.R2)
	return s.blobs, nil
}

// errPastMax is a body longer than the ref it was fetched for declares.
var errPastMax = errors.New("syncworker: the body runs past the length its ref declares")

// capWriter passes writes through until left bytes are used, then fails the
// write that would pass it, ending the copy and so the download.
type capWriter struct {
	w    io.Writer
	left int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, errPastMax
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}
