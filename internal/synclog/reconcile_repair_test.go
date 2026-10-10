package synclog

import (
	"context"
	"strconv"
	"testing"
)

// presentKeys is the bucket's listing of one origin, as the join reads it.
func presentKeys(t *testing.T, mover *MemBlobMover, origin string) map[string]bool {
	t.Helper()
	objects, err := mover.Store().List(context.Background(), origin+"/")
	if err != nil {
		t.Fatalf("list the bucket: %v", err)
	}
	present := make(map[string]bool, len(objects))
	for _, o := range objects {
		present[o.Key] = true
	}
	return present
}

// dropObjects deletes every object the bucket holds, as a lost bucket would.
func dropObjects(t *testing.T, mover *MemBlobMover) int {
	t.Helper()
	objects, err := mover.Store().List(context.Background(), "")
	if err != nil {
		t.Fatalf("list the bucket: %v", err)
	}
	for _, o := range objects {
		if err := mover.Store().Delete(context.Background(), o.Key); err != nil {
			t.Fatalf("delete %s: %v", o.Key, err)
		}
	}
	return len(objects)
}

// A row the log already carries still names its object. When the bucket lost
// that object, a reconcile told what the bucket holds uploads it again and
// appends nothing, so a reader joining from zero can fetch it.
func TestReconcileRepairsABodyTheBucketLost(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertRecord("01A", "m1"), insertSizedRoundFile("01A", "body", 50<<10))
	log, mover := NewBlobTransport("m1", t.TempDir())
	if _, err := blobReconciler(d, log).Reconcile(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if lost := dropObjects(t, mover); lost != 1 {
		t.Fatalf("the bucket held %d objects, want 1", lost)
	}
	before, _ := log.Stats()

	r := blobReconciler(d, log)
	r.RepairBodies(presentKeys(t, mover, "m1"))
	res, err := r.Reconcile()
	if err != nil {
		t.Fatalf("repairing reconcile: %v", err)
	}
	if res.Repaired != 1 || res.Batches != 0 {
		t.Fatalf("reconcile = %+v, want one body repaired and nothing appended", res)
	}
	key := "m1/" + digestOf(make([]byte, 50<<10))
	if ok, _ := mover.Store().Has(context.Background(), key); !ok {
		t.Fatalf("the bucket lacks %s after the repair", key)
	}
	if after, _ := log.Stats(); after.Entries != before.Entries {
		t.Errorf("the log grew from %d to %d entries, want a repair to append nothing", before.Entries, after.Entries)
	}
}

// Without the bucket's listing a reconcile does not look for lost bodies: the
// steady path never lists the bucket, so it never uploads a body twice.
func TestReconcileWithoutAListingRepairsNothing(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertRecord("01A", "m1"), insertSizedRoundFile("01A", "body", 50<<10))
	log, mover := NewBlobTransport("m1", t.TempDir())
	if _, err := blobReconciler(d, log).Reconcile(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	dropObjects(t, mover)

	res, err := blobReconciler(d, log).Reconcile()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Repaired != 0 {
		t.Fatalf("reconcile = %+v, want nothing repaired without a listing", res)
	}
}

// Repairs fill chunks the way proposed rows do. A chunk that ends on the body
// budget with no difference in it is not the end of the walk: the run goes on
// and repairs every lost body.
func TestReconcileRepairsAcrossChunks(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	stmts := []string{insertRecord("01A", "m1")}
	const files = 5
	for i := range files {
		stmts = append(stmts, insertSizedRoundFile("01A", "f"+strconv.Itoa(i), 50<<10+i))
	}
	seed(t, path, stmts...)
	log, mover := NewBlobTransport("m1", t.TempDir())
	if _, err := blobReconciler(d, log).Reconcile(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	dropObjects(t, mover)

	r := blobReconciler(d, log)
	r.blobBudget = 60 << 10
	r.RepairBodies(presentKeys(t, mover, "m1"))
	res, err := r.Reconcile()
	if err != nil {
		t.Fatalf("repairing reconcile: %v", err)
	}
	if res.Repaired != files {
		t.Fatalf("reconcile = %+v, want all %d bodies repaired", res, files)
	}
	if left := presentKeys(t, mover, "m1"); len(left) != files {
		t.Errorf("the bucket holds %d of the %d bodies", len(left), files)
	}
}
