package e2e

// TestChainServerE2E is slice 3 round 6's end-to-end pin: a chain started with
// --server runs entirely on the in-process server, with the client's daemon
// never ticked after ChainStart returns. Two plans, no security phase and a
// passing gate: plan 1's first review asks for changes, so the planner's
// correction runs, then the reviewer passes plan 2 and the chain finishes. One
// later relevo.SyncRemote pulls every member's rounds in order with the
// builder's commits, and the MasterMind gets exactly one delivery.
//
// The server drives every round. The test's script runner holds each served
// process; the test completes it through finishRound (a builder), completeGate
// (the gate) or finishServedReader (a reader), the three shapes a served chain
// member takes.

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
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

const (
	// chainServerName is the chain's name, and its builder member's.
	chainServerName = "s3r6shop"
	// chainServerToken is the one candidate both registries resolve to, so the
	// server's own pick is an exact match for the client's.
	chainServerToken = "claude/anthropic/haiku"
	// chainServerDeadline bounds the server-only loop. It is what turns a
	// stuck chain into a failure that names the step.
	chainServerDeadline = 3 * time.Minute
	// chainServerPlanText is the correction plan the chain's planner writes:
	// the bytes the builder's round 2 prompt must equal.
	chainServerPlanText = "# Correction plan\n\nDo it the other way.\n"
)

