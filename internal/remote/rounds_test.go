package remote

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestChainRoundFactsCompletesOnlyTheViewsRound(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	view := BindingView{ClosedRound: 3, DirtyCommit: "dirty", PriorTokens: &usage.Tokens{In: 7}, RoundStartedAt: at}

	got := ChainRoundFacts(ClosedRoundView{Round: 3}, view)
	if got.DirtyCommit != "dirty" || got.PriorTokens != view.PriorTokens || !got.RoundStartedAt.Equal(at) {
		t.Fatalf("round 3 = %+v, want the view's dirty commit, prior tokens and start", got)
	}

	older := ChainRoundFacts(ClosedRoundView{Round: 2}, view)
	if older.DirtyCommit != "" || older.PriorTokens != nil || !older.RoundStartedAt.IsZero() {
		t.Fatalf("round 2 = %+v, want it untouched", older)
	}

	own := ClosedRoundView{Round: 3, DirtyCommit: "own", PriorTokens: &usage.Tokens{In: 1}, RoundStartedAt: at.Add(-time.Hour)}
	kept := ChainRoundFacts(own, view)
	if kept.DirtyCommit != "own" || kept.PriorTokens != own.PriorTokens || !kept.RoundStartedAt.Equal(own.RoundStartedAt) {
		t.Fatalf("round 3 with its own facts = %+v, want them kept", kept)
	}
}
