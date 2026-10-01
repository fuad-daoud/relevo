package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainServerFake is a server that can run a whole chain: it advertises the
// chain and readers features, origin, and (so a misplaced placement walk is
// visible) placement and chain-member support.
func chainServerFake() *fakeRemote {
	return &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{
			remote.FeatureChain, remote.FeatureReaders, remote.FeatureOrigin,
			remote.FeaturePlacement, remote.FeatureChainMember, remote.FeatureLabels,
		}},
		createChainResp: chainServerView("shop", seedRemoteBase),
	}
}

// chainServerView is the chain view a create answers with: a running chain
// awaiting the builder's first round, with one member view per part.
func chainServerView(name, base string) remote.ChainView {
	member := func(part, memberName, actor, shape string) remote.ChainMemberView {
		return remote.ChainMemberView{
			Part: part, Name: memberName, Actor: actor,
			View: remote.BindingView{
				Name: memberName, Candidate: testClaudeRef, Tier: "harness",
				Shape: shape, State: string(store.StateActive), Round: 1,
			},
		}
	}
	return remote.ChainView{
		Name: name, Status: string(chain.StatusRunning),
		Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
		Plan: 1, Plans: 1, AwaitingMember: chain.MemberBuilder, AwaitingRound: 1,
		Base: base, Branch: "relevo/" + name, PlanStartCommit: base,
		Members: []remote.ChainMemberView{
			member(chain.MemberBuilder, name, "builder", store.ShapeWriter),
			member(chain.MemberReviewer, name+"-rev", "reviewer", store.ShapeReader),
			member(chain.MemberPlanner, name+"-plan", "lite-planner", store.ShapeReader),
		},
	}
}

// chainServerRuntime is a runtime a server chain can start on: the chain
// runtime's git and registry, the caller's mastermind, the given server and an
// empty snapshot.
func chainServerRuntime(t *testing.T, fr *fakeRemote) (Runtime, *fakeGit, *fakeTransport) {
	t.Helper()
	rt, fg := chainRuntime(t)
	fg.headCommitID = seedRemoteBase
	fg.rootCommitSHA = "2222222222222222222222222222222222222222"
	// The actors name a server: a start that walked placements would probe it,
	// so the mirror test's "no Actor call" pins that it never does.
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, chainRemoteRows())
	rt.Remote = fr
	ft := &fakeTransport{snapshotResp: remote.Snapshot{Empty: true}}
	rt.Transport = ft
	return rt, fg, ft
}

// chainServerOpts is the start a server-chain test drives.
func chainServerOpts(t *testing.T) ChainOptions {
	t.Helper()
	return ChainOptions{
		Name: "shop", Plans: []string{writePlan(t, "build it")},
		Feature: "auth", MasterMindID: testMasterMindName, Server: "zen",
	}
}