func TestChainServerE2E(t *testing.T) {
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

	t.Setenv("PATH", writeFakeHarness(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := filepath.Join(home, ".config", "relevo")
	writeChainRemoteCandidatesAndPolicy(t, configDir)

	// -- The in-process server and the client --------------------------------
	// The server's registry knows the reader actors the chain names: the
	// shipped reviewer and a lite-planner whose claude definition is architect.
	srv, url, fp, enroll, srvStore, runner := newServerWithContext(t, ctx, cancel, policy.Policy{}, func(c *serve.Config) {
		c.Registry = newServerChainRegistry(t, c.Candidates, c.Policy)
	})
	rt, reg, kp := newChainRemoteClient(t, url, fp, home)
	owner := enroll(remote.MarshalPublic(kp.Public, "test client"))

	repo := newRepo(t)
	t.Chdir(repo)

	rec, _, err := mastermind.Init(reg, mastermind.InitInput{
		Kind: "claude", SessionID: "e2e-chain-server", CWD: repo, Now: rt.Now(),
	})
	if err != nil {
		t.Fatalf("register the mastermind: %v", err)
	}

	builderName := chainServerName
	reviewerName := chainServerName + "-rev"
	plannerName := chainServerName + "-plan"
	serverStore := srvStore(owner)
	gitClient := rt.Git
	serverRT := relevo.Runtime{Store: serverStore, Git: gitClient, Runner: runner}

	t.Cleanup(func() {
		stopRecordedBuilders(t, rt, builderName, reviewerName, plannerName)
	})

	// -- 1. Start: the whole chain is placed on zen ---------------------------
	plans := []string{
		writePlan(t, "chain-server-plan-1.md", "# Plan one\n\nDo the first thing.\n"),
		writePlan(t, "chain-server-plan-2.md", "# Plan two\n\nDo the second thing.\n"),
	}
	res, err := relevo.ChainStart(ctx, rt, relevo.ChainOptions{
		Name:           chainServerName,
		Plans:          plans,
		Feature:        "chains-s3-r6",
		MasterMindID:   rec.ID,
		ReviewerActor:  "reviewer",
		PlannerActor:   "lite-planner",
		Security:       chainBoolPtr(false),
		Gate:           "make check",
		Regate:         chainIntPtr(0),
		MaxCorrections: chainIntPtr(1),
		Server:         "zen",
	})
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}
	if res.Chain.Server != "zen" {
		t.Fatalf("mirror row server = %q, want zen", res.Chain.Server)
	}
	if chain.Status(res.Chain.Status) != chain.StatusRunning {
		t.Fatalf("mirror row status = %q, want running", res.Chain.Status)
	}
	if res.Plans != 2 {
		t.Fatalf("chain started with %d plans, want 2", res.Plans)
	}

	serverRow := chainE2ERow(t, serverRT, chainServerName)
	if serverRow.Builder != builderName || serverRow.Reviewer != reviewerName || serverRow.Planner != plannerName {
		t.Fatalf("server row members = %s/%s/%s, want %s/%s/%s",
			serverRow.Builder, serverRow.Reviewer, serverRow.Planner, builderName, reviewerName, plannerName)
	}
	sb, err := serverStore.Load(builderName)
	if err != nil {
		t.Fatalf("load the served builder: %v", err)
	}
	if sb.Worktree == "" {
		t.Fatalf("the served builder has no worktree: %+v", sb)
	}
	for _, name := range []string{reviewerName, plannerName} {
		m, err := serverStore.Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if m.CWD != sb.Worktree {
			t.Errorf("%s CWD = %q, want the builder's worktree %q (a reader shares the builder's tree)", name, m.CWD, sb.Worktree)
		}
		if m.Worktree != "" || m.Branch != "" {
			t.Errorf("%s worktree/branch = %q/%q, want both empty", name, m.Worktree, m.Branch)
		}
	}
	if entries, rerr := serverStore.ReadLog(builderName); rerr == nil {
		switch st := relevo.RoundStateOf(sb, entries); st {
		case remote.RoundQueued, remote.RoundRunning:
		default:
			t.Fatalf("plan 1 round state = %q, want queued or running", st)
		}
	}

	// -- 2. The server drives; the client never ticks -------------------------
	deadline := time.Now().Add(chainServerDeadline)
	for {
		if err := srv.Tick(ctx); err != nil {
			t.Fatalf("server tick: %v", err)
		}
		driveServedChain(t, serverRT, runner, builderName, reviewerName, plannerName)

		row := chainE2ERow(t, serverRT, chainServerName)
		if chain.Status(row.Status) != chain.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chain %s did not finish within %s: status %s, phase %s, step %s, plan %d/%d, awaiting %s round %d, reason %q; members: %s",
				chainServerName, chainServerDeadline, row.Status, row.Phase, row.Step, row.Plan, row.Plans,
				row.AwaitingMember, row.AwaitingRound, row.Reason, chainMembersNote(t, serverRT, row))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The server's own row and trace: every transition, in order, ending at
	// finish.
	serverRow = chainE2ERow(t, serverRT, chainServerName)
	if chain.Status(serverRow.Status) != chain.StatusDone || serverRow.Phase != string(chain.PhaseFinished) {
		t.Fatalf("server chain ended status %q phase %q, want done/finished (reason %q)", serverRow.Status, serverRow.Phase, serverRow.Reason)
	}
	// The pass onto plan 2 resets the correction count, so it reads 0 at the
	// end; the correction is visible as the planner's send in the trace below.
	if serverRow.Plan != 2 {
		t.Errorf("server chain ended on plan %d, want 2: the reviewer's pass advanced it", serverRow.Plan)
	}
	if serverRow.Corrections != 0 {
		t.Errorf("server chain ended with %d corrections, want 0: the plan pass resets the count", serverRow.Corrections)
	}
	want := []chainFlowStep{
		{step: "build", round: 1, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionRunCheck, to: "check", plan: 1},
		{step: "check", round: 0, member: reviewerName, event: workflow.EventCheckClosed, action: workflow.ActionSend, to: "review", plan: 1},
		{step: "review", round: 1, member: plannerName, event: workflow.EventStepClosed, action: workflow.ActionSend, to: "correct", plan: 1},
		{step: "correct", round: 1, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionSend, to: "build-fix", plan: 1},
		{step: "build-fix", round: 2, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionRunCheck, to: "check", plan: 1},
		{step: "check", round: 0, member: reviewerName, event: workflow.EventCheckClosed, action: workflow.ActionSend, to: "review", plan: 1},
		{step: "review", round: 2, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionSend, to: "build", plan: 2},
		{step: "build", round: 3, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionRunCheck, to: "check", plan: 2},
		{step: "check", round: 0, member: reviewerName, event: workflow.EventCheckClosed, action: workflow.ActionSend, to: "review", plan: 2},
		{step: "review", round: 3, member: builderName, event: workflow.EventStepClosed, action: workflow.ActionFinish, to: "", plan: 2},
	}
	serverDoc, err := relevo.ChainTrace(ctx, serverRT, chainServerName)
	if err != nil {
		t.Fatalf("server ChainTrace: %v", err)
	}
	if len(serverDoc.Events) != len(want) {
		t.Fatalf("the server trace has %d rows, want %d:\n%s", len(serverDoc.Events), len(want), relevo.RenderTrace(serverDoc))
	}
	for i, step := range want {
		got := serverDoc.Events[i]
		if got.Seq != i+1 || got.Step != step.step || got.Member != step.member ||
			got.Flow == nil || got.Flow.Kind != step.event || got.FlowAction == nil || got.FlowAction.Kind != step.action {
			t.Errorf("server trace row %d = seq %d step %s member %s event %+v action %+v, want seq %d step %s member %s event %q action %q",
				i, got.Seq, got.Step, got.Member, got.Flow, got.FlowAction, i+1, step.step, step.member, step.event, step.action)
		}
		if step.to != "" && got.FlowAction.Step != step.to {
			t.Errorf("server trace row %d action sends to step %q, want %q", i, got.FlowAction.Step, step.to)
		}
	}
	if got := serverDoc.Events[2].Flow.Outcomes["verdict"]; got != "changes" {
		t.Errorf("the reviewer's first verdict = %q, want changes", got)
	}
	if got := serverDoc.Events[9].Flow.Outcomes["verdict"]; got != "pass" {
		t.Errorf("the reviewer's final verdict = %q, want pass", got)
	}

	// The correction plan reached the builder: its round 2 staged prompt is
	// byte-identical to the planner's plan artifact.
	plannerPlan := chainRemoteRead(t, serverStore, servedReaderOutputPath(serverStore, mustBinding(t, serverStore, plannerName), 1))
	if plannerPlan != chainServerPlanText {
		t.Errorf("the planner's plan artifact = %q, want %q", plannerPlan, chainServerPlanText)
	}
	if got := chainRemoteRead(t, serverStore, serverStore.PromptPath(builderName, 2)); got != plannerPlan {
		t.Errorf("the builder's round 2 staged prompt is not the planner's plan artifact:\ngot:\n%s\nwant:\n%s", got, plannerPlan)
	}

	// Mutation 4: the served sends queue, so admit leaves a "started after ...
	// queued" entry on a reader round. A chain that started served members
	// directly would vanish this.
	if !chainQueueStarted(t, serverStore, reviewerName, 1) {
		t.Errorf("the reviewer's round 1 has no admit ('started after') entry: a served chain send must queue, not start")
	}

	// -- 3. Before the sync: the mirror is running and still at base ----------
	mirror := chainE2ERow(t, rt, chainServerName)
	if chain.Status(mirror.Status) != chain.StatusRunning {
		t.Fatalf("mirror status before the sync = %q, want running", mirror.Status)
	}
	for _, name := range []string{builderName, reviewerName, plannerName} {
		mustNoReport(t, rt.Store, name)
	}
	branchRef := "refs/heads/relevo/" + chainServerName
	if sha, ok, rerr := rt.Git.RefSHA(ctx, repo, branchRef); rerr != nil || !ok || sha != res.Chain.Base {
		t.Errorf("the client branch %s before the sync = (%q, %v, %v), want base %q", branchRef, sha, ok, rerr, res.Chain.Base)
	}

	// -- 4. One sync (in a bounded progress loop) and the pull assertions -----
	syncChainServerMirror(t, ctx, rt, chainServerName)
	post := chainE2ERow(t, rt, chainServerName)
	if chain.Status(post.Status) != chain.StatusDone || post.Phase != string(chain.PhaseFinished) {
		t.Fatalf("mirror after the sync = status %q phase %q, want done/finished (reason %q)", post.Status, post.Phase, post.Reason)
	}

	// The round numbers each member pulled, in order.
	if b := loadBinding(t, rt, builderName); b.Round != 4 {
		t.Errorf("client builder round = %d, want 4: rounds 1, 2 and 3 pulled", b.Round)
	}
	if b := loadBinding(t, rt, reviewerName); b.Round != 4 {
		t.Errorf("client reviewer round = %d, want 4: rounds 1, 2 and 3 pulled", b.Round)
	}
	if b := loadBinding(t, rt, plannerName); b.Round != 2 {
		t.Errorf("client planner round = %d, want 2: round 1 pulled", b.Round)
	}

	// Every installed round's prompt and report entry exists once, in order,
	// and its files are readable.
	for _, name := range []string{builderName, reviewerName, plannerName} {
		b := loadBinding(t, rt, name)
		rounds := pulledRounds(name, builderName, reviewerName, plannerName)
		for _, round := range rounds {
			entries := chainMemberLog(t, rt.Store, name)
			if countLog(entries, round, store.DirToBuilder, store.KindPrompt) != 1 {
				t.Errorf("%s round %d prompt entries = %d, want 1", name, round, countLog(entries, round, store.DirToBuilder, store.KindPrompt))
			}
			if countLog(entries, round, store.DirToMasterMind, store.KindReport) != 1 {
				t.Errorf("%s round %d report entries = %d, want 1", name, round, countLog(entries, round, store.DirToMasterMind, store.KindReport))
			}
			for _, e := range entries {
				if e.Round == round && e.Kind == store.KindReport && !e.Confirmed {
					t.Errorf("%s round %d report entry is not consumed: %+v", name, round, e)
				}
			}
			if _, rerr := rt.Store.ReadFile(rt.Store.PromptPath(name, round)); rerr != nil {
				t.Errorf("%s round %d prompt file: %v", name, round, rerr)
			}
			reportPath := rt.Store.ReportPath(name, round)
			if b.Shape == store.ShapeReader {
				reportPath = servedReaderOutputPath(rt.Store, b, round)
			}
			if _, rerr := rt.Store.ReadFile(reportPath); rerr != nil {
				t.Errorf("%s round %d report file %s: %v", name, round, reportPath, rerr)
			}
		}
	}
	for round := 1; round <= 3; round++ {
		for _, p := range []string{
			rt.Store.ReportPath(builderName, round),
			rt.Store.DiffPath(builderName, round),
			rt.Store.GateLogPath(builderName, round),
		} {
			if _, rerr := rt.Store.ReadFile(p); rerr != nil {
				t.Errorf("builder round %d pulled file %s is not readable: %v", round, p, rerr)
			}
		}
	}

	// The client branch equals the server's builder branch head and holds the
	// three round commits.
	serverSHA, ok, err := gitClient.RefSHA(ctx, sb.Serve.BareRepo, branchRef)
	if err != nil || !ok {
		t.Fatalf("resolve the server branch %s: sha %q ok %v err %v", branchRef, serverSHA, ok, err)
	}
	clientSHA, ok, err := rt.Git.RefSHA(ctx, repo, branchRef)
	if err != nil || !ok {
		t.Fatalf("resolve the client branch %s: sha %q ok %v err %v", branchRef, clientSHA, ok, err)
	}
	if clientSHA != serverSHA {
		t.Errorf("the client branch %s = %s, want the server's %s", branchRef, clientSHA, serverSHA)
	}
	if n := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", res.Chain.Base+".."+branchRef)); n != "3" {
		t.Errorf("the client branch holds %s commits over base, want 3", n)
	}
	logText := runGit(t, repo, "log", "--format=%s", res.Chain.Base+".."+branchRef)
	for round := 1; round <= 3; round++ {
		if !strings.Contains(logText, fmt.Sprintf("chain round %d", round)) {
			t.Errorf("the client branch log does not carry the round %d commit:\n%s", round, logText)
		}
	}

	// Every server member is fully acked.
	for _, name := range []string{builderName, reviewerName, plannerName} {
		m, err := serverStore.Load(name)
		if err != nil {
			t.Fatalf("load server %s: %v", name, err)
		}
		if m.Serve == nil || m.Serve.AckedRound != m.Serve.ClosedRound {
			t.Errorf("server %s acked/closed = %+v, want acked == closed", name, m.Serve)
		}
	}

	// One end delivery exists on the builder, and relevo.WaitChain pulls it.
	entry, found, err := rt.Store.PendingForMasterMind(builderName)
	if err != nil {
		t.Fatalf("PendingForMasterMind(%s): %v", builderName, err)
	}
	if !found || entry.Kind != store.KindChain {
		t.Fatalf("pending on the builder = %+v (found %v), want the chain's one KindChain entry", entry, found)
	}
	if !strings.Contains(entry.Payload, "chain "+chainServerName+" finished") {
		t.Errorf("the end delivery payload = %q, want the chain's finished line", entry.Payload)
	}
	waited, err := relevo.WaitChain(ctx, rt, chainServerName, time.Minute, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if waited.Code != relevo.WaitClosed {
		t.Fatalf("WaitChain code = %d, want %d: a finished chain is exit 0", waited.Code, relevo.WaitClosed)
	}
	if waited.Payload == "" {
		t.Fatalf("WaitChain returned no payload; the chain's one end delivery must be pulled")
	}
	again, err := relevo.WaitChain(ctx, rt, chainServerName, time.Minute, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("second WaitChain: %v", err)
	}
	if again.Payload != "" {
		t.Errorf("a second WaitChain pulled %q; the chain has exactly one end delivery", again.Payload)
	}

	// The trace through GET holds every transition in seq order, ending at
	// finish -- the same pattern the server wrote.
	doc, err := relevo.ChainTrace(ctx, rt, chainServerName)
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if len(doc.Events) != len(want) {
		t.Fatalf("the pulled trace has %d rows, want %d:\n%s", len(doc.Events), len(want), relevo.RenderTrace(doc))
	}
	for i, step := range want {
		got := doc.Events[i]
		if got.Seq != i+1 || got.Step != step.step || got.Member != step.member ||
			got.Flow == nil || got.Flow.Kind != step.event || got.FlowAction == nil || got.FlowAction.Kind != step.action {
			t.Errorf("pulled trace row %d = seq %d step %s member %s event %+v action %+v, want seq %d step %s member %s event %q action %q",
				i, got.Seq, got.Step, got.Member, got.Flow, got.FlowAction, i+1, step.step, step.member, step.event, step.action)
		}
	}
	if last := doc.Events[len(doc.Events)-1]; last.FlowAction.Kind != workflow.ActionFinish {
		t.Errorf("the last pulled trace row's action = %q, want finish", last.FlowAction.Kind)
	}
}

// newServerChainRegistry is the server's own roles registry for this scenario:
// the shipped builder and reviewer plus the lite-planner whose claude
// definition is architect. Its candidate set is the server's own, so both
// machines resolve the same token.
//
// The shipped default also names a security actor, which this chain leaves
// switched off, but a served chain validates the whole definition against the
// server's own actors, so the server must know it.
func newServerChainRegistry(t *testing.T, set *candidate.Set, pol policy.Policy) *roles.Registry {
	t.Helper()
	reader := "reader"
	rows := map[string]roles.Row{
		"builder":  {Candidates: []string{chainServerToken}},
		"reviewer": {Candidates: []string{chainServerToken}},
		"lite-planner": {
			Shape:       &reader,
			Candidates:  []string{chainServerToken},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "architect"}},
		},
		"security": {
			Shape:       &reader,
			Candidates:  []string{chainServerToken},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "security-reviewer"}},
		},
	}
	reg, err := roles.Build(&roles.File{Rows: rows}, set, pol)
	if err != nil {
		t.Fatalf("build the server roles registry: %v", err)
	}
	return reg
}

