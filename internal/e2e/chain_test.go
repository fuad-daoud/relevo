package e2e

// TestChainE2E is the chain slice's integration pin: a two-plan local chain,
// run end to end by the fake `claude` on PATH, with the daemon driving every
// round from the first builder send to the chain's own finish.
//
// The scenario the plan pins:
//
//	1  the same isolation TestHeadlessE2E uses -- HOME and the XDG dirs under
//	   a temp root, the fake harness first on PATH, and dbtest.InstallOwner;
//	2  a mastermind is registered, and relevo.ChainStart is called with two
//	   plan files and the security phase on, its reader members pointed at the
//	   shipped reader roles (reviewer, researcher) so every round resolves to
//	   the fake harness;
//	3  the daemon ticks until the chain's row is done, bounded;
//	4  the trace has one row per transition and ends at finish, the builder
//	   ran plan 1, the correction round, plan 2 and the fix round, the
//	   reviewer's second round saw the correction, and the security scan
//	   reported one finding;
//	5  no member held a pending mastermind delivery while the chain ran, and
//	   the chain's one end delivery sits on the builder;
//	6  relevo.WaitChain returns that one delivery with code 0, and a second
//	   wait finds nothing;
//	7  `show <n> --trace` renders the same steps.
//
// Every wait is bounded and names what it was waiting for, so a stuck chain
// fails with its row and its members' notes rather than hanging the run.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	// chainE2EName is the chain's name: the builder member's too.
	chainE2EName = "chainshop"
	// The two plans the chain runs, and the candidate the fake harness is
	// registered as. The model is only a label: nothing reads it.
	chainE2EModel = "fake-chain-e2e"
	// chainE2EDeadline bounds the whole scenario. Eleven rounds, each one a
	// real process, take seconds; the bound is what turns a stuck chain into a
	// failure that names the step.
	chainE2EDeadline = 2 * time.Minute
	// chainRecapMarker is the text the fake reviewer writes after its
	// block-bearing message: a recap that carries no relevo block, so the
	// verdict can only be read from the round's stream.
	chainRecapMarker = "Reviewer recap"
)

