package synclog

import (
	"testing"
)

// A preview reports the differences a reconcile would append and writes
// nothing anywhere: no entry reaches the log, no body reaches the store.
func TestReconcileDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertRecord("01A", "m1"),
		insertSizedRoundFile("01A", "large", BlobRefThreshold+1),
		insertBinding("01B", "m1"),
	)
	log, mover := NewBlobTransport("m1", t.TempDir())

	got, err := reconciler(d, log).Preview(10)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	appends, deletes := 0, 0
	for _, tbl := range got.Tables {
		appends += tbl.Appends
		deletes += tbl.Deletes
	}
	if appends != 3 || deletes != 0 {
		t.Fatalf("preview = %+v, want three appends and no deletes", got)
	}
	if len(log.batches) != 0 || len(log.appended()) != 0 {
		t.Errorf("the preview appended %d batches, want none", len(log.batches))
	}
	if mover.Puts != 0 {
		t.Errorf("the preview uploaded %d bodies, want none", mover.Puts)
	}
	if heads, _ := log.Head("m1"); len(heads) != 0 {
		t.Errorf("the log holds %d head rows after a preview, want none", len(heads))
	}
}

// A file the log already carries previews as empty, so the report says the two
// are in step rather than listing tables with nothing in them.
func TestReconcilePreviewOfAConvergedFileIsEmpty(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"), insertBinding("01B", "m1"))
	log := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := reconciler(d, log).Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := reconciler(d, log).Preview(10)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !got.Empty() {
		t.Fatalf("preview of a converged file = %+v, want no tables", got)
	}
}

// The key lists are bounded and the counts are not.
func TestReconcileDryRunBoundsKeysNotCounts(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"), insertBinding("01B", "m1"), insertBinding("01C", "m1"),
	)
	got, err := reconciler(d, &recording{MemTransport: NewMemTransport("m1")}).Preview(2)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(got.Tables) != 1 || got.Tables[0].Appends != 3 || len(got.Tables[0].AppendKeys) != 2 {
		t.Fatalf("preview = %+v, want 3 appends with 2 keys kept", got)
	}
}
