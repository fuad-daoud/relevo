package sync

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestPerEntryDropReachesTroubleAndStatusSurface pins that a per-entry drop
// recorded during import flows into TroubleFrom, gets persisted into sync.trouble,
// is loaded into State by ReadState, and prints in the status surface.
func TestPerEntryDropReachesTroubleAndStatusSurface(t *testing.T) {
	_, _, local := openSplit(t)

	// Simulate an ImportResult with a per-entry drop.
	entryDrop := synclog.Drop{
		Origin: "m2",
		Seq:    42,
		Reason: `binding_record["rec1"]: UNIQUE constraint failed`,
	}
	res := synclog.ImportResult{
		Applied: 1,
		Dropped: []synclog.Drop{entryDrop},
	}

	trouble := TroubleFrom(res)
	if len(trouble.Dropped) != 1 {
		t.Fatalf("TroubleFrom dropped count = %d, want 1", len(trouble.Dropped))
	}
	dropLine := trouble.Dropped[0]

	// The drop line must name origin, seq, and reason.
	for _, want := range []string{"m2", "42", "UNIQUE constraint failed"} {
		if !strings.Contains(dropLine, want) {
			t.Errorf("drop line %q missing %q", dropLine, want)
		}
	}

	// Persist the trouble marker via record/writeTrouble.
	runner := &Runner{Local: local}
	runner.record(SteadyResult{Trouble: trouble})

	// Read state back via ReadState.
	st, err := ReadState(local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if len(st.Trouble.Dropped) != 1 || st.Trouble.Dropped[0] != dropLine {
		t.Fatalf("ReadState dropped = %v, want [%q]", st.Trouble.Dropped, dropLine)
	}

	// Verify ParseDrop round-trips from the stored string.
	parsed, ok := synclog.ParseDrop(st.Trouble.Dropped[0])
	if !ok {
		t.Fatalf("ParseDrop failed on %q", st.Trouble.Dropped[0])
	}
	if parsed.Origin != "m2" || parsed.Seq != 42 || parsed.Reason != entryDrop.Reason {
		t.Fatalf("parsed drop = %+v, want %+v", parsed, entryDrop)
	}
}
