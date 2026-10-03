package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
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

// TestChainStartServerRetypesAnUnknownActor pins the create refusal's class: a
// server's 400 unknown_actor is rebuilt as ErrUnknownRole, so a bad actor is
// the same policy refusal a local start gives -- never an internal failure.
func TestChainStartServerRetypesAnUnknownActor(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	fr.createChainErr = &client.HTTPError{
		Status: http.StatusBadRequest,
		Body: remote.ErrorBody{
			Code:    remote.CodeUnknownActor,
			Message: `unknown actor "ghost" (known: [builder reviewer]): unknown actor`,
		},
	}
	rt, fg, _ := chainServerRuntime(t, fr)

	_, err := ChainStart(context.Background(), rt, chainServerOpts(t))
	if err == nil {
		t.Fatal("ChainStart --server = nil, want the unknown-actor refusal")
	}
	if !errors.Is(err, ErrUnknownRole) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownRole)", err)
	}
	if !strings.Contains(err.Error(), `unknown actor "ghost"`) {
		t.Errorf("err = %q, want the server's own message kept", err)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Errorf("a refused create must cut no local branch: %v", fg.createBranchCalls)
	}
	assertNothingCreated(t, rt, fg, "shop")
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

// TestChainStartServerPlanCopyFailureLeavesNothing pins the plan copies' order:
// they land before the mirror row, so a copy that cannot be written leaves no
// row and no branch and asks for the same command again.
func TestChainStartServerPlanCopyFailureLeavesNothing(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, fg, _ := chainServerRuntime(t, fr)
	// A file where the chain's own plan directory belongs refuses the copy.
	chainDir := rt.Store.ChainDir("shop")
	if err := os.MkdirAll(filepath.Dir(chainDir), 0o755); err != nil {
		t.Fatalf("mkdir the chains directory: %v", err)
	}
	if err := os.WriteFile(chainDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("plant the blocking file: %v", err)
	}

	_, err := ChainStart(context.Background(), rt, chainServerOpts(t))
	if err == nil {
		t.Fatal("ChainStart with an impossible plan copy = nil, want the record-it-again failure")
	}
	if !strings.Contains(err.Error(), "run the same relevo chain command again") || !strings.Contains(err.Error(), "zen") {
		t.Errorf("err = %q, want the record-it-again message naming zen", err.Error())
	}
	if _, cerr := rt.Store.Chain("shop"); !errors.Is(cerr, store.ErrNotFound) {
		t.Errorf("chain exists after a failed plan copy: %v", cerr)
	}
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		if _, lerr := rt.Store.Load(name); !errors.Is(lerr, store.ErrNotFound) {
			t.Errorf("binding %q exists after a failed plan copy: %v", name, lerr)
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
		var aerr error
		next, aerr = chainApply(context.Background(), rt, tx, b, nil)
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

// TestServerChainMemberObservesTheLiveRound pins the observe half of the
// split: the daemon prefetch and the read verbs' sync both fetch and apply a
// member's live view -- status, live facts and the mirrored builder log -- so
// the cockpit shows the running round the server drives.
func TestServerChainMemberObservesTheLiveRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	const logBody = "builder log line 1\n"
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			Name: "shop", Round: 1, RoundState: remote.RoundRunning,
			Live: &remote.LiveView{
				Tail: []string{"building the plan"},
				Diff: &remote.DiffStat{Files: 1, Added: 2, Removed: 0},
			},
		},
		roundFileFromFunc: func(context.Context, string, string, int, string, int64) (io.ReadCloser, remote.FileRange, error) {
			return io.NopCloser(strings.NewReader(logBody)), remote.FileRange{}, nil
		},
	}
	rt.Remote = fr
	b := seedServerChain(t, rt, "shop")

	// The read verbs' pass observes the live view on its own, from the seed.
	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	assertServerChainMemberLive(t, rt, logBody)

	// The daemon's tick observes it too: prefetch unlocked, apply and save
	// under the lock.
	pre := NewDaemon(rt, time.Minute).prefetchRemote(context.Background(), b)
	if pre == nil {
		t.Fatal("prefetchRemote = nil, want a live fetch for a member")
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		next, rerr := reconcileRemote(context.Background(), rt, tx, b, pre)
		if rerr != nil {
			return rerr
		}
		return tx.Save(next)
	}); err != nil {
		t.Fatalf("reconcileRemote on a member: %v", err)
	}
	assertServerChainMemberLive(t, rt, logBody)

	fetched := false
	for _, c := range fr.calls {
		if c == "GetBinding:zen:shop" {
			fetched = true
		}
	}
	if !fetched {
		t.Errorf("calls = %v, want GetBinding:zen:shop", fr.calls)
	}
}

