package synclog

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The snapshot region and the join's secrets pre-pass both verify through
// undercut, so a two-epoch log is enough to pin that they inherit the rule
// rather than a second path around it.

// TestSnapshotRegionUnderTwoEpochs pins the snapshot path on a rekeyed log:
// the surviving region sits below the rekey's cut, so its epoch-1 entries
// verify by trial under the epoch-1 key -- and an entry above the cut is held
// like any other import.
func TestSnapshotRegionUnderTwoEpochs(t *testing.T) {
	t.Parallel()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	signedExport(t, m1, log, keyA) // the entry that survives compaction
	seed(t, path, `UPDATE binding SET cwd = '/y' WHERE id = '01A'`)
	signedExport(t, m1, log, keyA)
	compactTo(t, log, "m1", 2, 1) // the region now starts at seq 1
	rekeyTo2(t, log, keyA, keyB)  // the cut lands at the survivor
	m2, _ := peerFile(t, "m2")

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Snapshots != 1 {
		t.Fatalf("import = %+v, want the one snapshot", res)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]` {
		t.Errorf("m2 holds %q, want the survivor verified under its old key", got)
	}
	if cuts, _ := m2.EpochCuts(); cuts["m1"] != (db.EpochCut{Epoch: 2, Seq: 2}) {
		t.Errorf("records = %v, want m1's cut verified and raised at the log's own head", cuts)
	}
}

// TestPrepassAppliesUnderTwoEpochs pins the pre-pass over a two-epoch log:
// the walk verifies each entry under its epoch's key, so a document an old
// epoch wrote lands on the joiner exactly as it did before the rekey.
func TestPrepassAppliesUnderTwoEpochs(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	rekeyTo2(t, log, keyA, keyB)
	m2, _ := peerFile(t, "m2")

	imp := NewImporter(m2, log.OnLog("m2"))
	imp.SetKeyring(ring2())
	imp.UseDocs(doc)
	n, err := imp.PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 1 {
		t.Fatalf("applied %d, want the one epoch-1 entry", n)
	}
	if got := string(doc.items["policy"]); got != `{"a":1}` {
		t.Errorf("policy = %s, want the document applied", got)
	}
}
