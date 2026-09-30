package relevo

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// policyWithGate is the policy a chain's resolved check reads from.
func policyWithGate(defaultCmd string, regate *int) policy.Policy {
	return policy.Policy{Gate: &policy.GatePolicy{Default: defaultCmd, Regate: regate}}
}

// chainRemoteFake is a server a chain's builder can be placed on: it carries
// chain members, labels and actors, lists the one test candidate, and answers a
// create and a round start.
func chainRemoteFake() *fakeRemote {
	return &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{
			remote.FeatureChainMember, remote.FeatureLabels, remote.FeatureRoles,
		}},
		candidatesResp:    remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testClaudeRef}}},
		createBindingResp: remote.BindingView{Name: "shop", Candidate: testClaudeRef, Tier: "harness"},
		startRoundResp:    remote.BindingView{RoundState: remote.RoundRunning},
	}
}

// chainRemoteRows is chainRows with every actor placed: the builder only on the
// server, each reader on the server and then local. A reader's server entry is
// skipped unprobed.
func chainRemoteRows() map[string]roles.Row {
	rows := chainRows()
	b := rows["builder"]
	b.Placement = []string{"zen"}
	rows["builder"] = b
	for _, name := range []string{"reviewer", "lite-planner"} {
		r := rows[name]
		r.Placement = []string{"zen", "local"}
		rows[name] = r
	}
	return rows
}

// chainReaderRows is chainRows with only the readers placed on a server first:
// the builder names no placement, so nothing is probed for it and the readers'
// server entries are the only ones a probe could touch.
func chainReaderRows() map[string]roles.Row {
	rows := chainRows()
	for _, name := range []string{"reviewer", "lite-planner"} {
		r := rows[name]
		r.Placement = []string{"zen", "local"}
		rows[name] = r
	}
	return rows
}

// chainRemoteRuntime is a chain runtime whose builder actor places its round on
// zen: a fake git that resolves a base and a root, a fake transport that ships a
// snapshot, and the given fake server.
func chainRemoteRuntime(t *testing.T, fr *fakeRemote) (Runtime, *fakeGit, *fakeTransport) {
	t.Helper()
	rt, fg := chainRuntime(t)
	fg.headCommitID = "1111111111111111111111111111111111111111"
	fg.rootCommitSHA = "2222222222222222222222222222222222222222"
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, chainRemoteRows())
	rt.Remote = fr
	ft := &fakeTransport{snapshotResp: remote.Snapshot{
		Heads: map[string]string{"refs/relevo/shop/out": "1111111111111111111111111111111111111111"},
	}}
	rt.Transport = ft
	return rt, fg, ft
}

// chainPlacedRuntime is chainRuntime with the given placement rows and server.
func chainPlacedRuntime(t *testing.T, fr *fakeRemote, rows map[string]roles.Row) (Runtime, *fakeGit) {
	t.Helper()
	rt, fg := chainRuntime(t)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	rt.Remote = fr
	return rt, fg
}