// assertServerChainMemberLive asserts the stored builder member carries the
// observed live round: its status word, its live facts and its mirrored log.
func assertServerChainMemberLive(t *testing.T, rt Runtime, logBody string) {
	t.Helper()
	b := chainBinding(t, rt, "shop")
	if b.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Errorf("RemoteStatus = %q, want %q", b.Builder.RemoteStatus, remote.RoundRunning)
	}
	if b.Builder.RemoteLive == nil {
		t.Fatal("RemoteLive = nil, want the live facts")
	}
	if got := b.Builder.RemoteLive.Tail; len(got) != 1 || got[0] != "building the plan" {
		t.Errorf("RemoteLive.Tail = %v, want [building the plan]", got)
	}
	body, err := rt.Store.ReadFile(rt.Store.BuilderLogPath("shop", 1))
	if err != nil {
		t.Fatalf("read mirrored log: %v", err)
	}
	if string(body) != logBody {
		t.Errorf("mirrored log = %q, want %q", body, logBody)
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

// TestServerChainRefusesCustomWorkflowWithoutFeature pins that a custom
// workflow on a server lacking the workflow feature is refused with the server
// and feature named, before CreateChain is called.
func TestServerChainRefusesCustomWorkflowWithoutFeature(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, fg, _ := chainServerRuntime(t, fr)
	const customYAML = `name: custom-flow
inputs:
  plans: required
start: b
steps:
  b:
    run: builder
    on:
      done: done
`
	wfPath := filepath.Join(t.TempDir(), "custom.yaml")
	if err := os.WriteFile(wfPath, []byte(customYAML), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	opts := chainServerOpts(t)
	opts.Workflow = wfPath

	_, err := ChainStart(context.Background(), rt, opts)
	if err == nil {
		t.Fatal("ChainStart = nil, want refusal for server missing workflow feature")
	}
	wantMsg := fmt.Sprintf("server zen cannot run workflow %s (missing the \"workflow\" feature)", wfPath)
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), wantMsg)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateChain") {
			t.Errorf("CreateChain called: %v, want it refused beforehand", fr.calls)
		}
	}
	assertNothingCreated(t, rt, fg, "shop")
}

// TestServerChainOldServerRefusesTakenMemberName pins that the name-collision
// guard is not lost on the old-server path: without the workflow feature the
// client derives the member names itself, so a member name already bound
// locally still refuses the start before any create.
func TestServerChainOldServerRefusesTakenMemberName(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, fg, _ := chainServerRuntime(t, fr)
	if err := rt.Store.Save(store.Binding{Name: "shop-rev", CWD: "/taken", State: store.StateActive}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	opts := chainServerOpts(t)
	_, err := ChainStart(context.Background(), rt, opts)
	if err == nil || !strings.Contains(err.Error(), `"shop-rev" already exists`) {
		t.Fatalf("err = %v, want a taken-name refusal", err)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want errors.Is(err, ErrRefused): a taken name is refused, not internal", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateChain") {
			t.Errorf("CreateChain called: %v, want it refused beforehand", fr.calls)
		}
	}
	assertNothingCreated(t, rt, fg, "shop")
}

// TestServerChainDefaultOnOldServerSendsSettings pins the new-client/old-server
// compatibility: when the server lacks the workflow feature, the default
// workflow sends settings and client binding ids.
func TestServerChainDefaultOnOldServerSendsSettings(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, _, ft := chainServerRuntime(t, fr)
	ft.snapshotResp = remote.Snapshot{
		Heads: map[string]string{"refs/relevo/shop/out": seedRemoteBase},
		Body:  io.NopCloser(strings.NewReader("bundle-bytes")),
	}

	opts := chainServerOpts(t)
	res, err := ChainStart(context.Background(), rt, opts)
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}

	req := fr.createChainReq
	if len(req.Workflow) != 0 {
		t.Errorf("create Workflow = %s, want empty for old server", req.Workflow)
	}
	if req.Settings.ReviewerActor != "reviewer" || req.Settings.MaxCorrections != 3 {
		t.Errorf("create Settings = %+v, want resolved settings", req.Settings)
	}
	if len(req.ClientBindingIDs) != 3 {
		t.Errorf("ClientBindingIDs = %v, want 3 entries", req.ClientBindingIDs)
	}
	if len(req.ClientActorIDs) != 0 {
		t.Errorf("ClientActorIDs = %v, want empty", req.ClientActorIDs)
	}
	if res.Chain.Name != "shop" {
		t.Errorf("Chain.Name = %q, want shop", res.Chain.Name)
	}
}