// driveServedChain completes whatever served process the runner holds: a gate,
// a builder round or a reader round. It runs after each server tick, so the
// server sees the completed step on its next tick.
func driveServedChain(t *testing.T, rt relevo.Runtime, runner *scriptRunner, builder string, members ...string) {
	t.Helper()
	spec, ok := runner.runningSpec()
	if !ok {
		return
	}
	if isGateSpec(spec) {
		// A chain check runs as its own step, so its log is the spec's own
		// check-log path; the run ends when the runner's exit is scripted.
		runner.completeGate(t, spec.LogPath, 0)
		return
	}
	name, b, ok := servedOpenMember(t, rt.Store, append([]string{builder}, members...)...)
	if !ok {
		return
	}
	if b.Shape == store.ShapeReader {
		finishServedReader(t, rt, name, b.Round, servedReaderOutput(name, b.Round))
		return
	}
	finishRound(t, rt, name, b.Round, fmt.Sprintf("chain round %d", b.Round))
}

// servedOpenMember names the member whose round the runner holds: an admitted
// (not queued) round with a prompt entry and no report entry yet.
func servedOpenMember(t *testing.T, st *store.Store, names ...string) (string, store.Binding, bool) {
	t.Helper()
	for _, name := range names {
		b, err := st.Load(name)
		if err != nil || !b.QueuedAt.IsZero() {
			continue
		}
		entries, err := st.ReadLog(name)
		if err != nil {
			continue
		}
		if !relevo.HasPromptEntry(entries, b.Round) {
			continue
		}
		if relevo.HasEntry(entries, b.Round, store.DirToMasterMind, store.KindReport) {
			continue
		}
		return name, b, true
	}
	return "", store.Binding{}, false
}

