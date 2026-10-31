package uplink

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestForwardUnavailablePostsBareProviderPerOpenBinding: a provider this
// machine configures travels as itself to the server, which gates it
// provider-wide; the server's candidate list is never consulted, so no
// mismatch line can appear.
func TestForwardUnavailablePostsBareProviderPerOpenBinding(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	saveOpenGate(t, st, "one", "zen")
	saveOpenGate(t, st, "two", "zen")
	fr := &fakeRemote{}
	rt := relevo.Runtime{Store: st, Remote: fr, Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(context.Background(), rt, "test", "hit a limit", time.Time{})
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none", lines)
	}
	got := callsWithPrefix(fr, "Unavailable:")
	want := []string{"Unavailable:zen:one:test", "Unavailable:zen:two:test"}
	if !slices.Equal(got, want) {
		t.Fatalf("Unavailable calls = %v, want %v", got, want)
	}
}

// TestForwardUnavailableProviderForwardsAccountKeys: the live group@account
// keys the ledger holds for a bare provider travel as the account keys, the
// same as for a candidate token of that provider.
func TestForwardUnavailableProviderForwardsAccountKeys(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	saveOpenGate(t, st, "open-remote", "zen")
	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureAccounts}}}
	rt := relevo.Runtime{Store: st, Remote: fr, Gates: testGateKV(t), Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}
	seedAccountGate(t, rt, "test", "cp2")

	lines := ForwardUnavailable(context.Background(), rt, "test", "hit a limit", time.Time{})
	if want := []string{"zen: gated test@cp2"}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	if got, want := callsWithPrefix(fr, "Unavailable:"), []string{"Unavailable:zen:open-remote:test@cp2"}; !slices.Equal(got, want) {
		t.Fatalf("Unavailable calls = %v, want %v", got, want)
	}
}

// TestForwardsSkipUnconfiguredServerSilently: a binding naming a server this
// machine does not configure yields no line, in either forward, for every
// subject form.
func TestForwardsSkipUnconfiguredServerSilently(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	saveOpenGate(t, st, "old", "contabo")
	rt := relevo.Runtime{Store: st, Remote: unknownEverywhere(), Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}
	ctx := context.Background()

	for _, subject := range []string{testOpencodeRef, "test", "test@cp2"} {
		if lines := ForwardUnavailable(ctx, rt, subject, "r", time.Time{}); len(lines) != 0 {
			t.Errorf("ForwardUnavailable(%q) lines = %v, want none", subject, lines)
		}
		if lines := ForwardAvailable(ctx, rt, subject); len(lines) != 0 {
			t.Errorf("ForwardAvailable(%q) lines = %v, want none", subject, lines)
		}
	}
}

// TestForwardUnavailableFailuresEndWithNextCommand: every outcome that leaves
// a configured server ungated carries one command gating the same subject,
// remaining time and reason on that server.
func TestForwardUnavailableFailuresEndWithNextCommand(t *testing.T) {
	t.Parallel()

	until := baseTime.Add(time.Hour)
	cases := []struct {
		name    string
		fr      *fakeRemote
		subject string
		want    string
	}{
		{"list read error", &fakeRemote{candidatesErr: errors.New("boom")}, testOpencodeRef,
			"next: relevo gate --serve opencode/test/m --for 1h0m0s --reason 'hit a limit'  # on zen"},
		{"mismatch", &fakeRemote{candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: "agy/other/x"}}}}, testOpencodeRef,
			"next: relevo gate --serve opencode/test/m --for 1h0m0s --reason 'hit a limit'  # on zen"},
		{"post error", &fakeRemote{unavailableErr: errors.New("boom")}, "test",
			"next: relevo gate --serve test --for 1h0m0s --reason 'hit a limit'  # on zen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := store.New(t.TempDir())
			saveOpenGate(t, st, "a", "zen")
			saveOpenGate(t, st, "b", "zen")
			rt := relevo.Runtime{Store: st, Remote: tc.fr, Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}

			lines := ForwardUnavailable(context.Background(), rt, tc.subject, "hit a limit", until)
			var nexts []string
			for _, l := range lines {
				if strings.HasPrefix(l, "next:") {
					nexts = append(nexts, l)
				}
			}
			if !slices.Equal(nexts, []string{tc.want}) {
				t.Fatalf("next lines = %v, want exactly %q (lines %v)", nexts, tc.want, lines)
			}
		})
	}
}

