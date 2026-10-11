package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// blobLog is a log that also moves bodies, over one in-memory bucket the join's
// own listing reads too.
type blobLog struct {
	*synclog.MemTransport
	*synclog.MemBlobMover
	staging string
}

func (b blobLog) BlobStagingDir() string { return b.staging }

// A join lists this machine's bodies in the bucket and has reconcile upload the
// ones a row the log already carries names but the bucket lost. Without it a
// reader joining from zero would latch on the missing object forever.
func TestJoinReuploadsABodyTheBucketLost(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinRecord("01A", "m1"),
		`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		VALUES ('01A', 'big', 1, zeroblob(51200), 51200, 's', 't', 't')`)
	log := blobLog{MemTransport: synclog.NewMemTransport("m1"), MemBlobMover: synclog.NewMemBlobMover(), staging: t.TempDir()}
	store := log.Store()
	e := &Enabler{Request: EnableRequest{Local: local, Shared: shared}}
	if _, _, err := e.reconcile(log); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	objects, _ := store.List(context.Background(), "")
	if len(objects) != 1 {
		t.Fatalf("the bucket holds %d objects after the first reconcile, want 1", len(objects))
	}
	if err := store.Delete(context.Background(), objects[0].Key); err != nil {
		t.Fatalf("delete: %v", err)
	}

	e.Blobs = func(R2Secrets) blobstore.BlobStore { return store }
	present, err := e.ownBodies(EnablePlan{})
	if err != nil {
		t.Fatalf("ownBodies: %v", err)
	}
	e.present = present
	if _, _, err := e.reconcile(log); err != nil {
		t.Fatalf("repairing reconcile: %v", err)
	}
	if ok, _ := store.Has(context.Background(), objects[0].Key); !ok {
		t.Fatalf("the join left %s missing from the bucket", objects[0].Key)
	}
}

// The listing is of this machine's bodies only: another origin's bodies and the
// published log sizes are not this join's to repair.
func TestOwnBodiesListsOnlyThisOriginsBodies(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	store := guardBucket(t, 10, map[string]int64{"m1": 5})
	mine := blobstore.Key("m1", strings.Repeat("cd", 32))
	if err := store.Put(context.Background(), mine, strings.NewReader("x"), 1); err != nil {
		t.Fatalf("put: %v", err)
	}
	e := &Enabler{Request: EnableRequest{Local: local, Shared: shared},
		Blobs: func(R2Secrets) blobstore.BlobStore { return store }}
	present, err := e.ownBodies(EnablePlan{})
	if err != nil {
		t.Fatalf("ownBodies: %v", err)
	}
	if len(present) != 1 || !present[mine] {
		t.Errorf("ownBodies = %v, want only %s", present, mine)
	}
}