// finishServedReader completes a served reader round: it writes the output
// where the server's close reads it (the artifact output path relevo resolves
// for the actor) and the done marker, then marks the runner dead.
func finishServedReader(t *testing.T, rt relevo.Runtime, name string, round int, output string) {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("finishServedReader: load %s: %v", name, err)
	}
	path := servedReaderOutputPath(rt.Store, b, round)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("finishServedReader: create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		t.Fatalf("finishServedReader: write %s: %v", path, err)
	}
	// Marker then exit, the order finishRound uses: a tick running between the
	// two never sees an exited reader with an output but no marker.
	if err := os.WriteFile(rt.Store.DonePath(name, round), []byte(""), 0o644); err != nil {
		t.Fatalf("finishServedReader: write done marker: %v", err)
	}
	if sr, ok := rt.Runner.(*scriptRunner); ok {
		sr.mu.Lock()
		sr.alive = false
		sr.mu.Unlock()
	}
}

// servedReaderOutputPath is where a served reader's close reads its output:
// the artifact output path for the binding's actor. The reviewer ships
// "findings"; a lite-planner runs the architect agent, whose label is "plan".
func servedReaderOutputPath(st *store.Store, b store.Binding, round int) string {
	label := "findings"
	if b.Role == "lite-planner" {
		label = "plan"
	}
	return st.OutputPath(b.Name, round, b.Role, label)
}