// seedServerChain plants a store-only client mirror of a running server chain:
// the row names zen, the builder is remote on zen with the branch relevo/<n>,
// and the readers are remote on zen with no branch.
func seedServerChain(t *testing.T, rt Runtime, name string) store.Binding {
	t.Helper()
	ctx := context.Background()
	opts := ChainOptions{Name: name, Feature: "auth", MasterMindID: testMasterMindName}
	settings := chainSettings(rt.Policy, opts, false)
	members := chainMembersFor(opts, settings)
	repo := rt.Store.Dir(name)
	repoRef := captureRepo(ctx, rt, repo)
	built := make([]store.Binding, 0, len(members))
	for _, m := range members {
		b := store.Binding{
			Name: m.name, CWD: repo, Repo: repo,
			MasterMindID:     testMasterMindID,
			Builder:          store.Endpoint{Mode: store.ModeRemote, Server: "zen", AgentName: m.name},
			BuilderCandidate: testClaudeRef, Round: 1, State: store.StateActive,
			Role: normRole(m.actor), Shape: m.shape, RepoRef: repoRef,
			Feature: "auth",
		}
		if m.writer {
			b.Branch = "relevo/" + name
			b.Base = seedRemoteBase
			b.Gate = settings.Gate
			b.Regate = settings.Regate
		}
		built = append(built, b)
	}
	paths := []string{rt.Store.ChainPlanPath(name, 1)}
	planJSON, err := json.Marshal(paths)
	if err != nil {
		t.Fatalf("seedServerChain: marshal paths: %v", err)
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("seedServerChain: marshal settings: %v", err)
	}
	now := baseTime
	if rt.Now != nil {
		now = rt.Now()
	}
	row := db.ChainRow{
		ID: db.NewID(), Name: name, Status: string(chain.StatusRunning),
		Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
		Plan: 1, Plans: 1, PlanPathsJSON: planJSON, SettingsJSON: settingsJSON,
		AwaitingMember: chain.MemberBuilder, AwaitingRound: 1,
		Builder: name, Reviewer: name + "-rev", Planner: name + "-plan",
		Base: seedRemoteBase, Branch: "relevo/" + name, Repo: repo,
		Server: "zen", MasterMindID: testMasterMindID, PlanStartCommit: seedRemoteBase,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(row, built) }); err != nil {
		t.Fatalf("seedServerChain: create: %v", err)
	}
	if err := os.MkdirAll(rt.Store.ChainDir(name), 0o755); err != nil {
		t.Fatalf("seedServerChain: chain dir: %v", err)
	}
	if err := os.WriteFile(paths[0], []byte("build it\n"), 0o644); err != nil {
		t.Fatalf("seedServerChain: plan copy: %v", err)
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("seedServerChain: load builder: %v", err)
	}
	return b
}

// TestChainStartServerRefusesAServerWithoutTheFeature pins the feature check:
// a server missing chain, readers, or both is refused with the server and each
// missing feature named, before any create, branch or row.
func TestChainStartServerRefusesAServerWithoutTheFeature(t *testing.T) {
	t.Parallel()

	arms := []struct {
		name     string
		features []string
		want     string
	}{
		{"no chain", []string{remote.FeatureReaders}, `missing the "chain" feature`},
		{"no readers", []string{remote.FeatureChain}, `missing the "readers" feature`},
		{"neither", nil, `missing the "chain" and "readers" features`},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()

			fr := chainServerFake()
			fr.whoAmIResp = remote.WhoAmI{Features: arm.features}
			rt, fg, _ := chainServerRuntime(t, fr)

			_, err := ChainStart(context.Background(), rt, chainServerOpts(t))
			if err == nil {
				t.Fatal("ChainStart --server = nil, want the feature refusal")
			}
			if !strings.Contains(err.Error(), "server zen cannot run a chain") || !strings.Contains(err.Error(), arm.want) {
				t.Errorf("err = %q, want it to name zen and %s", err.Error(), arm.want)
			}
			for _, c := range fr.calls {
				if !strings.HasPrefix(c, "WhoAmI") {
					t.Errorf("calls = %v, want only WhoAmI", fr.calls)
				}
			}
			if len(fg.updateRefCalls) != 0 || len(fg.createBranchCalls) != 0 {
				t.Errorf("git calls = refs %v, branches %v, want none", fg.updateRefCalls, fg.createBranchCalls)
			}
			assertNothingCreated(t, rt, fg, "shop")
		})
	}
}

