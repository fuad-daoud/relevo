package relevo

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// openRemote saves one open remote round on server, the shape the account
// forward tests start from.
func openRemote(t *testing.T, st *store.Store, name, server string) {
	t.Helper()
	b := remoteBinding(server)
	b.Name = name
	b.CWD = "/fake/" + name
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}
}

// seedAccountGate writes one live group@account rate-limit entry for provider,
// the local record a `gate <token>` leaves before it forwards.
func seedAccountGate(t *testing.T, rt Runtime, provider, accountName string) {
	t.Helper()
	err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: []availability.Entry{
		{Kind: availability.RateLimited, Subject: provider + "@" + accountName, At: baseTime, Source: "planner"},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// TestForwardAvailableRefusesAccountKeyOnOldServer: a group@account clear is a
// key a server without the accounts feature cannot resolve, so the client
// refuses it with a clear line and posts nothing.
func TestForwardAvailableRefusesAccountKeyOnOldServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "open-remote", "zen")

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, "test@cp2")
	want := []string{"zen: account gates unsupported; upgrade the server"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Available:") {
			t.Fatalf("posted an account clear to an old server: %v", fr.calls)
		}
	}
}

// TestForwardAvailableSendsAccountKeyToFeatureServer: a server that advertises
// accounts receives the same group@account key the client cleared locally.
func TestForwardAvailableSendsAccountKeyToFeatureServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		whoAmIResp:    remote.WhoAmI{Features: []string{remote.FeatureAccounts}},
		availableResp: remote.AvailableResponse{Provider: "test", Removed: 1},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, "test@cp2")
	want := []string{"zen: cleared test (1 entries)"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}

	var available []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Available:") {
			available = append(available, c)
		}
	}
	if !slices.Equal(available, []string{"Available:zen:test@cp2"}) {
		t.Fatalf("Available calls = %v, want the account key", available)
	}
}

// TestForwardUnavailableForwardsAccountGates: a candidate token whose logins
// this host's ledger gated is forwarded as the server's own group@account
// keys, so the server's rotation gates the same logins.
func TestForwardUnavailableForwardsAccountGates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureAccounts}},
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Gates:      testGateKV(t),
		Candidates: candidateSet(t, testTwoProviderJSON),
		Now:        func() time.Time { return baseTime },
	}
	seedAccountGate(t, rt, "test", "cp2")

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "hit a limit")
	want := []string{"zen: gated test@cp2"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}

	var unavailable []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailable = append(unavailable, c)
		}
	}
	if !slices.Equal(unavailable, []string{"Unavailable:zen:open-remote:test@cp2"}) {
		t.Fatalf("Unavailable calls = %v, want the account key", unavailable)
	}
}

// TestForwardUnavailableRefusesAccountGateOnOldServer: the same group@account
// gate is refused, not posted, when the server does not advertise accounts.
func TestForwardUnavailableRefusesAccountGateOnOldServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Gates:      testGateKV(t),
		Candidates: candidateSet(t, testTwoProviderJSON),
		Now:        func() time.Time { return baseTime },
	}
	seedAccountGate(t, rt, "test", "cp2")

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "hit a limit")
	want := []string{"zen: account gates unsupported; upgrade the server"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			t.Fatalf("posted an account gate to an old server: %v", fr.calls)
		}
	}
}

// TestReconcileRemoteTakesTheServersAccount: a served round's account is the
// server's own choice, so observing the view carries it onto the client
// binding the owner's own status names it.
func TestReconcileRemoteTakesTheServersAccount(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundIdle, Account: "work"},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderAccount != "work" {
		t.Fatalf("BuilderAccount = %q, want the server's account", got.BuilderAccount)
	}
}

// TestForwardUnavailableAccountGateStaysBareWithoutAccounts: the same forward
// on a host with no accounts and no ledger record is exactly today's bare
// token, so an old server keeps taking bare gates.
func TestForwardUnavailableAccountGateStaysBareWithoutAccounts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, testTwoProviderJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "hit a limit")
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none", lines)
	}

	var unavailable []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailable = append(unavailable, c)
		}
	}
	if !slices.Equal(unavailable, []string{"Unavailable:zen:open-remote:" + testOpencodeRef}) {
		t.Fatalf("Unavailable calls = %v, want the bare token", unavailable)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "WhoAmI:") {
			t.Fatalf("a bare forward asked for features: %v", fr.calls)
		}
	}
}