// servedReaderOutput is the text each reader round writes: the reviewer asks
// for changes on plan 1's first round and passes every later one; the planner
// writes the correction plan.
func servedReaderOutput(name string, round int) string {
	switch {
	case strings.HasSuffix(name, "-plan"):
		return chainServerPlanText
	case round == 1:
		return chainServerVerdict("changes")
	default:
		return chainServerVerdict("pass")
	}
}

// chainServerVerdict is a reviewer output carrying the relevo verdict block
// the server's close reads.
func chainServerVerdict(v string) string {
	return "I reviewed the round.\n\n```relevo\nverdict: " + v + "\n```\n"
}

// syncChainServerMirror pulls a server chain into its mirror, re-running while
// the mirror still moves and the chain has not settled. It is bounded, so a
// pull that never advances fails rather than spinning.
func syncChainServerMirror(t *testing.T, ctx context.Context, rt relevo.Runtime, name string) {
	t.Helper()
	deadline := time.Now().Add(chainServerDeadline)
	prev := ""
	for {
		if _, err := relevo.SyncRemote(ctx, rt); err != nil {
			t.Fatalf("SyncRemote: %v", err)
		}
		key := chainServerMirrorKey(t, rt, name)
		row := chainE2ERow(t, rt, name)
		settled := chain.Status(row.Status) != chain.StatusRunning
		if settled || key == prev {
			return
		}
		prev = key
		if time.Now().After(deadline) {
			t.Fatalf("chain %s did not settle within %s while pulling: status %s, phase %s, awaiting %s round %d, reason %q",
				name, chainServerDeadline, row.Status, row.Phase, row.AwaitingMember, row.AwaitingRound, row.Reason)
		}
	}
}