// TestChainStartServerPostsOneCreateAndRecordsTheMirror pins the whole start:
// one create carries the plans, settings, labels, base, author and bundle; the
// local branch is cut; the mirror row names the server with no worktree; every
// member is remote on zen with the reader branchless; and no placement is
// probed.
func TestChainStartServerPostsOneCreateAndRecordsTheMirror(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, fg, ft := chainServerRuntime(t, fr)
	ft.snapshotResp = remote.Snapshot{
		Heads: map[string]string{"refs/relevo/shop/out": seedRemoteBase},
		Body:  io.NopCloser(strings.NewReader("bundle-bytes")),
	}
	repo, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	res, err := ChainStart(context.Background(), rt, chainServerOpts(t))
	if err != nil {
		t.Fatalf("ChainStart --server: %v", err)
	}

	req := fr.createChainReq
	if req.Name != "shop" || req.RepoID == "" || req.BaseCommit != seedRemoteBase {
		t.Errorf("create = %+v, want shop at the resolved base with a repo id", req)
	}
	if len(req.Plans) != 1 || req.Plans[0] != "build it" {
		t.Errorf("create plans = %q, want the plan text", req.Plans)
	}
	if req.Settings.ReviewerActor != "reviewer" || req.Settings.PlannerActor != "lite-planner" || req.Settings.MaxCorrections != 3 {
		t.Errorf("create settings = %+v, want the resolved policy", req.Settings)
	}
	if req.Feature != "auth" {
		t.Errorf("create feature = %q, want auth", req.Feature)
	}
	if req.Author == nil || req.Author.Name != "Test User" || req.Author.Email != "test@example.com" {
		t.Errorf("create author = %+v, want the fake git identity", req.Author)
	}
	if len(req.ClientBindingIDs) != 3 {
		t.Errorf("client binding ids = %v, want one per member", req.ClientBindingIDs)
	}
	if fr.createChainBundle == nil || string(fr.createChainBundle) != "bundle-bytes" {
		t.Errorf("bundle = %q, want the snapshot body", fr.createChainBundle)
	}
	if len(fg.updateRefCalls) != 1 || fg.updateRefCalls[0].Ref != "refs/relevo/shop/out" || fg.updateRefCalls[0].NewSHA != seedRemoteBase {
		t.Errorf("update refs = %+v, want refs/relevo/shop/out at the base", fg.updateRefCalls)
	}
	if len(fg.createBranchCalls) != 1 || fg.createBranchCalls[0].Branch != "relevo/shop" || fg.createBranchCalls[0].Commit != seedRemoteBase {
		t.Fatalf("CreateBranch = %+v, want relevo/shop at the base", fg.createBranchCalls)
	}

	row := res.Chain
	if row.Server != "zen" || row.Worktree != "" || row.Branch != "relevo/shop" {
		t.Errorf("mirror row = server %q worktree %q branch %q, want zen/\"\"/relevo/shop", row.Server, row.Worktree, row.Branch)
	}
	builder := memberByName(t, res.Members, "shop")
	if !builder.Builder.Remote() || builder.Builder.Server != "zen" || builder.Branch != "relevo/shop" || builder.Base != seedRemoteBase {
		t.Errorf("builder mirror = %+v, want remote on zen on relevo/shop at the base", builder)
	}
	for _, name := range []string{"shop-rev", "shop-plan"} {
		m := memberByName(t, res.Members, name)
		if !m.Builder.Remote() || m.Builder.Server != "zen" || m.Branch != "" || m.CWD != repo {
			t.Errorf("%s mirror = %+v, want remote on zen with no branch in the repo", name, m)
		}
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Actor:") {
			t.Errorf("calls = %v, want no placement probe for a server chain", fr.calls)
		}
	}
}