// TestServerChainSendsWorkflowWhenFeaturePresent pins the new-client/new-server
// path: when the server advertises the workflow feature, the client sends the
// workflow definition and actor IDs, omitting settings.
func TestServerChainSendsWorkflowWhenFeaturePresent(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureWorkflow)
	rt, _, ft := chainServerRuntime(t, fr)
	ft.snapshotResp = remote.Snapshot{
		Heads: map[string]string{"refs/relevo/shop/out": seedRemoteBase},
		Body:  io.NopCloser(strings.NewReader("bundle-bytes")),
	}

	opts := chainServerOpts(t)
	res, err := ChainStart(context.Background(), rt, opts)
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}

	req := fr.createChainReq
	if len(req.Workflow) == 0 {
		t.Fatal("create Workflow is empty, want workflow definition")
	}
	def, err := workflow.Parse(req.Workflow)
	if err != nil {
		t.Fatalf("parse Workflow: %v", err)
	}
	if def.Name != "default" {
		t.Errorf("Workflow name = %q, want default", def.Name)
	}
	if req.Settings != (remote.ChainSettings{}) {
		t.Errorf("Settings = %+v, want empty zero value", req.Settings)
	}
	if len(req.ClientBindingIDs) != 0 {
		t.Errorf("ClientBindingIDs = %v, want empty", req.ClientBindingIDs)
	}
	used := workflow.UsedActors(def)
	if len(req.ClientActorIDs) != len(used) {
		t.Errorf("ClientActorIDs len = %d (%v), want %d (%v)", len(req.ClientActorIDs), req.ClientActorIDs, len(used), used)
	}
	for _, a := range used {
		if id, ok := req.ClientActorIDs[a]; !ok || id == "" {
			t.Errorf("missing ClientActorID for actor %s", a)
		}
	}
	if res.Chain.Name != "shop" {
		t.Errorf("Chain.Name = %q, want shop", res.Chain.Name)
	}
}

// TestMirrorTakesWorkflowAndStateFromView pins that a view carrying workflow
// and engine state is recorded verbatim on the mirror row, without invoking
// workflow.FromLegacy.
func TestMirrorTakesWorkflowAndStateFromView(t *testing.T) {
	t.Parallel()

	customWF := []byte(`{"name":"custom-wf","steps":{"special_step":{"run":"builder"}}}`)
	customState := []byte(`{"step":"special_step_unreachable_by_legacy","status":"running"}`)

	v := chainServerView("shop", seedRemoteBase)
	v.Workflow = customWF
	v.State = customState

	opts := ChainOptions{Name: "shop", Feature: "auth"}
	plan := chainServerPlan{base: seedRemoteBase, repo: "/repo", server: "zen"}
	paths := []string{"/plan1"}

	row, err := chainMirrorRow(opts, plan, v, paths, time.Now())
	if err != nil {
		t.Fatalf("chainMirrorRow: %v", err)
	}
	if !bytes.Equal(row.WorkflowJSON, customWF) {
		t.Errorf("WorkflowJSON = %s, want %s", row.WorkflowJSON, customWF)
	}
	if !bytes.Equal(row.StateJSON, customState) {
		t.Errorf("StateJSON = %s, want %s", row.StateJSON, customState)
	}

	baseRow := db.ChainRow{Name: "shop"}
	pulled, err := chainRowFromView(baseRow, v, time.Now())
	if err != nil {
		t.Fatalf("chainRowFromView: %v", err)
	}
	if !bytes.Equal(pulled.WorkflowJSON, customWF) {
		t.Errorf("chainRowFromView WorkflowJSON = %s, want %s", pulled.WorkflowJSON, customWF)
	}
	if !bytes.Equal(pulled.StateJSON, customState) {
		t.Errorf("chainRowFromView StateJSON = %s, want %s", pulled.StateJSON, customState)
	}
}

