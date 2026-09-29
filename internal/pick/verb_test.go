package pick

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// TestRowsForFiltersPerVerb pins the spec §4 table.
func TestRowsForFiltersPerVerb(t *testing.T) {
	// State is what HideDone keys on; Display is derived from it in a real
	// report. A row with Display "DONE" and no State cannot occur, so the
	// fixture sets both -- round 1 set only Display and the test could not
	// pass against the real HideDone.
	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: "active", State: string(store.StateActive), Display: "ACTIVE", BuilderStatus: "working"},
		{Name: "blocked", State: string(store.StateNeedsYou), Display: "NEEDS YOU", BuilderStatus: "blocked"},
		{Name: "finished", State: string(store.StateDone), Display: "DONE", BuilderStatus: "idle"},
	}}
	names := func(rows []view.BindingStatus) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Name)
		}
		return out
	}
	cases := []struct {
		verb Verb
		want []string
	}{
		{VerbDone, []string{"active", "blocked"}},
		{VerbUnbind, []string{"active", "blocked", "finished"}},
	}
	for _, c := range cases {
		got := names(rowsFor(c.verb, rep))
		if len(got) != len(c.want) {
			t.Errorf("%s: rows = %v, want %v", c.verb, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: rows = %v, want %v", c.verb, got, c.want)
				break
			}
		}
	}
}

func TestEmptyTextPerVerb(t *testing.T) {
	cases := map[Verb]string{
		VerbDone:   "no bindings to mark done",
		VerbUnbind: "nothing bound",
	}
	for verb, want := range cases {
		if got := emptyText(verb); got != want {
			t.Errorf("%s: %q, want %q", verb, got, want)
		}
	}
}

func TestNeedsConfirmOnlyForLiveRowsUnderDestructiveVerbs(t *testing.T) {
	live := []string{"ACTIVE", "NEEDS YOU", "HELD"}
	for _, d := range live {
		if !needsConfirm(VerbDone, row("a", d, "working")) {
			t.Errorf("done on %s row: needsConfirm = false, want true", d)
		}
		if !needsConfirm(VerbUnbind, row("a", d, "working")) {
			t.Errorf("unbind on %s row: needsConfirm = false, want true", d)
		}
	}
	if needsConfirm(VerbUnbind, row("a", "DONE", "idle")) {
		t.Error("unbind on a DONE row must run at once")
	}
	if needsConfirm(VerbDone, row("a", "DONE", "idle")) {
		t.Error("done never lists DONE rows; the rule still says no confirm for one")
	}
}
