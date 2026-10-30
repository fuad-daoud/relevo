package relevo

import (
	"testing"
)

// TestReconcileHeadlessSkipsEscapeCheckWithoutAMarker pins the marker gate: an
// open round pays no escape git reads until its completion marker is on disk,
// and the close that consumes the answer still takes them.
func TestReconcileHeadlessSkipsEscapeCheckWithoutAMarker(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	// Make the escape check applicable: the source repo and the round's
	// baseline are what it compares.
	b.Repo = "/src/myrepo"
	b.RoundBaselineTree = "tree1"
	fg := &fakeGit{snapshotTreeID: "tree1", dirtyResult: true}
	rt.Git = fg

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if fg.snapshotCalls != 0 || fg.dirtyCalls != 0 {
		t.Errorf("a round with no marker paid %d snapshot and %d dirty git reads, want none",
			fg.snapshotCalls, fg.dirtyCalls)
	}

	// The marker makes the close real, and the close is the reader of the
	// escape answer: the snapshot and the dirty read are taken once.
	touch(t, rt.Store.DonePath(b.Name, b.Round))
	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile with the marker: %v", err)
	}
	if fg.snapshotCalls == 0 || fg.dirtyCalls == 0 {
		t.Errorf("the close paid %d snapshot and %d dirty git reads, want at least one of each",
			fg.snapshotCalls, fg.dirtyCalls)
	}
}
