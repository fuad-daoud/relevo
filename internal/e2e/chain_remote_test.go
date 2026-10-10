package e2e

// TestChainRemoteBuilderE2E is slice 2 round 5's end-to-end pin: a chain whose
// builder is a remote member on the in-process server. Plan 1 ships through the
// pending-send step; the served builder runs (the script runner) and the server
// runs the chain's gate. Round 1's gate exits non-zero, so the chain's red check
// routes to the repair step, whose green check then seeds the local reviewer
// with the check line, the plan's cumulative diff and the round's own prompt.
// The reviewer asks for changes, so the planner's correction plan is shipped to
// the remote builder the same way; the chain finishes. The trace holds every
// transition, the client's relevo/<n> branch holds the builder's commits and
// equals the last pulled result commit, and the MasterMind receives exactly one
// delivery.
//
// The two-runner seam holds in one test: the server runs its served rounds and
// gates through the script runner, while the chain's local readers (reviewer,
// planner) run the fake `claude` on PATH through a real proc runner.
//
// Every wait is bounded and names the step it waited for, so a stuck chain
// fails with its row and its members' notes rather than hanging the run.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

const (
	// chainRemoteName is the chain's name, and its remote builder member's.
	chainRemoteName = "s2r5shop"
	// chainRemoteToken is the candidate the client resolves its members to; it
	// is the same token newServer's candidate set lists, so the server's own
	// candidate mapping is an exact match.
	chainRemoteToken = "claude/anthropic/haiku"
	// chainRemoteLocal is the reserved placement entry that names this machine:
	// the reader rows carry it so both readers stay local.
	chainRemoteLocal = "local"
	// chainRemoteDeadline bounds the whole scenario. Each client tick pays a
	// full bundle fetch, so the bound is generous; it is what turns a stuck
	// chain into a failure that names the step.
	chainRemoteDeadline = 3 * time.Minute
)

