package verb

import (
	"strings"
	"testing"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The unchecked-join sentence is owed only when the guard ran and no Turso
// figure was read: a known figure was checked, and an unguarded enable never
// ran the check at all.
func TestEnableWarningNamesTheUncheckedJoinOnlyWhenGuardedAndUnknown(t *testing.T) {
	const unchecked = "the join's Turso cost could not be read, so it was not checked against the Turso quota"
	cases := []struct {
		name string
		res  relevosync.EnableResult
		want string
	}{
		{"guarded and unknown", relevosync.EnableResult{Guarded: true}, unchecked},
		{"guarded and known", relevosync.EnableResult{Guarded: true, Estimate: relevosync.JoinEstimate{Known: true}}, ""},
		{"unguarded and unknown", relevosync.EnableResult{}, ""},
		{"unguarded and known", relevosync.EnableResult{Estimate: relevosync.JoinEstimate{Known: true}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := enableWarning(tc.res); got != tc.want {
				t.Errorf("enableWarning = %q, want %q", got, tc.want)
			}
		})
	}
}

// The estimate's figures come from the bucket listing and the worker's pull;
// no machine publishes a size for them, so no combination of inputs may put
// "published" in the warning.
func TestEnableWarningNeverSpeaksOfPublishedSizes(t *testing.T) {
	leftovers := synclog.ImportResult{
		Stalled: []synclog.Stall{{Origin: "m1", Label: "laptop", Reason: "a body it refers to could not be fetched"}},
		Dropped: []synclog.Drop{{Origin: "m1", Seq: 36207, Reason: `the references of binding ["01C"] refuse every remaining row`}},
	}
	for _, guarded := range []bool{false, true} {
		for _, known := range []bool{false, true} {
			for _, locked := range []bool{false, true} {
				for _, imported := range []synclog.ImportResult{{}, leftovers} {
					res := relevosync.EnableResult{
						Guarded:  guarded,
						Estimate: relevosync.JoinEstimate{Known: known},
						Locked:   locked,
						Imported: imported,
					}
					if got := enableWarning(res); strings.Contains(got, "published") {
						t.Errorf("enableWarning(%+v) = %q, want no mention of published sizes", res, got)
					}
				}
			}
		}
	}
}

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
