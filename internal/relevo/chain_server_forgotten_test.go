package relevo

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
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

// noClientRuntime is a pulled server chain on a machine left with no remote
// client, its config holding servers; a nil Remote means any server call
// would panic, so a test that passes made none.
func noClientRuntime(t *testing.T, servers remote.Servers) Runtime {
	t.Helper()
	fr := chainPullFake(chainPullView("shop", string(chain.StatusHalted), 0, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")
	rt.Remote = nil
	rt.Config = auditStore(t)
	body, err := client.EncodeServers(servers)
	if err != nil {
		t.Fatalf("EncodeServers: %v", err)
	}
	if _, err := rt.Config.As("test", "seed servers").Put(config.Servers, body); err != nil {
		t.Fatalf("Put servers: %v", err)
	}
	return rt
}

// TestChainDoneWithNoClientSettlesAChainWhoseServerWasRetired pins the machine
// whose last server was retired: no remote client is built, and the chain on
// that server still settles locally.
func TestChainDoneWithNoClientSettlesAChainWhoseServerWasRetired(t *testing.T) {
	t.Parallel()

	rt := noClientRuntime(t, remote.Servers{})

	if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainDone with no client and no such server = %v, want it settled locally", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Errorf("mirror status = %q, want done", row.Status)
	}
}

// TestChainDoneWithNoClientWaitsForAServerStillConfigured pins the other side:
// a server still in config is only missing the client key, so the chain is
// not settled behind its back.
func TestChainDoneWithNoClientWaitsForAServerStillConfigured(t *testing.T) {
	t.Parallel()

	rt := noClientRuntime(t, remote.Servers{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"}})

	if _, err := ChainDone(context.Background(), rt, "shop"); !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("ChainDone with no client and the server configured = %v, want ErrRemoteUnavailable", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status == string(chain.StatusDone) {
		t.Error("the mirror was settled while its server is still configured")
	}
}
