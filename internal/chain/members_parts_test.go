package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestPartOfAndMemberNameAreInverse(t *testing.T) {
	t.Parallel()

	row := db.ChainRow{Builder: "c", Reviewer: "c-rev", Planner: "c-plan", Security: "c-sec"}
	for part, name := range map[string]string{
		MemberBuilder: "c", MemberReviewer: "c-rev", MemberPlanner: "c-plan", MemberSecurity: "c-sec",
	} {
		if got := MemberName(row, part); got != name {
			t.Errorf("MemberName(%s) = %q, want %q", part, got, name)
		}
		if got := PartOf(row, name); got != part {
			t.Errorf("PartOf(%s) = %q, want %q", name, got, part)
		}
	}
	if PartOf(row, "") != "" || PartOf(row, "other") != "" || MemberName(row, "other") != "" {
		t.Error("an unknown name or part maps to a part")
	}
}

func TestStatusTerminalStopsOnTheThreeEnds(t *testing.T) {
	t.Parallel()

	for status, want := range map[Status]bool{
		StatusDone: true, StatusHalted: true, StatusStopped: true, StatusRunning: false, StatusGone: false,
	} {
		if got := StatusTerminal(string(status)); got != want {
			t.Errorf("StatusTerminal(%s) = %v, want %v", status, got, want)
		}
	}
}

func TestSnapshotRefAndDir(t *testing.T) {
	t.Parallel()

	if got := SnapshotRef(store.Binding{Name: "c"}, 3); got != "refs/relevo/c/round-3" {
		t.Errorf("SnapshotRef = %q", got)
	}
	s := store.New(t.TempDir())
	member := newMemberChainRow(t, s, "c", string(StatusRunning), "")
	want := s.ChainDir("c")
	if got := Dir(s, nil, member.Name); got != want {
		t.Errorf("Dir(store) = %q, want %q", got, want)
	}
	if got := Dir(s, nil, "plain"); got != "" {
		t.Errorf("Dir(store, non-member) = %q", got)
	}
	if got := Dir(nil, nil, member.Name); got != "" {
		t.Errorf("Dir(nil store) = %q", got)
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		if got := Dir(s, tx, member.Name); got != want {
			t.Errorf("Dir(tx) = %q, want %q", got, want)
		}
		if got := Dir(s, tx, "plain"); got != "" {
			t.Errorf("Dir(tx, non-member) = %q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWriterKeeperPicksTheBuilderParamAmongWriters(t *testing.T) {
	t.Parallel()

	actors := map[string]workflow.ActorInfo{
		"alpha": {Shape: workflow.ShapeWriter},
		"beta":  {Shape: workflow.ShapeWriter},
		"rev":   {Shape: workflow.ShapeReader},
	}
	used := []string{"alpha", "beta", "rev"}
	named := workflow.Definition{Params: map[string]workflow.Param{"builder": {Kind: workflow.ParamString, Str: "beta"}}}
	if got := WriterKeeper(named, used, actors); got != "beta" {
		t.Errorf("builder param names beta: keeper = %q", got)
	}
	reader := workflow.Definition{Params: map[string]workflow.Param{"builder": {Kind: workflow.ParamString, Str: "rev"}}}
	if got := WriterKeeper(reader, used, actors); got != "alpha" {
		t.Errorf("builder param names a reader: keeper = %q, want the first writer", got)
	}
	if got := WriterKeeper(workflow.Definition{}, used, actors); got != "alpha" {
		t.Errorf("no builder param: keeper = %q, want the first writer", got)
	}
	if got := WriterKeeper(workflow.Definition{}, []string{"rev"}, actors); got != "" {
		t.Errorf("no writer: keeper = %q", got)
	}
	if !HasParam(named, "builder") || HasParam(named, "gate") {
		t.Error("HasParam misreads the declared params")
	}
}