// TestForwardUnavailableStaleOnlyHasNoNextCommand: stale bindings keep the
// single summary and nothing to run by hand.
func TestForwardUnavailableStaleOnlyHasNoNextCommand(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	saveOpenGate(t, st, "a", "zen")
	fr := &fakeRemote{unavailableErr: staleAnswer()}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(context.Background(), rt, testOpencodeRef, "r", time.Time{})
	if want := []string{staleLine(1)}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
}

// TestForwardUnavailableSkipsStaleBindings: the client holds three open
// remote bindings on one server, and the server has dropped two of them.
// Forwarding a gate posts to the one that is still there and emits a single
// summary naming how many were skipped -- never a "<binding>: <server>: not
// found" line per stale binding.
func TestForwardUnavailableSkipsStaleBindings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	for _, n := range []string{"live-one", "stale-one", "stale-two"} {
		saveOpenGate(t, st, n, "zen")
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
		unavailableErrFor: func(_, name, _ string) error {
			if strings.HasPrefix(name, "stale-") {
				return staleAnswer()
			}
			return nil
		},
	}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "RESOURCE_EXHAUSTED 429", time.Time{})

	for _, l := range lines {
		if strings.Contains(l, "stale-") || strings.Contains(l, "not found") {
			t.Fatalf("forward named a stale binding per line: %v", lines)
		}
	}
	want := "skipped 2 binding(s) the server no longer has"
	if !slices.Contains(lines, want) {
		t.Fatalf("lines = %v, want the summary %q", lines, want)
	}
	if got := strings.Count(strings.Join(lines, "\n"), "skipped"); got != 1 {
		t.Fatalf("summary printed %d times, want exactly 1: %v", got, lines)
	}

	// The binding the server still holds really was gated.
	var posted bool
	for _, c := range fr.calls {
		if c == "Unavailable:zen:live-one:"+testOpencodeRef {
			posted = true
		}
	}
	if !posted {
		t.Fatalf("the live binding was never gated: %v", fr.calls)
	}
}

// TestForwardUnavailableReportsRealFailures: a per-binding failure that is not
// "not found" is still named. The skip covers a binding the server dropped,
// never a failure that has somewhere to be fixed.
func TestForwardUnavailableReportsRealFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "broken", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
		unavailableErr: &client.HTTPError{Status: 500, Body: remote.ErrorBody{Message: "boom"}},
	}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "RESOURCE_EXHAUSTED 429", time.Time{})
	var found bool
	for _, l := range lines {
		if strings.Contains(l, "broken: zen: boom") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a real per-binding failure was swallowed: %v", lines)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "skipped") {
			t.Fatalf("a real failure was counted as stale: %v", lines)
		}
	}
}

// TestForwardNoSummaryWhenNothingIsStale: the summary counts what was skipped,
// so a forward with nothing stale must say nothing about it rather than
// announcing that it skipped nothing.
func TestForwardNoSummaryWhenNothingIsStale(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "live-one", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	for _, lines := range [][]string{
		ForwardUnavailable(ctx, rt, testOpencodeRef, "RESOURCE_EXHAUSTED 429", time.Time{}),
		ForwardAvailable(ctx, rt, testOpencodeRef),
	} {
		for _, l := range lines {
			if strings.Contains(l, "skipped") {
				t.Fatalf("a forward with nothing stale reported a skip: %v", lines)
			}
		}
	}
}

// TestForwardUnavailableForwardsAccountGates: a candidate token whose logins
// this host's ledger gated is forwarded as the server's own group@account
// keys, so the server's rotation gates the same logins.
func TestForwardUnavailableForwardsAccountGates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureAccounts}},
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := relevo.Runtime{
		Store:      st,
		Remote:     fr,
		Gates:      testGateKV(t),
		Candidates: candidateSet(t, testTwoProviderJSON),
		Now:        func() time.Time { return baseTime },
	}
	seedAccountGate(t, rt, "test", "cp2")

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "hit a limit", time.Time{})
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
	saveOpenGate(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := relevo.Runtime{
		Store:      st,
		Remote:     fr,
		Gates:      testGateKV(t),
		Candidates: candidateSet(t, testTwoProviderJSON),
		Now:        func() time.Time { return baseTime },
	}
	seedAccountGate(t, rt, "test", "cp2")

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "hit a limit", time.Time{})
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

