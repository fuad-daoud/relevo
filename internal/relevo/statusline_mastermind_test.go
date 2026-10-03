package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func setupMasterMindStatusStore(t *testing.T) Runtime {
	t.Helper()
	rt := newRuntime(t)
	bindings := []store.Binding{
		{
			Name:             "zeta",
			CWD:              "/a",
			MasterMindID:     testClaimMasterMind,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "alpha",
			CWD:              "/b",
			MasterMindID:     testClaimMasterMind,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "other",
			CWD:              "/c",
			MasterMindID:     otherClaimMasterMind,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "finished",
			CWD:              "/d",
			MasterMindID:     testClaimMasterMind,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateDone,
		},
	}
	for _, b := range bindings {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", b.Name, err)
		}
	}
	return rt
}

func TestMasterMindStatusFiltersToOneMasterMind(t *testing.T) {
	rt := setupMasterMindStatusStore(t)
	ctx := context.Background()

	rep, err := MasterMindStatus(ctx, rt, Scope{MasterMindID: testClaimMasterMind})
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}
	if len(rep.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2", len(rep.Bindings))
	}
	if rep.Bindings[0].Name != "alpha" || rep.Bindings[1].Name != "zeta" {
		t.Errorf("got bindings [%s, %s], want [alpha, zeta]", rep.Bindings[0].Name, rep.Bindings[1].Name)
	}
	for i, b := range rep.Bindings {
		if b.MasterMindID != testClaimMasterMind {
			t.Errorf("row %d MasterMindID = %q, want %q", i, b.MasterMindID, testClaimMasterMind)
		}
	}

	repOther, err := MasterMindStatus(ctx, rt, Scope{MasterMindID: otherClaimMasterMind})
	if err != nil {
		t.Fatalf("MasterMindStatus(other): %v", err)
	}
	if len(repOther.Bindings) != 1 || repOther.Bindings[0].Name != "other" {
		t.Errorf("got %d bindings for the other mastermind, want only 'other'", len(repOther.Bindings))
	}
}

// TestMasterMindStatusEmptyScopeIsTheWholeStore pins the scope rule the line
// and every other format share: an unnamed scope is the fleet, not the empty
// report. It used to be the empty report, which is how a session that could not
// name its MasterMind saw no rows here while `relevo status` showed it
// everything. DONE rows still take no line.
func TestMasterMindStatusEmptyScopeIsTheWholeStore(t *testing.T) {
	rt := setupMasterMindStatusStore(t)
	ctx := context.Background()

	rep, err := MasterMindStatus(ctx, rt, Scope{})
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}
	names := make([]string, 0, len(rep.Bindings))
	for _, b := range rep.Bindings {
		names = append(names, b.Name)
	}
	if len(names) != 3 {
		t.Fatalf("got bindings %v, want the three live ones from every MasterMind", names)
	}
	for _, n := range names {
		if n == "finished" {
			t.Errorf("got bindings %v, want no DONE row", names)
		}
	}
}

// TestMasterMindStatusAllScopeKeepsDoneRows pins that --all is the fleet in
// full for the line too: the DONE rule is the default view's, and the fleet
// view is what asks to see them.
func TestMasterMindStatusAllScopeKeepsDoneRows(t *testing.T) {
	rt := setupMasterMindStatusStore(t)
	ctx := context.Background()

	rep, err := MasterMindStatus(ctx, rt, Scope{All: true})
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}
	if len(rep.Bindings) != 4 {
		t.Errorf("got %d bindings, want all four including the DONE one", len(rep.Bindings))
	}
}
