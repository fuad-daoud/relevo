package synclog

import "fmt"

// Preview is what a reconcile would append, grouped by table, in the order the
// reconcile would walk.
type Preview struct {
	Tables []PreviewTable
}

// PreviewTable is one table's share of a preview. The counts are complete; the
// key lists together stop at the limit the preview was asked for, so a table of a
// million rows costs a bounded report.
type PreviewTable struct {
	Table      string
	Appends    int
	Deletes    int
	AppendKeys []string
	DeleteKeys []string
}

// Empty reports whether the file and the log already agree.
func (p Preview) Empty() bool { return len(p.Tables) == 0 }

// Preview walks the same differences Reconcile would append, in the same
// order, and reports them without appending anything: no body is uploaded and
// no entry reaches the transport. keyLimit bounds the keys kept per table,
// appends first; the counts are never bounded.
func (r *Reconciler) Preview(keyLimit int) (Preview, error) {
	r.reset()
	var out Preview
	index := map[string]int{}
	for {
		entries, err := r.differences(r.chunk)
		if err != nil {
			return Preview{}, fmt.Errorf("synclog: reconcile preview: %w", err)
		}
		// The chunk's bodies are never uploaded, so what it queued is dropped.
		r.pending = nil
		if len(entries) == 0 {
			return out, nil
		}
		for _, e := range entries {
			i, ok := index[e.Table]
			if !ok {
				i = len(out.Tables)
				index[e.Table] = i
				out.Tables = append(out.Tables, PreviewTable{Table: e.Table})
			}
			out.Tables[i].note(e, keyLimit)
		}
	}
}

func (t *PreviewTable) note(e Entry, keyLimit int) {
	room := len(t.AppendKeys)+len(t.DeleteKeys) < keyLimit
	if e.Op == OpDelete {
		t.Deletes++
		if room {
			t.DeleteKeys = append(t.DeleteKeys, e.PK)
		}
		return
	}
	t.Appends++
	if room {
		t.AppendKeys = append(t.AppendKeys, e.PK)
	}
}
