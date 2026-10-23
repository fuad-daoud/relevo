package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

var capMembers = map[string]string{
	"shop": chain.MemberBuilder, "shop-rev": chain.MemberReviewer, "shop-plan": chain.MemberPlanner,
}

func capPlans(t *testing.T, n int) []string {
	t.Helper()
	plans := make([]string, n)
	for i := range plans {
		plans[i] = writePlan(t, "build it")
	}
	return plans
}

func setMemberCaps(t *testing.T, rt Runtime, names []string, cap int, edit func(*store.Binding)) {
	t.Helper()
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, n := range names {
			b, err := tx.Load(n)
			if err != nil {
				return err
			}
			b.RoundCap = cap
			if edit != nil {
				edit(&b)
			}
			if err := tx.Save(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("set caps: %v", err)
	}
}

func loadedCap(t *testing.T, rt Runtime, name string) int {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	return b.RoundCap
}

func stoppedCapChain(t *testing.T, opts ChainOptions) Runtime {
	t.Helper()
	rt, _ := chainRuntime(t)
	opts.Plans = capPlans(t, 7)
	stoppedChain(t, rt, opts)
	setMemberCaps(t, rt, []string{"shop", "shop-rev", "shop-plan"}, store.DefaultRoundCap, nil)
	return rt
}

func TestChainResumeRaisesMemberCapsToRecomputed(t *testing.T) {
	t.Parallel()

	rt := stoppedCapChain(t, ChainOptions{MaxCorrections: ptr(3), Regate: ptr(2)})
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	set := chain.Settings{MaxCorrections: 3, Regate: 2}
	for name, part := range capMembers {
		want := chainMemberRoundCap(part, 7, set)
		if want <= store.DefaultRoundCap {
			t.Fatalf("test setup: %s want %d must exceed the default", name, want)
		}
		if got := loadedCap(t, rt, name); got != want {
			t.Errorf("%s RoundCap = %d, want %d", name, got, want)
		}
	}
}

func TestChainResumeWithMaxCorrectionsRaisesCaps(t *testing.T) {
	t.Parallel()

	rt := stoppedCapChain(t, ChainOptions{MaxCorrections: ptr(1)})
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name: "shop", MaxCorrections: ptr(5), Regate: ptr(2),
	}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	set := chain.Settings{MaxCorrections: 5, Regate: 2}
	for name, part := range capMembers {
		want := chainMemberRoundCap(part, 7, set)
		if got := loadedCap(t, rt, name); got != want {
			t.Errorf("%s RoundCap = %d, want %d", name, got, want)
		}
	}
}

func TestChainResumeNeverLowersAMemberCap(t *testing.T) {
	t.Parallel()

	rt := stoppedCapChain(t, ChainOptions{})
	const high = 500
	setMemberCaps(t, rt, []string{"shop-rev"}, high, nil)
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if got := loadedCap(t, rt, "shop-rev"); got != high {
		t.Errorf("shop-rev RoundCap = %d, want the hand-set %d kept", got, high)
	}
}

func TestChainResumeLeavesServedMemberCapAlone(t *testing.T) {
	t.Parallel()

	rt := stoppedCapChain(t, ChainOptions{MaxCorrections: ptr(3), Regate: ptr(2)})
	setMemberCaps(t, rt, []string{"shop-plan"}, store.DefaultRoundCap, func(b *store.Binding) { b.Owner = "alice" })
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if got := loadedCap(t, rt, "shop-plan"); got != store.DefaultRoundCap {
		t.Errorf("served shop-plan RoundCap = %d, want %d untouched", got, store.DefaultRoundCap)
	}
	if got := loadedCap(t, rt, "shop"); got <= store.DefaultRoundCap {
		t.Errorf("local shop RoundCap = %d, want raised above %d", got, store.DefaultRoundCap)
	}
}
