package relevo

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestChainWriterMemberGetsScaledRoundCap pins the scaling: a chain's writer
// is built with one build round per plan plus the correction and repair
// budgets and slack, while its reader members leave the store to stamp the
// default.
func TestChainWriterMemberGetsScaledRoundCap(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	settings := chain.Settings{
		MaxCorrections: 3, Regate: 2,
		ReviewerActor: "reviewer", PlannerActor: "lite-planner",
	}
	opts := ChainOptions{Name: "capchain", Feature: "auth", MasterMindID: testMasterMindName}
	members := chainMembersFor(opts, settings)
	resolutions, err := chainResolveActors(rt, members)
	if err != nil {
		t.Fatalf("chainResolveActors: %v", err)
	}
	base := chainBase{
		cwd: t.TempDir(), worktree: t.TempDir(), repo: t.TempDir(),
		feature: "auth", mastermindID: testMasterMindName,
	}
	const plans = 8
	built, err := chainBuildMembers(context.Background(), rt, members, resolutions, base, settings, plans)
	if err != nil {
		t.Fatalf("chainBuildMembers: %v", err)
	}

	want := plans*(1+settings.MaxCorrections+settings.Regate) + chainWriterCapSlack
	if want <= store.DefaultRoundCap {
		t.Fatalf("test setup: want %d must exceed the default %d", want, store.DefaultRoundCap)
	}
	for _, b := range built {
		if chainMemberWriter(members, b.Name) {
			if b.RoundCap != want {
				t.Errorf("writer %s RoundCap = %d, want %d", b.Name, b.RoundCap, want)
			}
			continue
		}
		if b.RoundCap != 0 {
			t.Errorf("reader %s RoundCap = %d, want 0 (the store stamps the default)", b.Name, b.RoundCap)
		}
	}
}

// TestChainWriterRoundCapFloorsAtTheDefault pins the floor: a one-plan chain
// still gets the default cap, never less.
func TestChainWriterRoundCapFloorsAtTheDefault(t *testing.T) {
	t.Parallel()

	got := chainWriterRoundCap(1, chain.Settings{MaxCorrections: 0, Regate: 0})
	if got != store.DefaultRoundCap {
		t.Errorf("chainWriterRoundCap(1, none) = %d, want the default %d", got, store.DefaultRoundCap)
	}
}

// TestOrdinaryBindingKeepsTheDefaultRoundCap pins that only a chain writer is
// scaled: a plain bind still stores the default cap.
func TestOrdinaryBindingKeepsTheDefaultRoundCap(t *testing.T) {
	t.Parallel()

	_, b := seedBound(t)
	if b.RoundCap != store.DefaultRoundCap {
		t.Errorf("RoundCap = %d, want the default %d", b.RoundCap, store.DefaultRoundCap)
	}
}

// TestSendRoundCapIsTyped pins the typed refusal: Send and SendDryRun on a
// binding past its cap both return an error that errors.Is finds under
// ErrRoundCap.
func TestSendRoundCapIsTyped(t *testing.T) {
	t.Parallel()

	capped := func(t *testing.T) (Runtime, string) {
		t.Helper()
		rt, _ := seedBound(t)
		b, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		b.Round, b.RoundCap = 5, 3
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return rt, writePlan(t, "# x")
	}

	rt, file := capped(t)
	if _, err := Send(context.Background(), rt, "webshop", file, SendOptions{}); !errors.Is(err, ErrRoundCap) {
		t.Errorf("Send err = %v, want errors.Is(err, ErrRoundCap)", err)
	}

	rtDry, fileDry := capped(t)
	if _, err := SendDryRun(context.Background(), rtDry, "webshop", fileDry, SendOptions{}); !errors.Is(err, ErrRoundCap) {
		t.Errorf("SendDryRun err = %v, want errors.Is(err, ErrRoundCap)", err)
	}
}
