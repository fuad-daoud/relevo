package uplink

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestForwardAvailableRefusesAccountKeyOnOldServer: a group@account clear is a
// key a server without the accounts feature cannot resolve, so the client
// refuses it with a clear line and posts nothing.
func TestForwardAvailableRefusesAccountKeyOnOldServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "open-remote", "zen")

	fr := &fakeRemote{}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

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
	saveOpenGate(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		whoAmIResp:    remote.WhoAmI{Features: []string{remote.FeatureAccounts}},
		availableResp: remote.AvailableResponse{Provider: "test", Removed: 1},
	}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

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

// TestForwardAvailableSkipsStaleServer: a clear against a server holding no
// such subject is skipped with one summary, not a "not found" line per binding.
func TestForwardAvailableSkipsStaleServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		candidatesResp:  remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
		availableErrFor: func(string, string) error { return staleAnswer() },
	}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, testOpencodeRef)
	if want := "skipped 1 binding(s) the server no longer has"; !slices.Contains(lines, want) {
		t.Fatalf("lines = %v, want the summary %q", lines, want)
	}
	for _, l := range lines {
		if strings.Contains(l, "not found") {
			t.Fatalf("forward printed a per-server not found line: %v", lines)
		}
	}
}

// TestForwardAvailablePostsToEveryServerOnce: a gate can matter most when
// nothing runs, so ForwardAvailable names every server this client's remote
// bindings point at -- open round or not, active or DONE -- once each, sorted,
// with one answer line per server. Filtering to open rounds (as
// ForwardUnavailable does) must fail this: alpha would still be called, beta
// would not.
func TestForwardAvailablePostsToEveryServerOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	// Two remote bindings on alpha: one with an open round, one idle.
	openA := remoteBinding("alpha")
	openA.Name = "open-alpha"
	openA.CWD = "/fake/open-alpha"
	if err := st.Save(openA); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-alpha", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}

	idleA := remoteBinding("alpha")
	idleA.Name = "idle-alpha"
	idleA.CWD = "/fake/idle-alpha"
	if err := st.Save(idleA); err != nil {
		t.Fatal(err)
	}

	// One remote binding on beta, whose work is done.
	doneB := remoteBinding("beta")
	doneB.Name = "done-beta"
	doneB.CWD = "/fake/done-beta"
	doneB.State = store.StateDone
	if err := st.Save(doneB); err != nil {
		t.Fatal(err)
	}

	// One local binding: not a server, so it must never be called.
	local := remoteBinding("")
	local.Name = "local"
	local.CWD = "/fake/local"
	local.Builder = store.Endpoint{PaneID: "w2:p4", Mode: store.Mode("pane")}
	if err := st.Save(local); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{availableResp: remote.AvailableResponse{Provider: "anthropic", Removed: 1}}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, "claude/anthropic/haiku")
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want one line per server (alpha, beta)", lines)
	}

	var calls []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Available:") {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("Available calls = %d, want exactly 2 (one per server): %v", len(calls), fr.calls)
	}
	if calls[0] != "Available:alpha:claude/anthropic/haiku" {
		t.Errorf("call 0 = %q, want alpha first", calls[0])
	}
	if calls[1] != "Available:beta:claude/anthropic/haiku" {
		t.Errorf("call 1 = %q, want beta second", calls[1])
	}
	if !strings.Contains(lines[0], "alpha") {
		t.Errorf("line 0 = %q, want it naming alpha", lines[0])
	}
	if !strings.Contains(lines[1], "beta") {
		t.Errorf("line 1 = %q, want it naming beta", lines[1])
	}
}

// TestForwardAvailableResolvesNameToToken: a candidate name is forwarded to
// every server as its canonical token.
func TestForwardAvailableResolvesNameToToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	b := remoteBinding("alpha")
	b.Name = "open-alpha"
	b.CWD = "/fake/open-alpha"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{availableResp: remote.AvailableResponse{Provider: "test", Removed: 1}}
	rt := relevo.Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, testCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	ForwardAvailable(ctx, rt, "claude-m")

	for _, c := range fr.calls {
		if c == "Available:alpha:"+testClaudeRef {
			return
		}
	}
	t.Errorf("calls = %v, want one Available:alpha:%s", fr.calls, testClaudeRef)
}

// TestForwardAvailableSendsTheServerTokenForTheSameName is the clear mirror of
// TestForwardUnavailableSendsTheServerTokenForTheSameName: the server's own
// token reaches Available, so the server's ledger clears the provider it gates.
func TestForwardAvailableSendsTheServerTokenForTheSameName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	b := remoteBinding("zen")
	b.Name = "open-remote"
	b.CWD = "/fake/open-remote"
	if err := st.Save(b); err != nil {
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

	lines := ForwardAvailable(ctx, rt, "test-model-1")
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one line for the one server", lines)
	}

	var available []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Available:") {
			available = append(available, c)
		}
	}
	if !slices.Equal(available, []string{"Available:zen:opencode/server-group/test-model-1"}) {
		t.Fatalf("Available calls = %v, want exactly one for the server token", available)
	}
}