func TestChainE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// -- 1. The same isolation TestHeadlessE2E runs under --------------------
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
	writeChainCandidatesAndPolicy(t, configDir)
	root := filepath.Join(home, ".local", "state", "relevo")
	rt, reg := newHeadlessRuntime(t, root, configDir)

	// relevo.ChainStart cuts the builder's worktree from the working
	// directory, so the throwaway repo is where the chain starts from.
	repo := newRepo(t)
	t.Chdir(repo)

	// -- 2. A mastermind, two plans, and the chain ---------------------------
	rec, _, err := mastermind.Init(reg, mastermind.InitInput{
		Kind: "claude", SessionID: "e2e-chain-mastermind", CWD: repo, Now: rt.Now(),
	})
	if err != nil {
		t.Fatalf("register the mastermind: %v", err)
	}

	plans := []string{
		writePlan(t, "chain-plan-1.md", "# Plan one\n\nDo the first thing.\n"),
		writePlan(t, "chain-plan-2.md", "# Plan two\n\nDo the second thing.\n"),
	}
	res, err := relevo.ChainStart(ctx, rt, relevo.ChainOptions{
		Name:         chainE2EName,
		Plans:        plans,
		Feature:      "chains-s1-e2e",
		MasterMindID: rec.ID,
		// The shipped reader roles: the chain's own defaults (lite-planner,
		// security) are not roles this machine's config names, and inventing
		// actors for a test would be a config change, not a test.
		ReviewerActor: "reviewer",
		PlannerActor:  "researcher",
		SecurityActor: "reviewer",
		Security:      chainBoolPtr(true),
	})
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}
	if res.Plans != 2 {
		t.Fatalf("chain %s started with %d plans, want 2", chainE2EName, res.Plans)
	}

	builderName := chainE2EName
	reviewerName := chainE2EName + "-rev"
	plannerName := chainE2EName + "-plan"
	securityName := chainE2EName + "-sec"
	t.Cleanup(func() {
		stopRecordedBuilders(t, rt, builderName, reviewerName, plannerName, securityName)
	})

	// -- 3. The daemon drives every round ------------------------------------
	daemon := relevo.NewDaemon(rt, 200*time.Millisecond)
	tickUntilChainFinishes(t, ctx, daemon, rt, chainE2EName, func() {
		chainAssertNoMemberPending(t, rt, builderName, reviewerName, plannerName, securityName)
	})

	// -- 4. What the trace and the members say -------------------------------
	row := chainE2ERow(t, rt, chainE2EName)
	if row.Status != string(chain.StatusDone) || row.Phase != string(chain.PhaseFinished) {
		t.Fatalf("chain ended status %q phase %q, want done/finished (reason %q)", row.Status, row.Phase, row.Reason)
	}
	if row.Plan != 2 {
		t.Errorf("chain ended on plan %d, want 2: plan 1 must pass before plan 2 runs", row.Plan)
	}
	if row.Corrections != 0 {
		t.Errorf("chain ended with %d corrections, want 0: the fix plan resets the count", row.Corrections)
	}

	planOne := chainStaged(t, rt, rt.Store.ChainPlanPath(chainE2EName, 1))
	planTwo := chainStaged(t, rt, rt.Store.ChainPlanPath(chainE2EName, 2))

	// The builder ran four rounds: plan 1, the correction round, plan 2 and
	// the fix round the security phase bought. A builder round is handed the
	// plan's own copy, so the round-3 prompt is plan 2 -- that is the pass
	// advancing; a chain that went straight from the changes verdict to plan 2
	// would never open the correction round at all.
	if b := loadBinding(t, rt, builderName); b.Round != 5 {
		t.Fatalf("builder round = %d, want 5: rounds 1 (plan 1), 2 (the correction), 3 (plan 2) and 4 (the fix plan) must all have closed", b.Round)
	}
	for round := 1; round <= 4; round++ {
		if !chainPromptOpen(t, rt, builderName, round) {
			t.Fatalf("the builder has no prompt entry for round %d; the chain must hand it a round per step", round)
		}
	}
	if got := chainStaged(t, rt, rt.Store.PromptPath(builderName, 1)); got != planOne {
		t.Errorf("the builder's round 1 staged prompt = %q, want plan 1's copy", got)
	}
	if got := chainStaged(t, rt, rt.Store.PromptPath(builderName, 3)); got != planTwo {
		t.Errorf("the builder's round 3 staged prompt = %q, want plan 2's copy: the reviewer's pass advances the plan", got)
	}

	// The reviewer saw the correction: its second round is seeded after the
	// correction round closed, so its prompt names that round's report.
	if got := chainStaged(t, rt, rt.Store.PromptPath(reviewerName, 2)); !strings.Contains(got, rt.Store.ReportPath(builderName, 2)) {
		t.Errorf("the reviewer's round 2 seed does not name the correction round's report %s:\n%s",
			rt.Store.ReportPath(builderName, 2), got)
	}

	// The reviewer's round-2 seed names the plan's whole span as well as the
	// correction round's own diff: the plan's start through the correction
	// round's tree, captured beside the round diff. Each is a copy under the
	// chain's own directory -- a path a runner can open, not the round_file key
	// itself. The chain finished done, so the copies were swept; the seed text
	// still names where they were.
	reviewerTwo := chainStaged(t, rt, rt.Store.PromptPath(reviewerName, 2))

	planDiffKey := rt.Store.PlanDiffPath(builderName, 2)
	planDiffCopy, ok := rt.Store.ChainInputPath(chainE2EName, planDiffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", planDiffKey)
	}
	chainInputCopyGone(t, planDiffCopy)
	if want := "Plan diff, every round of this plan so far: " + planDiffCopy + "."; !strings.Contains(reviewerTwo, want) {
		t.Errorf("the reviewer's round 2 seed does not name the cumulative plan diff copy %s:\n%s", planDiffCopy, reviewerTwo)
	}
	if patch := chainStaged(t, rt, planDiffKey); patch == "" {
		t.Errorf("the plan's cumulative diff at %s is empty", planDiffKey)
	}

	// The correction round appends its own line to the fake worktree file, so
	// its own round diff was captured: the seed names a copy of that diff. A
	// runner that could not produce one would instead read the path-free
	// "not available" wording -- a seed never names a key a runner cannot open.
	diffKey := rt.Store.DiffPath(builderName, 2)
	if _, err := rt.Store.ReadFile(diffKey); err == nil {
		diffCopy, ok := rt.Store.ChainInputPath(chainE2EName, diffKey)
		if !ok {
			t.Fatalf("ChainInputPath(%s) = false", diffKey)
		}
		chainInputCopyGone(t, diffCopy)
		if want := "This round's diff: " + diffCopy + "."; !strings.Contains(reviewerTwo, want) {
			t.Errorf("the reviewer's round 2 seed does not name the correction round's diff copy %s:\n%s", diffCopy, reviewerTwo)
		}
	} else if want := "This round's diff: not available:"; !strings.Contains(reviewerTwo, want) {
		t.Errorf("the reviewer's round 2 seed neither names a round diff copy nor words its miss:\n%s", reviewerTwo)
	}

	// The reviewer's round-1 seed judged the builder's round 1, whose diff was
	// captured: that seed names the diff copy the chain kept while it ran.
	reviewerOne := chainStaged(t, rt, rt.Store.PromptPath(reviewerName, 1))
	firstDiffKey := rt.Store.DiffPath(builderName, 1)
	firstDiffCopy, ok := rt.Store.ChainInputPath(chainE2EName, firstDiffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", firstDiffKey)
	}
	chainInputCopyGone(t, firstDiffCopy)
	if want := "This round's diff: " + firstDiffCopy + "."; !strings.Contains(reviewerOne, want) {
		t.Errorf("the reviewer's round 1 seed does not name the round diff copy %s:\n%s", firstDiffCopy, reviewerOne)
	}

	// The reviewer writes a recap after the message that carries its block.
	// The saved artifact is the block-carrying message, stripped of the block
	// the parse already read; the verdict on the trace above is still pass, so
	// it must have been read from the stream: an event build that dropped the
	// stream fallback would halt here with "reviewer gave no verdict".
	for _, round := range []int{1, 2} {
		outPath := rt.Store.OutputPath(reviewerName, round, "reviewer", "findings")
		body := chainStaged(t, rt, outPath)
		if !strings.Contains(body, "I read the plan, the report, the round diff") {
			t.Errorf("the reviewer's round %d output lost the review's own text:\n%s", round, body)
		}
		if strings.Contains(body, chainRecapMarker) {
			t.Errorf("the reviewer's round %d output is the recap, not the block-carrying message:\n%s", round, body)
		}
		if strings.Contains(body, "verdict:") {
			t.Errorf("the reviewer's round %d output carries the verdict block; the verdict must come from the stream:\n%s", round, body)
		}
	}

	// The correction planner's seed names the builder round the reviewer's
	// changes verdict judged, so the planner reads the report and diff that
	// verdict was about.
	correctionSeed := chainStaged(t, rt, rt.Store.PromptPath(plannerName, 1))
	if want := "Builder's report: " + rt.Store.ReportPath(builderName, 1); !strings.Contains(correctionSeed, want) {
		t.Errorf("the correction planner's seed does not name the judged builder round's report %s:\n%s", rt.Store.ReportPath(builderName, 1), correctionSeed)
	}

	// The builder's round 2 is the planner's correction plan itself, not the
	// chain's copy of plan 1: the correction plan reaches the builder.
	if got := chainStaged(t, rt, rt.Store.PromptPath(builderName, 2)); !strings.Contains(got, "# Correction plan") {
		t.Errorf("the builder's round 2 prompt is not the planner's correction plan:\n%s", got)
	}

	// The security member scanned once and found one thing; that finding is
	// what bought the planner's fix plan and the builder's fourth round.
	if !chainPromptOpen(t, rt, securityName, 1) {
		t.Fatalf("the security member has no round 1 prompt entry; the security phase must scan once")
	}
	if !chainPromptOpen(t, rt, plannerName, 1) || !chainPromptOpen(t, rt, plannerName, 2) {
		t.Fatalf("the planner has no prompt entries for both seeds: a correction and a fix plan")
	}

	// The security member's seed names the whole branch diff -- the chain's base
	// to the builder's newest closed round (3), copied under the chain's own
	// directory. The chain finished done, so the copy was swept, but the key
	// itself still carries every round's line, so a one-round diff cannot stand
	// in for it. The seed text still names where the copy was.
	securitySeed := chainStaged(t, rt, rt.Store.PromptPath(securityName, 1))
	chainDiffKey := rt.Store.ChainDiffPath(builderName, 3)
	chainDiffCopy, ok := rt.Store.ChainInputPath(chainE2EName, chainDiffKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", chainDiffKey)
	}
	chainInputCopyGone(t, chainDiffCopy)
	if !strings.Contains(securitySeed, chainDiffCopy) {
		t.Errorf("the security seed does not name the whole branch diff copy %s:\n%s", chainDiffCopy, securitySeed)
	}
	for round := 1; round <= 3; round++ {
		line := fmt.Sprintf("one round of fake work %03d", round)
		if !strings.Contains(chainStaged(t, rt, chainDiffKey), line) {
			t.Errorf("the whole branch diff at %s does not carry %q; a one-round diff cannot stand in:\n%s", chainDiffKey, line, chainStaged(t, rt, chainDiffKey))
		}
	}
	if oldCopy, ok := rt.Store.ChainInputPath(chainE2EName, rt.Store.DiffPath(builderName, 3)); ok && strings.Contains(securitySeed, oldCopy) {
		t.Errorf("the security seed still names the old round-diff copy %s:\n%s", oldCopy, securitySeed)
	}

	// The fix planner's seed names the same whole branch diff copy: the
	// builder's newest closed round is still 3 at the fixes send.
	fixSeed := chainStaged(t, rt, rt.Store.PromptPath(plannerName, 2))
	if !strings.Contains(fixSeed, chainDiffCopy) {
		t.Errorf("the fix planner's seed does not name the same whole branch diff copy %s:\n%s", chainDiffCopy, fixSeed)
	}

	// The trace: one row per transition, in seq order, ending at finish. The
	// pattern is the pin for the three design mutations -- a changes verdict
	// advancing to plan 2 would drop the planner's first send, a close that
	// matched any round would add a second row per close, and a missing
	// verdict read as a pass would drop the reviewer's first round.
	want := []chainE2EStep{
		{member: builderName, event: chain.EventBuilderClosed, action: chain.ActionSend, to: chain.MemberReviewer, plan: 1},
		{member: reviewerName, event: chain.EventReviewerClosed, action: chain.ActionSend, to: chain.MemberPlanner, plan: 1},
		{member: plannerName, event: chain.EventPlannerClosed, action: chain.ActionSend, to: chain.MemberBuilder, plan: 1},
		{member: builderName, event: chain.EventBuilderClosed, action: chain.ActionSend, to: chain.MemberReviewer, plan: 1},
		{member: reviewerName, event: chain.EventReviewerClosed, action: chain.ActionSend, to: chain.MemberBuilder, plan: 1},
		{member: builderName, event: chain.EventBuilderClosed, action: chain.ActionSend, to: chain.MemberReviewer, plan: 2},
		{member: reviewerName, event: chain.EventReviewerClosed, action: chain.ActionSend, to: chain.MemberSecurity, plan: 2},
		{member: securityName, event: chain.EventSecurityClosed, action: chain.ActionSend, to: chain.MemberPlanner, plan: 2},
		{member: plannerName, event: chain.EventPlannerClosed, action: chain.ActionSend, to: chain.MemberBuilder, plan: 2},
		{member: builderName, event: chain.EventBuilderClosed, action: chain.ActionSend, to: chain.MemberReviewer, plan: 2},
		{member: reviewerName, event: chain.EventReviewerClosed, action: chain.ActionFinish, plan: 2},
	}
	doc, err := relevo.ChainTrace(ctx, rt, chainE2EName)
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
		if got.Member != step.member {
			t.Errorf("trace row %d closed %s, want %s", i, got.Member, step.member)
		}
		if got.Event.Kind != step.event {
			t.Errorf("trace row %d event = %q, want %q", i, got.Event.Kind, step.event)
		}
		if got.Action.Kind != step.action {
			t.Errorf("trace row %d action = %q, want %q", i, got.Action.Kind, step.action)
		}
		if step.to != "" && got.Action.Member != step.to {
			t.Errorf("trace row %d action sends to %q, want %q", i, got.Action.Member, step.to)
		}
		if got.Plan != step.plan {
			t.Errorf("trace row %d plan = %d, want %d: each row keeps the plan it was written on", i, got.Plan, step.plan)
		}
	}
	if got := doc.Events[1].Event.Verdict; got != chain.VerdictChanges {
		t.Errorf("the reviewer's first verdict = %q, want changes: the fake harness's first reviewer round asks for a correction", got)
	}
	for _, i := range []int{4, 6, 10} {
		if got := doc.Events[i].Event.Verdict; got != chain.VerdictPass {
			t.Errorf("the reviewer's verdict on trace row %d = %q, want pass", i, got)
		}
	}
	if got := doc.Events[7].Event; !got.FindingsGiven || got.Findings != 1 {
		t.Errorf("the security close = %+v, want one finding given", got)
	}
	if last := doc.Events[len(doc.Events)-1]; last.Action.Kind != chain.ActionFinish {
		t.Errorf("the last trace row's action = %q, want finish", last.Action.Kind)
	}

	// A `show --trace` read renders exactly the same steps.
	shown, err := relevo.Show(ctx, rt, relevo.ShowOptions{Name: chainE2EName, Section: relevo.ShowTrace})
	if err != nil {
		t.Fatalf("Show --trace: %v", err)
	}
	if shown.Text != relevo.RenderTrace(doc) {
		t.Errorf("show --trace rendered different steps:\ngot:\n%s\nwant:\n%s", shown.Text, relevo.RenderTrace(doc))
	}

	// -- 5/6. The one end delivery -------------------------------------------
	// Exactly one pending KindChain entry exists, and it is on the builder:
	// chainTerminal queues it on the first member whose record survives, and
	// the builder's is the first of the four.
	entry, found, err := rt.Store.PendingForMasterMind(builderName)
	if err != nil {
		t.Fatalf("PendingForMasterMind(%s): %v", builderName, err)
	}
	if !found || entry.Kind != store.KindChain {
		t.Fatalf("pending on the builder = %+v (found %v), want the chain's one KindChain entry", entry, found)
	}
	if !strings.Contains(entry.Payload, "chain "+chainE2EName+" finished") {
		t.Errorf("the end delivery payload = %q, want the chain's finished line", entry.Payload)
	}

	waited, err := relevo.WaitChain(ctx, rt, chainE2EName, time.Minute, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if waited.Code != relevo.WaitClosed {
		t.Fatalf("WaitChain code = %d, want %d (0): a finished chain is exit 0", waited.Code, relevo.WaitClosed)
	}
	if waited.Payload == "" {
		t.Fatalf("WaitChain returned no payload; the chain's one end delivery must be pulled")
	}
	again, err := relevo.WaitChain(ctx, rt, chainE2EName, time.Minute, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("second WaitChain: %v", err)
	}
	if again.Payload != "" {
		t.Errorf("a second WaitChain pulled %q; the chain has exactly one end delivery", again.Payload)
	}
}

