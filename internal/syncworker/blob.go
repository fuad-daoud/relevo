package syncworker

// The two blob verbs. A body never crosses the pipe: the daemon writes it to a
// staging file beside the replica, names that file and the object's key, and
// the worker moves the bytes between the two. A pipe is one line per request and
// a JSON line cannot carry a few megabytes, so this is what makes a large
// value movable at all.
//
// The digest is checked before anything is published. A key names a body by its
// sha256, and a fetch on another machine checks what arrived against the same
// digest: if a wrong file were stored under a key, every machine that fetched it
// would disagree with the entry and the row could not be applied. Checking here
// is what turns that from a silent corruption into a refusal naming both the key
// and the file.

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

// stagingMode is the mode a staging file is created with. A body in this folder
// is a user's content that has not been stored anywhere else yet, and 0600 is
// what keeps another account on this machine from reading it out of a directory
// its own user does not own.
const stagingMode = 0o600

// newBlobStore builds the store hello configured. It is a var so a test can
// stand in for it: the real one signs every request and reaches a bucket, and no
// CI test may. Nothing else replaces it.
var newBlobStore = func(cfg R2Config) blobstore.BlobStore {
	return blobstore.NewS3Store(blobstore.S3Config{
		Endpoint: cfg.Endpoint,
		Bucket:   cfg.Bucket,
		KeyID:    cfg.KeyID,
		Secret:   cfg.Secret,
	})
}

// partSuffix is the name a fetched body is written under before it is renamed
// into place. The rename is what makes the file's arrival atomic: a reader
// either sees the whole body or no file, and a fetch interrupted half way
// leaves a name the next fetch overwrites rather than a truncated body that
// looks complete.
const partSuffix = ".part"

// putBlob uploads the staged file under key, unless the store already holds it.
//
// It asks Has before Put because two machines exporting the same body would
// otherwise each pay to send it, and because the answer is what the caller needs
// to know: a skip says the object another machine put is already the one this
// entry points at, so the entry may be appended either way.
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

// getBlob fetches the object under key into staging.
//
// The bytes land beside the target first and are renamed over it, so a fetch
// that fails or is interrupted never leaves a partial body where the importer
// will read one and compare its digest against a key it cannot match. A missing
// object is its own refusal class: it repeats, and the daemon latches the origin
// rather than retrying a bucket that has already answered.
func getBlob(ctx context.Context, store blobstore.BlobStore, key, staging string) (int64, error) {
	part := staging + partSuffix
	written, err := fetchTo(ctx, store, key, part)
	if err != nil {
		return 0, err
	}
	if err := os.Rename(part, staging); err != nil {
		// The rename is the last step and its own failure leaves nothing behind
		// to clean up by accident: the caller retries and the part is overwritten.
		return 0, fmt.Errorf("syncworker: move %s into place: %w", staging, err)
	}
	return written, nil
}

// fetchTo writes the object under key to path, fsyncing it before returning.
//
// The fsync is what makes the rename mean something: without it the rename can
// land in the directory while the bytes are still only in the page cache, and a
// crash would leave a correctly named file holding nothing.
func fetchTo(ctx context.Context, store blobstore.BlobStore, key, path string) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, stagingMode)
	if err != nil {
		return 0, fmt.Errorf("syncworker: open the staging file: %w", err)
	}
	written, getErr := store.Get(ctx, key, f)
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

// blobFetchError classifies a failed fetch. A 404 is marked with the class the
// daemon latches on; every other failure is a fault a later attempt can still
// get past, so it is left unmarked and the caller retries it.
func blobFetchError(key string, err error) error {
	if errors.Is(err, blobstore.ErrNotFound) {
		return MarkRefusal(CodeBlobMissing, fmt.Errorf("syncworker: %s is not in the store: %w", key, err))
	}
	return fmt.Errorf("syncworker: get %s: %w", key, err)
}

// digestFile reads a staged file and returns its length and its sha256. Both
// come from one pass: a body is a few megabytes at most, and hashing a second
// read would double the cost of the only check standing between a wrong file and
// a published object.
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

// checkDigest refuses a staged file that is not the body its key names.
//
// The digest in the key is the last path segment, because that is how
// blobstore.Key builds one. A key with no digest in it is refused rather than
// compared against nothing: it names no body, and publishing under it would
// leave an object no fetch could ever verify.
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

// putBlobVerb answers put_blob. It carries no body: the daemon wrote the file
// and named it, and the only thing this call decides is whether the object
// belongs in the store and how many bytes that was.
//
// The counters are kept on the server rather than on the backend because they
// count work the verbs themselves do. The backend's own byte totals come from
// the replica, and a Backend that did not move bodies should not have to carry
// fields for them.
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
	got, err := getBlob(context.Background(), store, req.Key, req.Staging)
	if err != nil {
		return refuse(req, err)
	}
	s.r2.R2Get += got
	return Response{ID: req.ID, OK: true, Bytes: got}
}

// blobStore returns the store hello configured, or a refusal when there is none.
//
// The store is built once and kept: a client is built per pipe and every
// request would otherwise construct an http client for one call. A machine that
// sends a blob verb before the handshake is refused by the dispatch table, so the
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
