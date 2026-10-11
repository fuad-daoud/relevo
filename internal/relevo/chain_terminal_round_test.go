package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// requireNoOpenRows fails when ingest derives an open round for the member.
func requireNoOpenRows(t *testing.T, d *db.DB, member string) {
	t.Helper()
	b, found, err := d.Binding(member)
	if err != nil || !found {
		t.Fatalf("mirror Binding %s: found=%v err=%v", member, found, err)
	}
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		t.Fatalf("Rounds %s: %v", member, err)
	}
	for _, r := range rounds {
		if r.Outcome == db.OutcomeOpen {
			t.Errorf("%s round %d derived outcome open, want none open", member, r.Number)
		}
	}
}

// requireNoticeOnClosedRound fails when the chain's terminal notice is filed
// under a round that holds no report.
func requireNoticeOnClosedRound(t *testing.T, rt Runtime, member string) {
	t.Helper()
	entries := chainLog(t, rt, member)
	var seen bool
	for _, e := range entries {
		if e.Kind != store.KindChain {
			continue
		}
		seen = true
		if !store.HasKind(entries, e.Round, store.DirToMasterMind, store.KindReport) {
			t.Errorf("%s terminal notice filed under round %d, which holds no report", member, e.Round)
		}
	}
	if !seen {
		t.Errorf("%s holds no terminal notice", member)
	}
}

// TestLocalChainTerminalNoticeLeavesNoOpenRound pins that a finished local
// chain's end notice is filed under the round it reports on, so ingest derives
// no open round on any member.
//
// Mutation: file the notice under the carrier's current round, and the builder's
// round 2 holds only the notice and derives open.
func TestLocalChainTerminalNoticeLeavesNoOpenRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Fatalf("chain status = %s, want done", row.Status)
	}

	requireNoticeOnClosedRound(t, rt, "shop")
	for _, m := range []string{"shop", "shop-rev"} {
		d := mirrorTheChain(t, rt, m)
		requireNoOpenRows(t, d, m)
	}
}

// TestMirroredChainTerminalNoticeLeavesNoOpenRound is the same pin for a chain
// the mirror pulled to its end.
//
// Mutation: file the pull's notice under the carrier's current round, and the
// mirror's round 2 holds only the notice and derives open.
func TestMirroredChainTerminalNoticeLeavesNoOpenRound(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)))
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")

	rt.Remote.(*fakeRemote).getChainResp = chainPullView("shop", string(chain.StatusDone), 1, 0, 0)
	pullRounds(t, rt, "shop")
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Fatalf("mirror status = %s, want done", row.Status)
	}

	requireNoticeOnClosedRound(t, rt, "shop")
	d := mirrorTheChain(t, rt, "shop")
	requireNoOpenRows(t, d, "shop")
}
