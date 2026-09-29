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

	rep, err := MasterMindStatus(ctx, rt, testClaimMasterMind)
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

	repOther, err := MasterMindStatus(ctx, rt, otherClaimMasterMind)
	if err != nil {
		t.Fatalf("MasterMindStatus(other): %v", err)
	}
	if len(repOther.Bindings) != 1 || repOther.Bindings[0].Name != "other" {
		t.Errorf("got %d bindings for the other mastermind, want only 'other'", len(repOther.Bindings))
	}
}

func TestMasterMindStatusEmptyMasterMindIsEmpty(t *testing.T) {
	rt := setupMasterMindStatusStore(t)
	ctx := context.Background()

	rep, err := MasterMindStatus(ctx, rt, "")
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}
	if len(rep.Bindings) != 0 {
		t.Errorf("got %d bindings, want 0", len(rep.Bindings))
	}
}