// TestChainRemoteBuilderE2E is the served-remote-builder chain scenario.
func TestChainRemoteBuilderE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// -- The same isolation the other e2e scenarios run under ----------------
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("CLAUDECODE", "1")

	ownerCleanup, err := dbtest.InstallOwner()
	if err != nil {
		t.Fatalf("install the in-process owner: %v", err)
	}
	t.Cleanup(ownerCleanup)

	// The fake `claude` first on PATH serves the chain's local readers; the
	// server's own runner is the script runner newServer builds.
	t.Setenv("PATH", writeFakeHarness(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := filepath.Join(home, ".config", "relevo")
	writeChainRemoteCandidatesAndPolicy(t, configDir)

	// -- The in-process server and the client --------------------------------
	srv, url, fp, enroll, srvStore, runner := newServer(t)
	rt, reg, kp := newChainRemoteClient(t, url, fp, home)
	owner := enroll(remote.MarshalPublic(kp.Public, "test client"))

	repo := newRepo(t)
	// relevo.ChainStart cuts the builder's branch and reads the working
	// directory as the repo, so the throwaway repo is where it starts from.
	t.Chdir(repo)

	rec, _, err := mastermind.Init(reg, mastermind.InitInput{
		Kind: "claude", SessionID: "e2e-chain-remote", CWD: repo, Now: rt.Now(),
	})
	if err != nil {
		t.Fatalf("register the mastermind: %v", err)
	}

	// -- Start the chain: the builder is placed on zen -----------------------
	plan := writePlan(t, "chain-remote-plan-1.md", "# Plan one\n\nDo the first thing.\n")
	res, err := relevo.ChainStart(ctx, rt, relevo.ChainOptions{
		Name:          chainRemoteName,
		Plans:         []string{plan},
		Feature:       "chains-s2-r5",
		MasterMindID:  rec.ID,
		ReviewerActor: "reviewer",
		PlannerActor:  "lite-planner",
		Security:      chainBoolPtr(false),
		Gate:          "make check",
		Regate:        chainIntPtr(1),
	})
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}
	if res.Plans != 1 {
		t.Fatalf("chain %s started with %d plans, want 1", chainRemoteName, res.Plans)
	}
	if res.Check != "make check" {
		t.Errorf("chain resolved check = %q, want make check", res.Check)
	}
	builderMember := chainRemoteMember(t, res.Members, chainRemoteName)
	if !builderMember.Builder.Remote() || builderMember.Builder.Server != "zen" {
		t.Fatalf("the builder member is not remote on zen: %+v", builderMember.Builder)
	}

	builderName := chainRemoteName
	reviewerName := chainRemoteName + "-rev"
	plannerName := chainRemoteName + "-plan"

	serverStore := srvStore(owner)
	gitClient := git.NewClient("git", 10*time.Second, 0)
	serverRT := relevo.Runtime{Store: serverStore, Git: gitClient, Runner: runner}

	t.Cleanup(func() {
		stopRecordedBuilders(t, rt, builderName, reviewerName, plannerName)
	})

	// Plan 1 shipped through the start's own pending-send step: the server has
	// the binding and its runner has the served round.
	waitChainRemote(t, 30*time.Second, "plan 1 to reach the served builder", func() bool {
		sb, err := serverStore.Load(builderName)
		if err != nil {
			return false
		}
		_, running := runner.runningSpec()
		return sb.Round == 1 && running
	})

	// -- Drive the choreography ----------------------------------------------
	// The gate's real exit travels: round 1's gate fails (2), rounds 2 and 3's
	// pass (0). Round 1's red check routes to the repair step, which runs round
	// 2; that round's green check seeds the reviewer.
	gateExit := map[int]int{1: 2, 2: 0, 3: 0}
	// checks counts the served checks this loop has scripted, so each is
	// scripted the exit of the round whose check it is.
	checks := 0

	daemon := relevo.NewDaemon(rt, 200*time.Millisecond)
	deadline := time.Now().Add(chainRemoteDeadline)
	for {
		if err := daemon.Tick(ctx); err != nil {
			t.Fatalf("client tick: %v", err)
		}
		if err := srv.Tick(ctx); err != nil {
			t.Fatalf("server tick: %v", err)
		}

		if sb, err := serverStore.Load(builderName); err == nil {
			if spec, ok := runner.runningSpec(); ok {
				switch {
				case isGateSpec(spec):
					// A placed writer's check is its own run on the server with
					// its own log, and it is keyed to the round it follows rather
					// than to the binding's round by the time it lands. So the
					// round's own gate is scripted by that round, and a served
					// check by the round whose check it is: the same exits, in
					// the same order.
					round := sb.Round
					if isServedCheckSpec(spec) {
						checks++
						round = checks
					}
					runner.completeGate(t, spec.LogPath, chainRemoteGateExit(gateExit, round))
				default:
					if _, statErr := os.Stat(serverStore.DonePath(builderName, sb.Round)); statErr != nil {
						finishRound(t, serverRT, builderName, sb.Round, fmt.Sprintf("round %d done", sb.Round))
					}
				}
			}
		}

		c := chainE2ERow(t, rt, chainRemoteName)
		if chain.Status(c.Status) != chain.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chain %s did not finish within %s: status %s, phase %s, step %s, plan %d/%d, awaiting %s round %d, reason %q; members: %s",
				chainRemoteName, chainRemoteDeadline, c.Status, c.Phase, c.Step, c.Plan, c.Plans, c.AwaitingMember, c.AwaitingRound, c.Reason,
				chainMembersNote(t, rt, c))
		}
		time.Sleep(30 * time.Millisecond)
	}

	// -- The chain's own row -------------------------------------------------
	row := chainE2ERow(t, rt, chainRemoteName)
	if row.Status != string(chain.StatusDone) || row.Phase != string(chain.PhaseFinished) {
		t.Fatalf("chain ended status %q phase %q, want done/finished (reason %q)", row.Status, row.Phase, row.Reason)
	}
	if row.Plan != 1 || row.Corrections != 1 {
		t.Errorf("chain ended on plan %d with %d corrections, want plan 1 with the one correction", row.Plan, row.Corrections)
	}

	// -- The trace: one row per transition -----------------------------------
	// The engine writes a row per transition, the red check along with the
	// rest: the red gate routes to the repair step, and the repair's own green
	// check seeds the reviewer. The pattern pins the send sequence per step and
	// member, the one repair the red gate bought, and the verdicts the reviewer
	// read. A check's row carries no round of its own: it names the run it
	// answered.
	want := []chainFlowStep{
		{step: "build", round: 1, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionRunCheck, to: "check", plan: 1},
		{step: "check", round: 0, member: builderName, event: workflow.EventCheckClosed, action: workflow.ActionSend, to: "repair", plan: 1},
		{step: "repair", round: 2, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionRunCheck, to: "check", plan: 1},
		{step: "check", round: 0, member: reviewerName, event: workflow.EventCheckClosed, action: workflow.ActionSend, to: "review", plan: 1},
		{step: "review", round: 1, member: plannerName, event: workflow.EventStepClosed, action: workflow.ActionSend, to: "correct", plan: 1},
		{step: "correct", round: 1, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionSend, to: "build-fix", plan: 1},
		{step: "build-fix", round: 3, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionRunCheck, to: "check", plan: 1},
		{step: "check", round: 0, member: reviewerName, event: workflow.EventCheckClosed, action: workflow.ActionSend, to: "review", plan: 1},
		{step: "review", round: 2, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionFinish, to: "", plan: 1},
	}
	doc, err := relevo.ChainTrace(ctx, rt, chainRemoteName)
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if len(doc.Events) != len(want) {
		t.Fatalf("the trace has %d rows, want %d -- one per transition:\n%s", len(doc.Events), len(want), relevo.RenderTrace(doc))
	}
	for i, step := range want {
		got := doc.Events[i]
		if got.Seq != i+1 {
			t.Errorf("trace row %d has seq %d, want %d", i, got.Seq, i+1)
		}
		if got.Step != step.step {
			t.Errorf("trace row %d step = %q, want %q", i, got.Step, step.step)
		}
		if got.Member != step.member {
			t.Errorf("trace row %d sends to %s, want %s", i, got.Member, step.member)
		}
		if got.Flow == nil || got.Flow.Kind != step.event {
			t.Errorf("trace row %d event = %+v, want %q", i, got.Flow, step.event)
		}
		if got.FlowAction == nil || got.FlowAction.Kind != step.action {
			t.Errorf("trace row %d action = %+v, want %q", i, got.FlowAction, step.action)
		}
		if step.to != "" && got.FlowAction.Step != step.to {
			t.Errorf("trace row %d action sends to step %q, want %q", i, got.FlowAction.Step, step.to)
		}
		if got.Round != step.round {
			t.Errorf("trace row %d round = %d, want %d", i, got.Round, step.round)
		}
		if got.Plan != step.plan {
			t.Errorf("trace row %d plan = %d, want %d: each row keeps the plan it was written on", i, got.Plan, step.plan)
		}
	}
	// The red gate bought exactly one repair step: the red check routes to
	// repair, and no later row names another repair.
	if got := doc.Events[1].FlowAction.Step; got != "repair" {
		t.Errorf("the red check routes to step %q, want repair", got)
	}
	var repairs int
	for _, e := range doc.Events {
		if e.Step == "repair" {
			repairs++
		}
	}
	if repairs != 1 {
		t.Errorf("the trace names %d repair rows, want exactly one: the red gate buys one repair", repairs)
	}
	if got := doc.Events[4].Flow.Outcomes["verdict"]; got != "changes" {
		t.Errorf("the reviewer's first verdict = %q, want changes: the reviewer's first round asks for a correction", got)
	}
	if got := doc.Events[8].Flow.Outcomes["verdict"]; got != "pass" {
		t.Errorf("the reviewer's final verdict = %q, want pass", got)
	}

	// -- The reviewer's seed: the prompts and the round they judge -----------
	// Round 1 of the reviewer judged the builder's green repair round (round
	// 2): its seed names the check line, the round's own diff and prompt, and
	// the plan's cumulative diff. The repair round's diff and the plan's whole
	// diff are sealed round files, so the seed names copies under the chain's
	// own directory -- a path a runner can open, holding the key's bytes.
	rev1 := chainRemoteRead(t, rt.Store, rt.Store.PromptPath(reviewerName, 1))
	planDiffKey := rt.Store.PlanDiffPath(builderName, 2)
	planDiffCopy, ok := rt.Store.ChainInputPath(chainRemoteName, planDiffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", planDiffKey)
	}
	chainInputCopyGone(t, planDiffCopy)
	if want := "Plan diff, every round of this plan so far: " + planDiffCopy + "."; !strings.Contains(rev1, want) {
		t.Errorf("the reviewer's round 1 seed does not name the cumulative plan diff copy %s:\n%s", planDiffCopy, rev1)
	}
	if patch := chainRemoteRead(t, rt.Store, planDiffKey); patch == "" {
		t.Errorf("the plan's cumulative diff at %s is empty", planDiffKey)
	}

	diffKey := rt.Store.DiffPath(builderName, 2)
	diffCopy, ok := rt.Store.ChainInputPath(chainRemoteName, diffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", diffKey)
	}
	chainInputCopyGone(t, diffCopy)
	if want := "This round's diff: " + diffCopy + "."; !strings.Contains(rev1, want) {
		t.Errorf("the reviewer's round 1 seed does not name the repair round's diff copy %s:\n%s", diffCopy, rev1)
	}

	if want := "This round's prompt: " + chainSeedCopy(t, rt, chainRemoteName, rt.Store.PromptPath(builderName, 2)); !strings.Contains(rev1, want) {
		t.Errorf("the reviewer's round 1 seed does not carry %q:\n%s", want, rev1)
	}

	// The check line names the gate log's copy, which a seal cannot remove
	// while the reviewer reads it, and which went with the chain's other inputs
	// when the chain ended.
	const gateLine = "Check result: green; its output: "
	if i := strings.Index(rev1, gateLine); i < 0 {
		t.Errorf("the reviewer's round 1 seed does not carry the green check line:\n%s", rev1)
	} else {
		namedGate := strings.TrimSuffix(strings.SplitN(rev1[i+len(gateLine):], "\n", 2)[0], ".")
		if want := chainSeedCopy(t, rt, chainRemoteName, rt.Store.GateLogPath(builderName, 2)); namedGate != want {
			t.Errorf("the seed's gate log = %s, want its copy %s", namedGate, want)
		}
		chainInputCopyGone(t, namedGate)
	}

	// Round 2 of the reviewer judged the builder's correction round (round 3):
	// its seed names the cumulative plan diff copy and the round's own prompt.
	rev2 := chainRemoteRead(t, rt.Store, rt.Store.PromptPath(reviewerName, 2))
	planDiffKey2 := rt.Store.PlanDiffPath(builderName, 3)
	planDiffCopy2, ok := rt.Store.ChainInputPath(chainRemoteName, planDiffKey2)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", planDiffKey2)
	}
	chainInputCopyGone(t, planDiffCopy2)
	if want := "Plan diff, every round of this plan so far: " + planDiffCopy2 + "."; !strings.Contains(rev2, want) {
		t.Errorf("the reviewer's round 2 seed does not name the cumulative plan diff copy %s:\n%s", planDiffCopy2, rev2)
	}
	if want := "This round's prompt: " + chainSeedCopy(t, rt, chainRemoteName, rt.Store.PromptPath(builderName, 3)); !strings.Contains(rev2, want) {
		t.Errorf("the reviewer's round 2 seed does not carry %q:\n%s", want, rev2)
	}

	// -- The correction plan reaches the remote builder ----------------------
	// The builder's round 3 staged prompt is byte-identical to the planner's
	// plan artifact, and so is the server's own staged plan for that round: the
	// pending-send step shipped exactly the planner's text.
	plannerPlan := chainRemotePlannerPlan(t, rt, plannerName, 1)
	if got := chainRemoteRead(t, rt.Store, rt.Store.PromptPath(builderName, 3)); got != plannerPlan {
		t.Errorf("the builder's round 3 staged prompt is not the planner's plan artifact:\ngot:\n%s\nwant:\n%s", got, plannerPlan)
	}
	if got := chainRemoteRead(t, serverStore, serverStore.PromptPath(builderName, 3)); got != plannerPlan {
		t.Errorf("the server's staged plan for round 3 is not the planner's plan artifact:\ngot:\n%s\nwant:\n%s", got, plannerPlan)
	}
	if entries, err := rt.Store.ReadLog(builderName); err != nil {
		t.Fatalf("read the builder's log: %v", err)
	} else if !chainRemotePromptNote(entries, 3, "chain builder") {
		t.Errorf("the builder's round 3 prompt entry does not carry the chain step note: %+v", entries)
	}

	// -- The branch, the pulled files and the delivery -----------------------
	branch := "refs/heads/relevo/" + chainRemoteName
	branchSHA, ok, err := rt.Git.RefSHA(ctx, repo, branch)
	if err != nil || !ok {
		t.Fatalf("resolve %s: sha %q ok %v err %v", branch, branchSHA, ok, err)
	}
	cb := loadBinding(t, rt, builderName)
	if cb.RoundClosedTree == "" || cb.RoundClosedTree != branchSHA {
		t.Errorf("the builder's RoundClosedTree = %q, want the branch's %s", cb.RoundClosedTree, branchSHA)
	}
	sv, err := rt.Remote.GetBinding(ctx, "zen", builderName)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if sv.ClosedRound != 3 || sv.GateResult != "pass" {
		t.Errorf("the server view = closed round %d gate %q, want round 3 passing", sv.ClosedRound, sv.GateResult)
	}
	// The last pulled result commit: the server's own record of the last closed
	// round's head, which the client's branch was absorbed to. The live view
	// omits ResultCommit once the round is acked (RoundState is no longer
	// closed), so the server's stored binding is the durable record.
	serverBinding, err := serverStore.Load(builderName)
	if err != nil {
		t.Fatalf("load the server binding: %v", err)
	}
	if serverBinding.Serve == nil || serverBinding.Serve.ClosedRound != 3 || serverBinding.Serve.ResultCommit == "" {
		t.Fatalf("the server has no closed round 3 result: %+v", serverBinding.Serve)
	}
	if last := serverBinding.Serve.ResultCommit; branchSHA != last || cb.RoundClosedTree != last {
		t.Errorf("the branch %s is at %s and RoundClosedTree %s, want the last pulled result commit %s", branch, branchSHA, cb.RoundClosedTree, last)
	}
	// The pulled files: the last round's report, diff and gate log, and the
	// plan's cumulative diff, all reachable through the store (a sealed round
	// is read from its row).
	for _, p := range []string{
		rt.Store.ReportPath(builderName, 3),
		rt.Store.DiffPath(builderName, 3),
		rt.Store.GateLogPath(builderName, 3),
		rt.Store.PlanDiffPath(builderName, 2),
	} {
		if _, err := rt.Store.ReadFile(p); err != nil {
			t.Errorf("pulled file %s is not readable: %v", p, err)
		}
	}

	// Exactly one KindChain end delivery exists, and it is on the builder (the
	// first member whose record survives).
	entry, found, err := rt.Store.PendingForMasterMind(builderName)
	if err != nil {
		t.Fatalf("PendingForMasterMind(%s): %v", builderName, err)
	}
	if !found || entry.Kind != store.KindChain {
		t.Fatalf("pending on the builder = %+v (found %v), want the chain's one KindChain entry", entry, found)
	}
	if !strings.Contains(entry.Payload, "chain "+chainRemoteName+" finished") {
		t.Errorf("the end delivery payload = %q, want the chain's finished line", entry.Payload)
	}
	waited, err := relevo.WaitChain(ctx, rt, chainRemoteName, time.Minute, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if waited.Code != relevo.WaitClosed {
		t.Fatalf("WaitChain code = %d, want %d: a finished chain is exit 0", waited.Code, relevo.WaitClosed)
	}
	if waited.Payload == "" {
		t.Fatalf("WaitChain returned no payload; the chain's one end delivery must be pulled")
	}
	again, err := relevo.WaitChain(ctx, rt, chainRemoteName, time.Minute, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("second WaitChain: %v", err)
	}
	if again.Payload != "" {
		t.Errorf("a second WaitChain pulled %q; the chain has exactly one end delivery", again.Payload)
	}
}

