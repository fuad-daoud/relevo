package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// policyWithGate is the policy a chain's resolved check reads from.
func policyWithGate(defaultCmd string, regate *int) policy.Policy {
	return policy.Policy{Gate: &policy.GatePolicy{Default: defaultCmd, Regate: regate}}
}

// seedRemoteBase is the commit a seeded remote chain's plan 1 starts at and the
// head its branch is cut from.
const seedRemoteBase = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// remoteChainOpts tunes seedRemoteChain.
type remoteChainOpts struct {
	Round  int // the builder's current round; 0 means 1
	Plans  int // plan copies the chain holds; 0 means 1
	Regate int // the builder member's repair budget
}

// seedRemoteChain plants a store-only chain whose builder member runs on a
// server: a running chain awaiting the builder's round, a builder binding on
// zen carrying branch relevo/<name>, and the local reviewer and planner readers
// built the way ChainStart builds them. Nothing is created on a server and no
// round is started -- the caller drives a close through the catch-up view, or
// stages and ships a round. The chain's own plan copies and the builder's round
// prompt are written, so a builder send and a repair both have their files.
func seedRemoteChain(t *testing.T, rt Runtime, name string, opts remoteChainOpts) store.Binding {
	t.Helper()
	if opts.Round == 0 {
		opts.Round = 1
	}
	if opts.Plans == 0 {
		opts.Plans = 1
	}
	ctx := context.Background()
	settings := chain.Settings{Gate: "make check", Regate: opts.Regate, MaxCorrections: 2, ReviewerActor: "reviewer", PlannerActor: "lite-planner"}
	chainOpts := ChainOptions{Name: name, Feature: "auth", MasterMindID: testMasterMindName}
	members := chainMembersFor(chainOpts, settings)

	repo := t.TempDir()
	base := chainBase{
		cwd: repo, worktree: repo, repo: repo, branch: "relevo/" + name,
		commit: seedRemoteBase, feature: "auth", mastermindID: testMasterMindName,
	}
	resolutions, err := chainResolveActors(rt, members)
	if err != nil {
		t.Fatalf("seedRemoteChain: resolve actors: %v", err)
	}
	built, err := chainBuildMembers(ctx, rt, members, resolutions, base, settings)
	if err != nil {
		t.Fatalf("seedRemoteChain: build members: %v", err)
	}

	builder := built[0]
	builder.Builder = store.Endpoint{Mode: store.ModeRemote, Server: "zen", AgentName: name}
	builder.Worktree = ""
	builder.Branch = "relevo/" + name
	builder.Base = seedRemoteBase
	builder.Repo = repo
	builder.CWD = repo
	builder.Round = opts.Round
	builder.State = store.StateActive
	builder.Gate = settings.Gate
	builder.Regate = opts.Regate
	built[0] = builder

	paths := make([]string, opts.Plans)
	for i := range paths {
		paths[i] = rt.Store.ChainPlanPath(name, i+1)
	}
	planJSON, err := json.Marshal(paths)
	if err != nil {
		t.Fatalf("seedRemoteChain: marshal plan paths: %v", err)
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("seedRemoteChain: marshal settings: %v", err)
	}
	now := baseTime
	if rt.Now != nil {
		now = rt.Now()
	}
	row := db.ChainRow{
		ID: db.NewID(), Name: name, Status: string(chain.StatusRunning),
		Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
		Plan: 1, Plans: opts.Plans, PlanPathsJSON: planJSON, SettingsJSON: settingsJSON,
		Corrections:    0,
		AwaitingMember: chain.MemberBuilder, AwaitingRound: opts.Round,
		Builder: name, Reviewer: name + "-rev", Planner: name + "-plan",
		Base: seedRemoteBase, Branch: "relevo/" + name, Repo: repo,
		PlanStartCommit: seedRemoteBase,
		CreatedAt:       now, UpdatedAt: now,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.CreateChain(row, built)
	}); err != nil {
		t.Fatalf("seedRemoteChain: create chain: %v", err)
	}
	if err := os.MkdirAll(rt.Store.ChainDir(name), 0o755); err != nil {
		t.Fatalf("seedRemoteChain: chain dir: %v", err)
	}
	for i := 1; i <= opts.Plans; i++ {
		if err := os.WriteFile(paths[i-1], []byte(fmt.Sprintf("plan %d\n", i)), 0o644); err != nil {
			t.Fatalf("seedRemoteChain: plan %d: %v", i, err)
		}
	}
	// The builder's own round prompt differs from the plan copy, so the
	// reviewer seed names it beside the plan.
	stageRemoteRoundFile(t, rt, name, opts.Round, "the round prompt\n")
	return builder
}

// remoteRoundFileFunc serves a closed round's files from a name->body map:
// every kind not named answers 404, as a round that wrote nothing does.
func remoteRoundFileFunc(files map[string]string) func(context.Context, string, string, int, string) (io.ReadCloser, error) {
	return func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
		if body, ok := files[kind]; ok {
			return io.NopCloser(strings.NewReader(body)), nil
		}
		return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
	}
}

// remoteChainClose drives one close of the remote builder named name through
// the client catch-up: fr serves the view and the round files, the tick
// reconciles and settles inline, and the chain advances in the same critical
// section. It does not run the pending-send step; the caller does that when it
// wants the staged round shipped. view's RoundState and ClosedRound are filled
// from the builder's current round.
func remoteChainClose(t *testing.T, rt Runtime, fr *fakeRemote, name string, view remote.BindingView, files map[string]string) store.Binding {
	t.Helper()
	b := chainBinding(t, rt, name)
	view.RoundState = remote.RoundClosed
	if view.ClosedRound == 0 {
		view.ClosedRound = b.Round
	}
	fr.getBindingResp = view
	fr.roundFileFunc = remoteRoundFileFunc(files)
	return chainReconcile(t, rt, name)
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
		if builder.Gate != "make check" || builder.Regate != 0 {
			t.Errorf("member gate/regate = %q/%d, want make check/0: a workflow chain's check is a step", builder.Gate, builder.Regate)
		}
		if got := storedSettings(t, res.Chain).Regate; got != 2 {
			t.Errorf("settings regate = %d, want the policy's 2", got)
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
		if builder := memberByName(t, res.Members, "shop"); builder.Regate != 0 {
			t.Errorf("member regate = %d, want 0: the workflow runs the check as a step", builder.Regate)
		}
		if got := storedSettings(t, res.Chain).Regate; got != 3 {
			t.Errorf("settings regate = %d, want the flag's 3", got)
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