// TestForwardUnavailableAccountGateStaysBareWithoutAccounts: the same forward
// on a host with no accounts and no ledger record is exactly today's bare
// token, so an old server keeps taking bare gates.
func TestForwardUnavailableAccountGateStaysBareWithoutAccounts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := relevo.Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, testTwoProviderJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "hit a limit", time.Time{})
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

func TestForwardUnavailable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-remote", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}

	idleB := remoteBinding("zen")
	idleB.Name = "idle-remote"
	idleB.CWD = "/fake/idle-remote"
	if err := st.Save(idleB); err != nil {
		t.Fatal(err)
	}
	// No plan entry for idle-remote's round: its round is not open.

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: "some-token"}}},
	}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(ctx, rt, "some-token", "hit a limit", time.Time{})
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none: the fake never fails", lines)
	}

	unavailableCalls := 0
	var target string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailableCalls++
			target = c
		}
	}
	if unavailableCalls != 1 {
		t.Fatalf("Unavailable calls = %d, want exactly 1: %v", unavailableCalls, fr.calls)
	}
	if !strings.Contains(target, "open-remote") {
		t.Fatalf("the one call = %q, want it naming open-remote", target)
	}
}

// TestForwardUnavailableSendsTheServerTokenForTheSameName: the two machines
// name the same candidate but the server lists it under another provider. The
// server's own token is what travels, and the mapping is announced rather than
// silent.
func TestForwardUnavailableSendsTheServerTokenForTheSameName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-remote", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "opencode/server-group/test-model-1", Name: "test-model-1"},
		}},
	}
	rt := relevo.Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, forwardClientCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, "test-model-1", "hit a limit", time.Time{})
	want := []string{"zen: gated opencode/server-group/test-model-1 for opencode/laptop-group/test-model-1"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}

	var unavailable []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailable = append(unavailable, c)
		}
	}
	if !slices.Equal(unavailable, []string{"Unavailable:zen:open-remote:opencode/server-group/test-model-1"}) {
		t.Fatalf("Unavailable calls = %v, want exactly one for the server token", unavailable)
	}
}

// TestForwardUnavailableSendsTheServerTokenForARenamedProvider: the server
// predates CandidateView.Name and sends only tokens. The provider renamed
// between the two machines is still matched from the harness and model in the
// token itself.
func TestForwardUnavailableSendsTheServerTokenForARenamedProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-remote", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "opencode/server-group/test-model-1"},
		}},
	}
	rt := relevo.Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, forwardClientCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, "test-model-1", "hit a limit", time.Time{})
	want := []string{"zen: gated opencode/server-group/test-model-1 for opencode/laptop-group/test-model-1"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}

	var unavailable []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailable = append(unavailable, c)
		}
	}
	if !slices.Equal(unavailable, []string{"Unavailable:zen:open-remote:opencode/server-group/test-model-1"}) {
		t.Fatalf("Unavailable calls = %v, want exactly one for the server token", unavailable)
	}
}

// TestForwardUnavailableReportsOneLinePerServerWhenNothingMatches: two open
// bindings on one server, and the server lists no candidate the client's
// candidate maps to. One line names the mismatch -- the token, its provider
// and the server's candidates -- followed by its next: command, and nothing is
// posted.
func TestForwardUnavailableReportsOneLinePerServerWhenNothingMatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	for _, name := range []string{"open-one", "open-two"} {
		b := remoteBinding("zen")
		b.Name = name
		b.CWD = "/fake/" + name
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
			t.Fatal(err)
		}
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "opencode/other-group/other-model", Name: "other-model"},
		}},
	}
	rt := relevo.Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, forwardClientCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, "test-model-1", "hit a limit", time.Time{})
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "next: ") {
		t.Fatalf("lines = %v, want the mismatch line and one next: line for the one server", lines)
	}
	for _, want := range []string{"opencode/laptop-group/test-model-1", "laptop-group", "other-model"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("line = %q, want it naming %q", lines[0], want)
		}
	}

	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			t.Errorf("Unavailable was called with no match: %v", fr.calls)
		}
	}
}