// TestChainStartServerLocalFailureLeavesNoBranch pins the all-or-none after
// the server accepted: a store failure leaves no branch, no row and no binding,
// and the error asks for the same command again.
func TestChainStartServerLocalFailureLeavesNoBranch(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, fg, _ := chainServerRuntime(t, fr)
	// A member directory that is a file refuses prepareSave inside the mirror
	// transaction, after the server create and the branch.
	if err := os.WriteFile(rt.Store.Dir("shop-rev"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("plant the blocking file: %v", err)
	}

	_, err := ChainStart(context.Background(), rt, chainServerOpts(t))
	if err == nil {
		t.Fatal("ChainStart --server = nil, want the store failure")
	}
	if !strings.Contains(err.Error(), "run the same relevo chain command again") || !strings.Contains(err.Error(), "zen") {
		t.Errorf("err = %q, want the record-it-again message naming zen", err.Error())
	}
	if _, cerr := rt.Store.Chain("shop"); !errors.Is(cerr, store.ErrNotFound) {
		t.Errorf("chain exists after the failed mirror: %v", cerr)
	}
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		if _, lerr := rt.Store.Load(name); !errors.Is(lerr, store.ErrNotFound) {
			t.Errorf("binding %q exists after the failed mirror: %v", name, lerr)
		}
	}
	deleted := false
	for _, c := range fg.deleteBranchCalls {
		if c.Branch == "relevo/shop" {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("DeleteBranch calls = %+v, want relevo/shop removed", fg.deleteBranchCalls)
	}
}

// TestServerChainMirrorNeverAdvancesLocally pins the chainApply guard: a close
// that names a mirror member writes no trace row and leaves the row running.
func TestServerChainMirrorNeverAdvancesLocally(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	b := seedServerChain(t, rt, "shop")

	var next store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		ev := chain.Event{
			Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder,
			Round: b.Round, Outcome: reporttail.OutcomeDone,
		}
		var aerr error
		next, aerr = chainApply(context.Background(), rt, tx, b, ev, nil)
		return aerr
	})
	if err != nil {
		t.Fatalf("chainApply on a mirror member: %v", err)
	}
	if !store.SameBinding(next, b) {
		t.Errorf("chainApply changed the mirror: %+v -> %+v", b, next)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 0 {
		t.Errorf("trace rows = %d, want none for a server chain", len(events))
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("mirror status = %q, want running", row.Status)
	}
}

// TestServerChainMemberSkipsTheBindingCatchUp pins the four per-binding
// guards: the daemon prefetch answers none, the per-binding reconcile changes
// nothing, and the read verbs' sync makes no GetBinding call for a mirror
// member.
func TestServerChainMemberSkipsTheBindingCatchUp(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundRunning, ClosedRound: 1}}
	rt.Remote = fr
	b := seedServerChain(t, rt, "shop")

	if pre := NewDaemon(rt, time.Minute).prefetchRemote(context.Background(), b); pre != nil {
		t.Errorf("prefetchRemote = %+v, want nil for a mirror member", pre)
	}
	var next store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var rerr error
		next, rerr = reconcileRemote(context.Background(), rt, tx, b, nil)
		return rerr
	})
	if err != nil {
		t.Fatalf("reconcileRemote on a mirror member: %v", err)
	}
	if !store.SameBinding(next, b) {
		t.Errorf("reconcileRemote changed the mirror: %+v -> %+v", b, next)
	}
	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "GetBinding") {
			t.Errorf("calls = %v, want no GetBinding for a mirror member", fr.calls)
		}
	}
}

// TestChainSweepAndPendingSendSkipServerChains pins the two chain-level
// guards: a member in NEEDS YOU does not halt a server chain here, and a
// staged builder round is not shipped by this machine.
func TestChainSweepAndPendingSendSkipServerChains(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rt.Remote = &fakeRemote{}
	seedServerChain(t, rt, "shop")
	// The reviewer is NEEDS YOU: the sweep would halt a local chain on it.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		cur.State = store.StateNeedsYou
		cur.Halt = "stuck"
		return tx.Save(cur)
	}); err != nil {
		t.Fatalf("mark the reviewer NEEDS YOU: %v", err)
	}
	// The builder has a staged round the pending-send step would ship.
	stageRemoteRoundFile(t, rt, "shop", 1, "build it")

	tickChains(context.Background(), rt)
	if err := chainSendPending(context.Background(), rt); err != nil {
		t.Fatalf("chainSendPending: %v", err)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 0 {
		t.Errorf("trace rows = %d, want none for a server chain", len(events))
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("mirror status = %q, want running", row.Status)
	}
}