// chainServerMirrorKey is a progress key for the sync loop: the row's settled
// fields plus every member's round. A pass that changes none of it adds no
// progress, so the loop stops.
func chainServerMirrorKey(t *testing.T, rt relevo.Runtime, name string) string {
	t.Helper()
	row := chainE2ERow(t, rt, name)
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s/%d/%s/%d", row.Status, row.Phase, row.Plan, row.AwaitingMember, row.AwaitingRound)
	for _, member := range []string{row.Builder, row.Reviewer, row.Planner, row.Security} {
		if member == "" {
			continue
		}
		mb, err := rt.Store.Load(member)
		if err != nil {
			fmt.Fprintf(&b, "|%s:?", member)
			continue
		}
		fmt.Fprintf(&b, "|%s:%d", member, mb.Round)
	}
	return b.String()
}

// chainQueueStarted reports whether a member holds an admit entry -- the
// "started after ... queued" KindQueue row Admit writes -- for round.
func chainQueueStarted(t *testing.T, st *store.Store, name string, round int) bool {
	t.Helper()
	entries, err := st.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	for _, e := range entries {
		if e.Round == round && e.Kind == store.KindQueue && strings.Contains(e.Note, "started after") {
			return true
		}
	}
	return false
}

// mustNoReport fails when a binding already holds a report entry: before the
// pull no chain member may have one.
func mustNoReport(t *testing.T, st *store.Store, name string) {
	t.Helper()
	entries, err := st.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	for _, e := range entries {
		if e.Kind == store.KindReport {
			t.Fatalf("%s holds a report entry before the sync: %+v", name, e)
		}
	}
}

// mustBinding loads a binding, failing the test when it cannot.
func mustBinding(t *testing.T, st *store.Store, name string) store.Binding {
	t.Helper()
	b, err := st.Load(name)
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	return b
}

// chainMemberLog reads a binding's log through the client's store.
func chainMemberLog(t *testing.T, st *store.Store, name string) []store.LogEntry {
	t.Helper()
	entries, err := st.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	return entries
}

// countLog counts entries of one shape for one round.
func countLog(entries []store.LogEntry, round int, dir store.Direction, kind store.Kind) int {
	n := 0
	for _, e := range entries {
		if e.Round == round && e.Direction == dir && e.Kind == kind {
			n++
		}
	}
	return n
}

// pulledRounds is the round numbers a member pulled in this scenario.
func pulledRounds(name, builder, reviewer, planner string) []int {
	switch name {
	case builder, reviewer:
		return []int{1, 2, 3}
	case planner:
		return []int{1}
	}
	return nil
}
