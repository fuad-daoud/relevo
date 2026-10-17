package sync

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

func TestFormatPreviewListsTablesThenFooter(t *testing.T) {
	t.Parallel()
	got := FormatPreview(synclog.Preview{Tables: []synclog.PreviewTable{
		{Table: "binding", Appends: 1, Deletes: 1, AppendKeys: []string{`["a"]`}, DeleteKeys: []string{`["z"]`}},
		{Table: "round", Appends: 2, AppendKeys: []string{`["r1"]`, `["r2"]`}},
	}})
	want := "binding: 1 to append, 1 to delete\n  + [\"a\"]\n  - [\"z\"]\n" +
		"round: 2 to append, 0 to delete\n  + [\"r1\"]\n  + [\"r2\"]\n" +
		"dry run: nothing was appended\n"
	if got != want {
		t.Fatalf("FormatPreview =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPreviewSaysHowManyMoreKeys(t *testing.T) {
	t.Parallel()
	keys := make([]string, PreviewKeyLimit)
	for i := range keys {
		keys[i] = fmt.Sprintf(`["k%d"]`, i)
	}
	got := FormatPreview(synclog.Preview{Tables: []synclog.PreviewTable{
		{Table: "binding", Appends: PreviewKeyLimit + 5, AppendKeys: keys},
	}})
	if !strings.Contains(got, "  … and 5 more\n") {
		t.Fatalf("report does not say how many keys were left out:\n%s", got)
	}
	if n := strings.Count(got, "  + "); n != PreviewKeyLimit {
		t.Fatalf("report lists %d keys, want %d", n, PreviewKeyLimit)
	}
}

func TestReconcileDryRunOnAConvergedFile(t *testing.T) {
	t.Parallel()
	got := FormatPreview(synclog.Preview{})
	if got != "in step with the log; nothing to append\n" {
		t.Fatalf("FormatPreview(empty) = %q", got)
	}
}

func TestFormatReconcileCountsWhatWasAppended(t *testing.T) {
	t.Parallel()
	got := FormatReconcile(synclog.ReconcileResult{Batches: 3, Upserts: 2, Deletes: 612})
	if got != "appended 2 upserts and 612 deletes in 3 batches\n" {
		t.Fatalf("FormatReconcile = %q", got)
	}
	if got := FormatReconcile(synclog.ReconcileResult{}); got != "in step with the log; nothing to append\n" {
		t.Fatalf("FormatReconcile(nothing) = %q", got)
	}
}
