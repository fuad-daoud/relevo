package sync

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// PreviewKeyLimit is how many keys a dry-run report lists per table and per
// direction before it says how many more there are.
const PreviewKeyLimit = 10

// FormatPreview renders a reconcile preview for a terminal. It is a pure
// function of the preview so the output is pinned by bytes, whichever side of
// the owner socket renders it.
func FormatPreview(p synclog.Preview) string {
	if p.Empty() {
		return "in step with the log; nothing to append\n"
	}
	var b strings.Builder
	for _, t := range p.Tables {
		fmt.Fprintf(&b, "%s: %d to append, %d to delete\n", t.Table, t.Appends, t.Deletes)
		for _, k := range t.AppendKeys {
			fmt.Fprintf(&b, "  + %s\n", k)
		}
		for _, k := range t.DeleteKeys {
			fmt.Fprintf(&b, "  - %s\n", k)
		}
		if more := t.Appends + t.Deletes - len(t.AppendKeys) - len(t.DeleteKeys); more > 0 {
			fmt.Fprintf(&b, "  … and %d more\n", more)
		}
	}
	b.WriteString("dry run: nothing was appended\n")
	return b.String()
}

// FormatReconcile renders what a reconcile appended for a terminal.
func FormatReconcile(r synclog.ReconcileResult) string {
	if r.Batches == 0 {
		return "in step with the log; nothing to append\n"
	}
	return fmt.Sprintf("appended %d upserts and %d deletes in %d batches\n", r.Upserts, r.Deletes, r.Batches)
}
