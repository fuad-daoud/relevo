package relevo

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestChainMemberRoundCapScalesEveryPart pins each part's per-plan count: the
// builder and reviewer take 1+corrections+regate, the planner corrections+1 and
// security 1+corrections, each plus slack.
func TestChainMemberRoundCapScalesEveryPart(t *testing.T) {
	t.Parallel()

	set := chain.Settings{MaxCorrections: 3, Regate: 2}
	const plans = 7
	cases := map[string]int{
		chain.MemberBuilder:  plans*6 + chainMemberCapSlack,
		chain.MemberReviewer: plans*6 + chainMemberCapSlack,
		chain.MemberPlanner:  plans*4 + chainMemberCapSlack,
		chain.MemberSecurity: plans*4 + chainMemberCapSlack,
	}
	for part, want := range cases {
		if want <= store.DefaultRoundCap {
			t.Fatalf("test setup: %s want %d must exceed the default %d", part, want, store.DefaultRoundCap)
		}
		if got := chainMemberRoundCap(part, plans, set); got != want {
			t.Errorf("chainMemberRoundCap(%s) = %d, want %d", part, got, want)
		}
	}
}

// TestChainMemberRoundCapNeverBelowDefault pins the floor: a one-plan chain
// still gets the default cap for every part, never less.
func TestChainMemberRoundCapNeverBelowDefault(t *testing.T) {
	t.Parallel()

	for _, part := range []string{chain.MemberBuilder, chain.MemberReviewer, chain.MemberPlanner, chain.MemberSecurity} {
		if got := chainMemberRoundCap(part, 1, chain.Settings{}); got != store.DefaultRoundCap {
			t.Errorf("chainMemberRoundCap(%s, 1, none) = %d, want the default %d", part, got, store.DefaultRoundCap)
		}
	}
}

// TestChainMemberRoundCapUnknownPartTakesBuilderCount pins that a part the
// table does not know is scaled like the builder.
func TestChainMemberRoundCapUnknownPartTakesBuilderCount(t *testing.T) {
	t.Parallel()

	set := chain.Settings{MaxCorrections: 3, Regate: 2}
	got := chainMemberRoundCap("custom", 7, set)
	if want := chainMemberRoundCap(chain.MemberBuilder, 7, set); got != want {
		t.Errorf("chainMemberRoundCap(custom) = %d, want the builder's %d", got, want)
	}
}

// TestOrdinaryBindingKeepsTheDefaultRoundCap pins that only a chain member is
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
