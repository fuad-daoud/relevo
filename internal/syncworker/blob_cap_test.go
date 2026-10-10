package syncworker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// A body longer than its ref declares is cut off at that length rather than
// downloaded whole, and neither the staging file nor its part is left behind.
func TestGetBlobStopsAtTheDeclaredLength(t *testing.T) {
	t.Parallel()
	store := blobstore.NewMemBlobStore(nil)
	key := blobstore.Key("origin-a", hexSHA("x"))
	body := bytes.Repeat([]byte("a"), 1<<20)
	if err := store.Put(context.Background(), key, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put: %v", err)
	}
	staging := filepath.Join(t.TempDir(), "body")
	if _, err := getBlob(context.Background(), store, key, staging, 10); !errors.Is(err, errPastMax) {
		t.Fatalf("get = %v, want errPastMax", err)
	}
	for _, p := range []string{staging, staging + partSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind (stat err %v)", p, err)
		}
	}
	if _, err := getBlob(context.Background(), store, key, staging, 0); err == nil {
		t.Error("a get naming no limit was answered")
	}
}