// writeChainRemoteCandidatesAndPolicy writes the config the client's members
// resolve against: one candidate whose token is the server's own, and an empty
// policy (the file-mode roles registry names the candidates).
func writeChainRemoteCandidatesAndPolicy(t *testing.T, configDir string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	candidates := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder","reviewer","researcher"]}]`
	pol := `{}`
	for name, body := range map[string]string{"candidates.json": candidates, "policy.json": pol} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// newChainRemoteClient assembles the client runtime the scenario needs: the
// production remote wiring newClient builds (store, remote client, bundle
// transport, mastermind registry) plus the local runner and roles registry
// newHeadlessRuntime wires, with an explicit roles file whose builder row is
// placed on zen and whose reader rows are local.
func newChainRemoteClient(t *testing.T, url, fingerprint, home string) (relevo.Runtime, *mastermind.DBRegistry, remote.Keypair) {
	t.Helper()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote.Generate: %v", err)
	}
	servers := remote.Servers{"zen": remote.ServerEntry{URL: url, Fingerprint: fingerprint}}

	configDir := filepath.Join(home, ".config", "relevo")
	candidates, err := candidate.Load(filepath.Join(configDir, "candidates.json"))
	if err != nil {
		t.Fatalf("load candidates: %v", err)
	}
	pol, err := policy.Load(filepath.Join(configDir, "policy.json"))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}

	root := filepath.Join(home, ".local", "state", "relevo")
	st := store.New(root)
	gitClient := git.NewClient("git", 10*time.Second, 0)
	mdb, err := st.DB()
	if err != nil {
		t.Fatalf("open store db: %v", err)
	}
	t.Cleanup(func() { _ = mdb.Close() })
	reg := &mastermind.DBRegistry{KV: db.TxKV{DB: mdb}, Now: time.Now}

	reader := "reader"
	rows := map[string]roles.Row{
		"builder": {Candidates: []string{chainRemoteToken}, Placement: []string{"zen"}},
		"reviewer": {
			Candidates: []string{chainRemoteToken},
			Placement:  []string{chainRemoteLocal},
			Shape:      &reader,
		},
		"lite-planner": {
			Candidates:  []string{chainRemoteToken},
			Placement:   []string{chainRemoteLocal},
			Shape:       &reader,
			Definitions: map[string]roles.DefRow{"claude": {Agent: "architect"}},
		},
		"security": {
			Candidates:  []string{chainRemoteToken},
			Placement:   []string{chainRemoteLocal},
			Shape:       &reader,
			Definitions: map[string]roles.DefRow{"claude": {Agent: "security-reviewer"}},
		},
	}
	registry, err := roles.Build(&roles.File{Rows: rows}, candidates, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	rt := relevo.Runtime{
		Git:         gitClient,
		Runner:      proc.New(),
		Store:       st,
		Candidates:  candidates,
		Policy:      pol,
		Registry:    registry,
		Gates:       mdb,
		Latency:     mdb,
		Now:         time.Now,
		Channels:    &delivery.KVClaims{KV: db.TxKV{DB: mdb}},
		MasterMinds: reg,
		ProcStart:   procStartUnix,
		Remote:      client.New(servers, kp, time.Now),
		Transport:   remote.NewBundleTransport(gitClient, t.TempDir()),
	}
	return rt, reg, kp
}

