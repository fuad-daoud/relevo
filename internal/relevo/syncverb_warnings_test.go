package relevo

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The join's import can stall an origin or drop rows and still let the enable
// succeed, so both have to reach whoever ran the enable rather than end with it.
func TestImportWarningsNameEveryStallAndDrop(t *testing.T) {
	got := importWarnings(synclog.ImportResult{
		Stalled: []synclog.Stall{{Origin: "m1", Label: "laptop", Reason: "a body it refers to could not be fetched"}},
		Dropped: []synclog.Drop{{Origin: "m1", Seq: 36207, Reason: `the references of binding ["01C"] refuse every remaining row`}},
	})
	if len(got) != 2 {
		t.Fatalf("importWarnings = %q, want one line per stall and drop", got)
	}
	if !strings.Contains(got[0], "laptop is held") || !strings.Contains(got[1], `binding ["01C"]`) {
		t.Errorf("importWarnings = %q, want the stalled origin and the dropped row named", got)
	}
	if none := importWarnings(synclog.ImportResult{Applied: 5}); len(none) != 0 {
		t.Errorf("importWarnings on a clean import = %q, want none", none)
	}
}