// TestChainStartRemoteBuilderCreatesThereAndShipsPlanOne pins the whole remote
// start: the server create carries the repo id, the base, the builder role, the
// mapped candidate, the chain's gate and labels and the author; the local
// branch is cut at base and no worktree is made; the readers stay local on the
// chain's own checkout; the chain row carries Worktree ""; and the step ships
// plan 1 with verify off.
func TestChainStartRemoteBuilderCreatesThereAndShipsPlanOne(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, fg, _ := chainRemoteRuntime(t, fr)
	repo, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	res := startedChain(t, rt, ChainOptions{})

	req := fr.createBindingReq
	if req.Name != "shop" || req.RepoID == "" {
		t.Errorf("create request = %+v, want shop with a repo id", req)
	}
	if req.BaseCommit != "1111111111111111111111111111111111111111" {
		t.Errorf("BaseCommit = %q, want the resolved head", req.BaseCommit)
	}
	if req.Role != "builder" || req.Candidate != testClaudeRef {
		t.Errorf("Role/Candidate = %q/%q, want builder/%s", req.Role, req.Candidate, testClaudeRef)
	}
	if req.Gate != "" {
		t.Errorf("Gate = %q, want none with no gate anywhere", req.Gate)
	}
	if req.Feature != "auth" {
		t.Errorf("Feature = %q, want auth", req.Feature)
	}
	if req.Author == nil || req.Author.Name != "Test User" || req.Author.Email != "test@example.com" {
		t.Errorf("Author = %+v, want the fake git identity", req.Author)
	}

	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("AddWorktree calls = %+v, want none for a remote builder", fg.addWorktreeCalls)
	}
	if len(fg.createBranchCalls) != 1 || fg.createBranchCalls[0].Branch != "relevo/shop" ||
		fg.createBranchCalls[0].Commit != "1111111111111111111111111111111111111111" {
		t.Fatalf("CreateBranch calls = %+v, want relevo/shop at the resolved base", fg.createBranchCalls)
	}

	builder := memberByName(t, res.Members, "shop")
	if !builder.Builder.Remote() || builder.Builder.Server != "zen" {
		t.Errorf("builder = %+v, want a remote builder on zen", builder)
	}
	if builder.Worktree != "" || builder.Branch != "relevo/shop" || builder.Base != "1111111111111111111111111111111111111111" {
		t.Errorf("builder worktree/branch/base = %q/%q/%q, want \"\"/relevo/shop/the base", builder.Worktree, builder.Branch, builder.Base)
	}
	if res.Chain.Worktree != "" || res.Chain.Branch != "relevo/shop" || res.Chain.Base != "1111111111111111111111111111111111111111" {
		t.Errorf("chain row worktree/branch/base = %q/%q/%q, want \"\"/relevo/shop/the base",
			res.Chain.Worktree, res.Chain.Branch, res.Chain.Base)
	}

	for _, name := range []string{"shop-rev", "shop-plan"} {
		m := memberByName(t, res.Members, name)
		if m.Builder.Remote() || m.Worktree != "" || m.Branch != "" {
			t.Errorf("%s = %+v, want a local reader with no worktree and no branch", name, m)
		}
		if m.CWD != repo {
			t.Errorf("%s CWD = %q, want the chain's repo %q", name, m.CWD, repo)
		}
	}

	// Plan 1 was shipped by the step: one StartRound with the plan text and
	// verify off.
	if string(fr.startRoundPlan) != "build it" {
		t.Errorf("shipped plan = %q, want the plan copy", fr.startRoundPlan)
	}
	if fr.startRoundVerify == nil || *fr.startRoundVerify {
		t.Errorf("verify = %v, want an explicit false", fr.startRoundVerify)
	}
	if !slices.Contains(fr.calls, "StartRound:zen:shop:1") {
		t.Errorf("calls = %v, want plan 1 shipped to zen", fr.calls)
	}
}

// TestChainStartRemoteBuilderKeepsReadersOnTheChainRepo pins the readers' tree:
// a remote builder gives every reader the chain's own checkout, never the
// builder's worktree (there is none).
func TestChainStartRemoteBuilderKeepsReadersOnTheChainRepo(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, fg, _ := chainRemoteRuntime(t, fr)
	repo, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	res := startedChain(t, rt, ChainOptions{})

	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("AddWorktree calls = %+v, want none", fg.addWorktreeCalls)
	}
	for _, m := range res.Members {
		if m.Name == "shop" {
			continue
		}
		if m.CWD != repo || m.Worktree != "" || m.Branch != "" {
			t.Errorf("%s = CWD %q worktree %q branch %q, want the chain repo with neither", m.Name, m.CWD, m.Worktree, m.Branch)
		}
	}
}

// TestChainStartRemoteBuilderCarriesTheResolvedCheck pins the check the create
// and the member carry: the chain's resolved Settings, so a policy
// gate.default travels with no flag at all, --no-gate sends "", and --regate
// stays a client-side fact.
func TestChainStartRemoteBuilderCarriesTheResolvedCheck(t *testing.T) {
	t.Parallel()

	t.Run("a policy gate.default travels with no flag", func(t *testing.T) {
		t.Parallel()

		fr := chainRemoteFake()
		rt, _, _ := chainRemoteRuntime(t, fr)
		rt.Policy = policyWithGate("make check", ptr(2))

		res := startedChain(t, rt, ChainOptions{})

		if fr.createBindingReq.Gate != "make check" {
			t.Errorf("create Gate = %q, want the resolved make check", fr.createBindingReq.Gate)
		}
		builder := memberByName(t, res.Members, "shop")
		if builder.Gate != "make check" || builder.Regate != 2 {
			t.Errorf("member gate/regate = %q/%d, want make check/2", builder.Gate, builder.Regate)
		}
		if res.Check != "make check" {
			t.Errorf("result check = %q, want make check", res.Check)
		}
	})

	t.Run("no-gate sends an empty check", func(t *testing.T) {
		t.Parallel()

		fr := chainRemoteFake()
		rt, _, _ := chainRemoteRuntime(t, fr)
		rt.Policy = policyWithGate("make check", ptr(2))

		res := startedChain(t, rt, ChainOptions{NoGate: true})

		if fr.createBindingReq.Gate != "" {
			t.Errorf("create Gate = %q, want none", fr.createBindingReq.Gate)
		}
		if builder := memberByName(t, res.Members, "shop"); builder.Gate != "" {
			t.Errorf("member gate = %q, want none", builder.Gate)
		}
	})

	t.Run("regate stays client-side", func(t *testing.T) {
		t.Parallel()

		fr := chainRemoteFake()
		rt, _, _ := chainRemoteRuntime(t, fr)

		res := startedChain(t, rt, ChainOptions{Regate: ptr(3)})

		if fr.createBindingReq.Gate != "" {
			t.Errorf("create Gate = %q, want none with no gate flag", fr.createBindingReq.Gate)
		}
		if builder := memberByName(t, res.Members, "shop"); builder.Regate != 3 {
			t.Errorf("member regate = %d, want the flag's 3", builder.Regate)
		}
	})
}