// chainIntPtr is a *int for the options whose nil means "take the policy".
func chainIntPtr(v int) *int { return &v }

// chainRemoteGateExit maps a served round to its scripted gate exit: round 1
// fails (2), rounds 2 and 3 pass (0). A round not named reads as a passing gate.
func chainRemoteGateExit(exits map[int]int, round int) int {
	if code, ok := exits[round]; ok {
		return code
	}
	return 0
}

// chainRemoteMember names one member binding from a chain result.
func chainRemoteMember(t *testing.T, members []store.Binding, name string) store.Binding {
	t.Helper()
	for _, m := range members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("chain member %s not found in %+v", name, members)
	return store.Binding{}
}

// chainRemoteRead reads a staged or sealed file through a store, failing the
// test when it cannot.
func chainRemoteRead(t *testing.T, st *store.Store, path string) string {
	t.Helper()
	body, err := st.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// chainRemotePlannerPlan reads the plan artifact the planning member wrote for
// the round that closed: the body of the planner's report entry for round.
func chainRemotePlannerPlan(t *testing.T, rt relevo.Runtime, planner string, round int) string {
	t.Helper()
	entries, err := rt.Store.ReadLog(planner)
	if err != nil {
		t.Fatalf("read log %s: %v", planner, err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round && e.Path != "" {
			return chainRemoteRead(t, rt.Store, e.Path)
		}
	}
	t.Fatalf("planner %s has no plan artifact for round %d", planner, round)
	return ""
}

// chainRemotePromptNote reports whether a member holds a to-builder prompt
// entry for round carrying note.
func chainRemotePromptNote(entries []store.LogEntry, round int, note string) bool {
	for _, e := range entries {
		if e.Direction == store.DirToBuilder && e.Round == round && e.Note == note {
			return true
		}
	}
	return false
}

// waitChainRemote polls pred, bounded, with a failure message naming the step.
func waitChainRemote(t *testing.T, timeout time.Duration, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if pred() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
