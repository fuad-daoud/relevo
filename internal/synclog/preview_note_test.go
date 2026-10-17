package synclog

import "testing"

// TestPreviewTableNoteCountsPastTheKeyLimit pins the preview's bound: every
// entry is counted, and only the first keyLimit keys are named.
func TestPreviewTableNoteCountsPastTheKeyLimit(t *testing.T) {
	var tbl PreviewTable
	tbl.note(Entry{Op: OpUpsert, PK: `["a"]`}, 2)
	tbl.note(Entry{Op: OpDelete, PK: `["b"]`}, 2)
	tbl.note(Entry{Op: OpDelete, PK: `["c"]`}, 2)
	tbl.note(Entry{Op: OpUpsert, PK: `["d"]`}, 2)
	if tbl.Appends != 2 || tbl.Deletes != 2 {
		t.Fatalf("counts = %d appends, %d deletes, want 2 and 2", tbl.Appends, tbl.Deletes)
	}
	if len(tbl.AppendKeys) != 1 || len(tbl.DeleteKeys) != 1 {
		t.Errorf("keys = %v and %v, want one of each: the limit is on the pair", tbl.AppendKeys, tbl.DeleteKeys)
	}
}

func TestBatchDoneRunsTheHookOnlyWhenSet(t *testing.T) {
	(&Importer{}).batchDone()
	calls := 0
	(&Importer{AfterBatch: func() { calls++ }}).batchDone()
	if calls != 1 {
		t.Errorf("AfterBatch ran %d times, want once", calls)
	}
}