// TestChainStartRemoteBuilderUnwindsEveryFailure pins the all-or-none order:
// a failed server create leaves nothing local at all, and a failed local branch
// after the create unbinds the server binding first.
func TestChainStartRemoteBuilderUnwindsEveryFailure(t *testing.T) {
	t.Parallel()

	t.Run("the server create fails: nothing local", func(t *testing.T) {
		t.Parallel()

		fr := chainRemoteFake()
		fr.createBindingErr = errors.New("server said no")
		rt, fg, _ := chainRemoteRuntime(t, fr)

		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
		})
		if err == nil || !strings.Contains(err.Error(), "server said no") {
			t.Fatalf("err = %v, want the create failure", err)
		}
		if len(fg.createBranchCalls) != 0 {
			t.Errorf("CreateBranch calls = %+v, want none", fg.createBranchCalls)
		}
		if slices.Contains(fr.calls, "Unbind:zen:shop") {
			t.Errorf("calls = %v, want no Unbind when the create itself failed", fr.calls)
		}
		assertNothingCreated(t, rt, fg, "shop")
	})

	t.Run("the local branch fails: the server binding is unbound", func(t *testing.T) {
		t.Parallel()

		fr := chainRemoteFake()
		rt, fg, _ := chainRemoteRuntime(t, fr)
		fg.createBranchErr = errors.New("disk full")

		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
		})
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("err = %v, want the branch failure", err)
		}
		if !slices.Contains(fr.calls, "Unbind:zen:shop") {
			t.Errorf("calls = %v, want the server binding unbound", fr.calls)
		}
		if _, err := rt.Store.Chain("shop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("chain exists after the unwind: %v", err)
		}
		for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
			if _, err := rt.Store.Load(name); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("binding %q exists after the unwind: %v", name, err)
			}
		}
	})
}

// TestChainStartReaderPlacementStaysLocal pins the reader rule: a reader's
// server entries are skipped unprobed, the member stays local, and its pick
// note records the skip. With only the readers placed, no call to the server is
// made at all.
func TestChainStartReaderPlacementStaysLocal(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{}
	rt, _ := chainPlacedRuntime(t, fr, chainReaderRows())

	res := startedChain(t, rt, ChainOptions{})

	rev := memberByName(t, res.Members, "shop-rev")
	if rev.Builder.Remote() || rev.Builder.Server != "" {
		t.Errorf("reviewer = %+v, want a local reader", rev)
	}
	if len(fr.calls) != 0 {
		t.Errorf("server calls = %v, want none: a reader's server entries are never probed", fr.calls)
	}

	note := lastPickNote(t, rt.Store, "shop-rev")
	want := "; placement local (actor); skipped zen (a chain reader reads the builder's artifacts on this machine)"
	if !strings.HasSuffix(note, want) {
		t.Errorf("reviewer pick note = %q, want it to end with %q", note, want)
	}
}

// TestChainStartRefusesWhenNoPlacementIsViable pins the refusal: a builder whose
// list is only a server without chain_member leaves no viable entry, and the
// one line names the server and the missing feature.
func TestChainStartRefusesWhenNoPlacementIsViable(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	fr.whoAmIResp = remote.WhoAmI{Features: []string{remote.FeatureLabels, remote.FeatureRoles}}
	rt, fg, _ := chainRemoteRuntime(t, fr)

	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
	})
	if err == nil {
		t.Fatal("ChainStart = nil, want the placement refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, `no viable placement for actor "builder"`) {
		t.Errorf("err = %q, want the placement one-liner", msg)
	}
	if !strings.Contains(msg, "zen (chain members unsupported; upgrade the server)") {
		t.Errorf("err = %q, want it to name zen and the missing feature", msg)
	}
	assertNothingCreated(t, rt, fg, "shop")
	if slices.Contains(fr.calls, "CreateBinding:zen:shop") {
		t.Errorf("calls = %v, want no create after the refusal", fr.calls)
	}
}
