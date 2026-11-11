package relevo

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// forgetServer makes every server call fail the way the client answers for a
// server that is no longer in this machine's config.
func forgetServer(fr *fakeRemote) {
	fr.getChainErr = client.ErrUnknownServer
	fr.chainDoneErr = client.ErrUnknownServer
}

// TestChainDoneOnAChainWhoseServerIsNotConfiguredSettlesLocally pins done for a
// chain on a retired server: nothing can be released there, so the mirror and
// its members settle locally instead of failing on the unknown server.
func TestChainDoneOnAChainWhoseServerIsNotConfiguredSettlesLocally(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusHalted), 0, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")
	forgetServer(fr)

	if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainDone on a chain whose server is not configured = %v, want it settled locally", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Errorf("mirror status = %q, want done", row.Status)
	}
	for _, member := range []string{"shop", "shop-rev", "shop-plan"} {
		got, err := rt.Store.Load(member)
		if err != nil {
			t.Errorf("load mirror %s: %v", member, err)
		} else if got.State != store.StateDone {
			t.Errorf("mirror %s state = %q, want done", member, got.State)
		}
	}
	if n := countCalls(fr, "ChainDone:"); n != 0 {
		t.Errorf("ChainDone calls = %d, want none once the mirror read named the server unknown", n)
	}
}

// TestChainDoneOnARunningChainWhoseServerIsNotConfiguredIsRefused pins the
// guard: the server cannot be asked whether it still runs the chain, so a
// mirror last seen running is refused as running, never settled.
func TestChainDoneOnARunningChainWhoseServerIsNotConfiguredIsRefused(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")
	forgetServer(fr)

	if _, err := ChainDone(context.Background(), rt, "shop"); !errors.Is(err, ErrChainRunning) {
		t.Fatalf("ChainDone on a running chain whose server is not configured = %v, want ErrChainRunning", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("mirror status = %q, want it left running", row.Status)
	}
}

// TestUnbindDoneSweepSettlesADoneChainWhoseServerIsNotConfigured pins the
// sweep: a finished chain on a retired server is closed, not reported as
// unsettled on every unbind --done.
func TestUnbindDoneSweepSettlesADoneChainWhoseServerIsNotConfigured(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusDone), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Fatalf("seeded mirror status = %q, want done", row.Status)
	}
	forgetServer(fr)

	_, err := GCDoneChains(context.Background(), rt, housekeep.GCOptions{AllMasterMinds: true})
	var unsettled *ChainsNotSettledError
	if errors.As(err, &unsettled) {
		t.Fatalf("GCDoneChains left the chain unsettled: %v", err)
	}
	if err != nil {
		t.Fatalf("GCDoneChains: %v", err)
	}
}