// TestChainRowFromViewFallsBackToLegacy pins that a server view without workflow
// or state falls back to converting the row via workflow.FromLegacy.
func TestChainRowFromViewFallsBackToLegacy(t *testing.T) {
	t.Parallel()

	v := chainServerView("shop", seedRemoteBase)
	v.Workflow = nil
	v.State = nil

	paths, err := json.Marshal([]string{"/path/to/plan1"})
	if err != nil {
		t.Fatalf("marshal plan paths: %v", err)
	}
	settings, err := json.Marshal(chain.Settings{ReviewerActor: "reviewer", MaxCorrections: 3})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	baseRow := db.ChainRow{
		Name:          "shop",
		Status:        string(chain.StatusRunning),
		Phase:         string(chain.PhaseBuild),
		Step:          string(chain.StepBuilding),
		Plan:          1,
		Plans:         1,
		PlanPathsJSON: paths,
		SettingsJSON:  settings,
	}

	pulled, err := chainRowFromView(baseRow, v, time.Now())
	if err != nil {
		t.Fatalf("chainRowFromView: %v", err)
	}
	if len(pulled.WorkflowJSON) == 0 {
		t.Fatal("WorkflowJSON is empty, want fallback default definition")
	}
	if len(pulled.StateJSON) == 0 {
		t.Fatal("StateJSON is empty, want fallback engine state")
	}
	def, err := workflow.Parse(pulled.WorkflowJSON)
	if err != nil {
		t.Fatalf("parse WorkflowJSON: %v", err)
	}
	if def.Name != "default" {
		t.Errorf("Workflow name = %q, want default", def.Name)
	}
}

// TestServerChainMembersComeFromView pins that the mirror's member bindings
// come authoritatively from view.Members, rather than a client-side plan.
func TestServerChainMembersComeFromView(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureWorkflow)
	fr.createChainResp = remote.ChainView{
		Name: "shop", Status: string(chain.StatusRunning),
		Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
		Plan: 1, Plans: 1, AwaitingMember: "shop-lead", AwaitingRound: 1,
		Base: seedRemoteBase, Branch: "relevo/shop", PlanStartCommit: seedRemoteBase,
		Workflow: []byte(`{"name":"custom"}`),
		State:    []byte(`{"step":"lead"}`),
		Members: []remote.ChainMemberView{
			{
				Part: "lead", Name: "shop-lead", Actor: "leader",
				View: remote.BindingView{
					Name: "shop-lead", Candidate: testClaudeRef, Tier: "harness",
					Shape: store.ShapeWriter, State: string(store.StateActive), Round: 1,
				},
			},
			{
				Part: "tester", Name: "shop-tester", Actor: "qa",
				View: remote.BindingView{
					Name: "shop-tester", Candidate: testClaudeRef, Tier: "harness",
					Shape: store.ShapeReader, State: string(store.StateActive), Round: 1,
				},
			},
		},
	}

	rt, _, ft := chainServerRuntime(t, fr)
	ft.snapshotResp = remote.Snapshot{
		Heads: map[string]string{"refs/relevo/shop/out": seedRemoteBase},
		Body:  io.NopCloser(strings.NewReader("bundle-bytes")),
	}

	opts := chainServerOpts(t)
	res, err := ChainStart(context.Background(), rt, opts)
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}

	if len(res.Members) != 2 {
		t.Fatalf("len(res.Members) = %d, want 2 members from view", len(res.Members))
	}
	lead := memberByName(t, res.Members, "shop-lead")
	if lead.Role != "leader" || lead.Shape != store.ShapeWriter || lead.Branch != "relevo/shop-lead" {
		t.Errorf("lead = %+v, want leader writer on relevo/shop-lead", lead)
	}
	tester := memberByName(t, res.Members, "shop-tester")
	if tester.Role != "qa" || tester.Shape != store.ShapeReader || tester.Branch != "" {
		t.Errorf("tester = %+v, want qa reader branchless", tester)
	}
	if _, err := rt.Store.Load("shop-lead"); err != nil {
		t.Errorf("load shop-lead from store: %v", err)
	}
	if _, err := rt.Store.Load("shop-tester"); err != nil {
		t.Errorf("load shop-tester from store: %v", err)
	}
}