// chainE2EStep is one expected trace row: the member whose round closed, the
// event that close raised, the action it produced, the part a send names, and
// the plan the row was written on.
type chainE2EStep struct {
	member string
	event  chain.EventKind
	action chain.ActionKind
	to     string
	plan   int
}

// writeChainCandidatesAndPolicy writes the config the chain's members resolve
// against. One candidate serves the builder plus the two shipped reader roles
// the e2e points the chain's reviewer, planner and security members at: a
// chain member's actor must be a reader unless it is the builder.
func writeChainCandidatesAndPolicy(t *testing.T, configDir string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	token := "claude/anthropic/" + chainE2EModel
	candidates := `[{"harness":"claude","provider":"anthropic","model":"` + chainE2EModel + `","roles":["builder","reviewer","researcher"]}]`
	pol := `{"order":{"builder":["` + token + `"],"reviewer":["` + token + `"],"researcher":["` + token + `"]}}`
	for name, body := range map[string]string{"candidates.json": candidates, "policy.json": pol} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// chainBoolPtr is a *bool for the options whose nil means "take the policy".
func chainBoolPtr(v bool) *bool { return &v }

// chainStaged reads a round's staged file through the store, so a round the
// daemon has already sealed is read from its row rather than its removed file.
func chainStaged(t *testing.T, rt relevo.Runtime, path string) string {
	t.Helper()
	body, err := rt.Store.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// chainE2ERow reads a chain's row, failing the test when it cannot.
func chainE2ERow(t *testing.T, rt relevo.Runtime, name string) db.ChainRow {
	t.Helper()
	c, err := rt.Store.Chain(name)
	if err != nil {
		t.Fatalf("Chain %s: %v", name, err)
	}
	return c
}

// chainPromptOpen reports whether a member has an open (unclosed) round for
// that number: the prompt entry the chain's own sender wrote.
func chainPromptOpen(t *testing.T, rt relevo.Runtime, name string, round int) bool {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToBuilder && e.Kind == store.KindPrompt {
			return true
		}
	}
	return false
}

// chainAssertNoMemberPending is the invariant that holds only while a chain
// runs: a member's close is consumed by the chain, so nothing is queued on any
// member's mastermind mailbox. The chain's own one end delivery is queued when
// it ends, which is why this is checked inside the tick loop and never after.
func chainAssertNoMemberPending(t *testing.T, rt relevo.Runtime, members ...string) {
	t.Helper()
	for _, member := range members {
		entry, found, err := rt.Store.PendingForMasterMind(member)
		if err != nil {
			t.Fatalf("PendingForMasterMind(%s): %v", member, err)
		}
		if found {
			t.Fatalf("while the chain ran, member %s held a pending delivery %+v; a chain member's close is consumed by the chain, not queued",
				member, entry)
		}
	}
}

// tickUntilChainFinishes drives the daemon tick until the chain's row is no
// longer running, bounded by chainE2EDeadline. observe runs while the chain is
// still running, so a test can assert an invariant that does not hold
// afterwards (the members' empty mailboxes). A chain that never finishes fails
// with its own row and its members' notes, so a stuck step is named rather
// than a bare timeout.
func tickUntilChainFinishes(t *testing.T, ctx context.Context, daemon *relevo.Daemon, rt relevo.Runtime, name string, observe func()) {
	t.Helper()
	deadline := time.Now().Add(chainE2EDeadline)
	for {
		if err := daemon.Tick(ctx); err != nil {
			t.Fatalf("daemon.Tick: %v", err)
		}
		c := chainE2ERow(t, rt, name)
		if chain.Status(c.Status) != chain.StatusRunning {
			if chain.Status(c.Status) != chain.StatusDone {
				dumpChainHaltDiagnostics(t, rt, c)
			}
			return
		}
		if observe != nil {
			observe()
		}
		if time.Now().After(deadline) {
			t.Fatalf("chain %s did not finish within %s: status %s, phase %s, step %s, plan %d/%d, awaiting %s round %d, reason %q; members: %s",
				name, chainE2EDeadline, c.Status, c.Phase, c.Step, c.Plan, c.Plans, c.AwaitingMember, c.AwaitingRound, c.Reason,
				chainMembersNote(t, rt, c))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// dumpChainHaltDiagnostics prints the halted member's round state -- its pid
// and stream segments, the marker and stream stats, and the stream's own bytes
// -- so a reader close that found no verdict is diagnosable from the CI log.
func dumpChainHaltDiagnostics(t *testing.T, rt relevo.Runtime, c db.ChainRow) {
	t.Helper()
	t.Logf("chain halted: status %s phase %s awaiting %s round %d reason %q", c.Status, c.Phase, c.AwaitingMember, c.AwaitingRound, c.Reason)
	member := c.AwaitingMember
	if member == "" {
		member = c.Reviewer
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		t.Logf("member %s: load: %v", member, err)
		return
	}
	donePath := rt.Store.DonePath(member, b.Round)
	streamPath := rt.Store.StreamPath(member, b.Round)
	ds, dmt, dok, derr := rt.Store.StatFile(donePath)
	ss, smt, sok, serr := rt.Store.StatFile(streamPath)
	stream, rerr := rt.Store.ReadFile(streamPath)
	t.Logf("member %s round %d state %s pid=%d started=%d segments=%+v", member, b.Round, b.State, b.Builder.PID, b.Builder.StartedAt, b.Builder.StreamSegments)
	t.Logf("done   %s stat=(size=%d mtime=%s ok=%v err=%v)", donePath, ds, dmt, dok, derr)
	t.Logf("stream %s stat=(size=%d mtime=%s ok=%v err=%v) readErr=%v bytes=%d", streamPath, ss, smt, sok, serr, rerr, len(stream))
	if len(stream) > 1200 {
		stream = stream[:1200]
	}
	t.Logf("stream content:\n%s", stream)
}

// chainMembersNote names every member's state, round and halt, for a failure
// message that must say which step a stuck chain was on.
func chainMembersNote(t *testing.T, rt relevo.Runtime, c db.ChainRow) string {
	t.Helper()
	var b strings.Builder
	for _, member := range []string{c.Builder, c.Reviewer, c.Planner, c.Security} {
		if member == "" {
			continue
		}
		mb, err := rt.Store.Load(member)
		if err != nil {
			fmt.Fprintf(&b, "%s (unreadable: %v); ", member, err)
			continue
		}
		fmt.Fprintf(&b, "%s state=%s round=%d halt=%q; ", member, mb.State, mb.Round, mb.Halt)
	}
	return strings.TrimSpace(b.String())
}

// chainInputCopyGone asserts a chain's input copy was swept when the chain
// ended done: the copy the seed named while the chain ran is gone from disk.
func chainInputCopyGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the chain's input copy %s is still there (err %v), want it swept when the chain ended done", path, err)
	}
}
