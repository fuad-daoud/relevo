package view

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// putChain stores row with every member it names bound beside it, and returns
// the row as stored.
func putChain(t *testing.T, s *store.Store, row db.ChainRow, members ...store.Binding) db.ChainRow {
	t.Helper()
	now := time.Now().UTC()
	row.ID = db.NewID()
	row.CreatedAt, row.UpdatedAt = now, now
	if row.Phase == "" {
		row.Phase, row.Step = "build", "building"
	}
	if row.Plans == 0 {
		row.Plan, row.Plans = 1, 1
	}
	if row.SettingsJSON == nil {
		row.SettingsJSON = []byte(`{"max_corrections":1}`)
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		for _, b := range members {
			if b.CWD == "" {
				b.CWD = s.Dir(b.Name)
			}
			if err := tx.Save(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed chain %q: %v", row.Name, err)
	}
	return row
}

func appendLog(t *testing.T, s *store.Store, name string, e store.LogEntry) {
	t.Helper()
	e.TS = time.Now().UTC()
	if err := s.AppendLog(name, e); err != nil {
		t.Fatalf("append %s: %v", name, err)
	}
}

func TestMembersOfListsTheFilledPartsInOrder(t *testing.T) {
	t.Parallel()

	got := MembersOf(db.ChainRow{Builder: "c", Planner: "c-plan", Security: "c-sec"})
	if want := []string{"c", "c-plan", "c-sec"}; !slices.Equal(got, want) {
		t.Errorf("MembersOf = %q, want %q", got, want)
	}
}

func TestReadAndEndMembersFollowChainMemberForAWorkflowRow(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	row := putChain(t, s, db.ChainRow{Name: "w", Status: string(chain.StatusRunning), Builder: "w", Reviewer: "legacy-rev",
		WorkflowJSON: []byte(`{}`)}, store.Binding{Name: "w"})
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.ChainMembersPut("w", []db.ChainMemberRow{{Binding: "w", Seq: 1}, {Binding: "w-rev", Actor: "reviewer", Seq: 2}, {Binding: "", Seq: 3}})
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"w", "w-rev"}
	if got := ReadMembers(s, row); !slices.Equal(got, want) {
		t.Errorf("ReadMembers = %q, want %q", got, want)
	}
	if got := EndMembers(s, row); !slices.Equal(got, want) {
		t.Errorf("EndMembers = %q, want %q", got, want)
	}
	legacy := db.ChainRow{Name: "l", Builder: "l", Reviewer: "l-rev"}
	if got := ReadMembers(s, legacy); !slices.Equal(got, []string{"l", "l-rev"}) {
		t.Errorf("ReadMembers(legacy) = %q", got)
	}
	if got := EndMembers(s, legacy); !slices.Equal(got, []string{"l", "l-rev"}) {
		t.Errorf("EndMembers(legacy) = %q", got)
	}
}

func TestChainStoreStateKeepsALiveChainOffDone(t *testing.T) {
	t.Parallel()

	for status, want := range map[chain.Status]store.State{
		chain.StatusHalted: store.StateNeedsYou, chain.StatusStopped: store.StateNeedsYou,
		chain.StatusDone: store.StateDone, chain.StatusRunning: store.StateActive,
	} {
		if got := ChainStoreState(string(status)); got != want {
			t.Errorf("ChainStoreState(%s) = %s, want %s", status, got, want)
		}
	}
}

func TestChainRowCarriesTheHaltAndTheManualRound(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	row := putChain(t, s, db.ChainRow{Name: "h", Status: string(chain.StatusHalted), Reason: "gate red", Phase: "build", Step: "checking",
		Builder: "h", Reviewer: "h-rev", AwaitingMember: chain.MemberBuilder, AwaitingRound: 3, MasterMindID: "mm_x"},
		store.Binding{Name: "h", Round: 3}, store.Binding{Name: "h-rev", Round: 1})
	appendLog(t, s, "h", store.LogEntry{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true})
	appendLog(t, s, "h", store.LogEntry{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	appendLog(t, s, "h-rev", store.LogEntry{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport})

	got := ChainRow(s, row)
	if got.Name != "h" || got.Role != "chain" || got.State != string(store.StateNeedsYou) || got.Detail != "gate red" ||
		got.MasterMindID != "mm_x" || got.MasterMindRoute != "pull" {
		t.Fatalf("ChainRow = %+v", got)
	}
	f := got.Chain
	if f.ManualRound != 3 || f.Awaiting != chain.MemberBuilder || f.Step != "checking" {
		t.Errorf("facts = %+v, want manual round 3 awaiting the builder", f)
	}
	if !slices.Equal(f.PendingMembers, []string{"h-rev"}) {
		t.Errorf("PendingMembers = %q, want h-rev", f.PendingMembers)
	}

	done := putChain(t, s, db.ChainRow{Name: "d", Status: string(chain.StatusDone), Builder: "d"}, store.Binding{Name: "d", Round: 1})
	appendLog(t, s, "d", store.LogEntry{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport})
	if f := FactsOf(s, done); f.PendingMembers != nil {
		t.Errorf("done chain PendingMembers = %q, want none", f.PendingMembers)
	}

	appendLog(t, s, "h", store.LogEntry{Round: 3, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true})
	if BuilderRoundOpen(s, row) {
		t.Error("a reported round reads as open")
	}
	if BuilderRoundOpen(s, db.ChainRow{Builder: "gone"}) {
		t.Error("a missing builder reads as open")
	}
}

func TestFactsOfReadsAWorkflowChainsState(t *testing.T) {
	t.Parallel()

	def, err := json.Marshal(workflow.Default())
	if err != nil {
		t.Fatal(err)
	}
	st := workflow.State{
		At:       "check",
		Awaiting: workflow.Awaiting{Step: "check", Run: 2},
		Iter:     map[string]workflow.Iter{"plans": {Index: 1, Items: []string{"a", "b", "c"}}},
		Results:  map[string]workflow.Result{"scan": {Round: 1, Outcomes: map[string]string{"findings": "3"}}},
	}
	state, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(t.TempDir())
	row := putChain(t, s, db.ChainRow{Name: "w", Status: string(chain.StatusRunning), Builder: "w", WorkflowJSON: def, StateJSON: state},
		store.Binding{Name: "w"})

	f := FactsOf(s, row)
	if f.StepAt != "check" || !f.Check || f.Round != 2 || f.PlanPos != 2 || f.PlanTotal != 3 {
		t.Errorf("facts = %+v, want check run 2 at plan 2/3", f)
	}
	if got := StoredFactsOf(row); got.Plans < 1 {
		t.Errorf("StoredFactsOf plans = %d, want at least 1", got.Plans)
	}
	if got := FindingsOfState(row); got != 3 {
		t.Errorf("FindingsOfState = %d, want 3", got)
	}
	if got := FindingsOfState(db.ChainRow{}); got != 0 {
		t.Errorf("FindingsOfState(no state) = %d", got)
	}
}

func TestApplyChainsFoldsMembersAndNestsForkChildren(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	running := string(chain.StatusRunning)
	parent := putChain(t, s, db.ChainRow{Name: "p", Status: running, Builder: "p", Reviewer: "p-rev"},
		store.Binding{Name: "p"}, store.Binding{Name: "p-rev"})
	kid10 := putChain(t, s, db.ChainRow{Name: "p.10", Parent: "p", Status: string(chain.StatusDone), Builder: "p.10"}, store.Binding{Name: "p.10"})
	kid2 := putChain(t, s, db.ChainRow{Name: "p.2", Parent: "p", Status: string(chain.StatusHalted), Reason: "red", Builder: "p.2"}, store.Binding{Name: "p.2"})
	orphan := putChain(t, s, db.ChainRow{Name: "q", Status: string(chain.StatusHalted), Builder: "q"})
	done := putChain(t, s, db.ChainRow{Name: "r", Status: string(chain.StatusDone), Builder: "r"})

	rep := Report{Bindings: []BindingStatus{{Name: "p"}, {Name: "p-rev"}, {Name: "p.2"}, {Name: "p.10"}, {Name: "solo"}}}
	got := ApplyChains(s, rep, []db.ChainRow{parent, kid10, kid2, orphan, done})
	var names []string
	for _, b := range got.Bindings {
		names = append(names, b.Name)
	}
	if want := []string{"q", "p", "p.2", "p.10", "solo"}; !slices.Equal(names, want) {
		t.Fatalf("rows = %q, want %q", names, want)
	}
	if f := got.Bindings[1].Chain; !slices.Equal(f.Children, []string{"p.2", "p.10"}) || f.ChildrenDone != 1 {
		t.Errorf("parent facts = %q done %d, want p.2 p.10 done 1", f.Children, f.ChildrenDone)
	}
	if got.Bindings[2].Detail != "red" {
		t.Errorf("halted child detail = %q", got.Bindings[2].Detail)
	}

	plain := Report{Bindings: []BindingStatus{{Name: "solo"}}}
	if out := ApplyChains(s, plain, nil); len(out.Bindings) != 1 {
		t.Errorf("no chains: rows = %+v", out.Bindings)
	}
	if kids, n := ChildRows(nil, parent); kids != nil || n != 0 {
		t.Errorf("ChildRows(nil store) = %v %d", kids, n)
	}
}
