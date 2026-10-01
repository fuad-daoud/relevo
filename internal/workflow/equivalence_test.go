package workflow

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/reporttail"
)

// The default workflow must be exactly today's chain. This file replays every
// case of the old chain.Next table through the shipped default, and drives both
// engines through whole event scripts, comparing the sequence of sends to each
// member and the terminal status. The translation below is the only place the
// old shapes and the new ones meet.

// equivCases is the old chain.Next table, one row per old scenario. Several
// rows share a source function when the old test loops over sub-cases. The
// source field is checked against ../chain/chain_test.go so a new TestNext*
// case cannot land without an equivalence row.
var equivCases = []equivCase{
	{
		source: "TestNextBuilderCloseGreenSeedsReviewer",
		name:   "builder green reaches the reviewer",
		state:  eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1),
		event: chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
			Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen},
	},
	{
		source: "TestNextBuilderCloseNoCheckSeedsReviewer",
		name:   "builder with no check reaches the reviewer",
		state:  eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1),
		event: chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
			Outcome: reporttail.OutcomeDone, Gate: chain.GateNone},
	},
	{
		source: "TestNextSendZeroesTheAwaitingRound",
		name:   "a send clears the awaited round",
		state:  eqAwaited(chain.StepBuilding, chain.MemberBuilder, 7),
		event: chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 7,
			Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen},
	},
	{
		source: "TestNextBuilderCloseRedSeedsReviewer",
		name:   "a red gate after the regate still reaches the reviewer",
		state:  eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1),
		event: chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
			Outcome: reporttail.OutcomeDone, Gate: chain.GateRed},
	},
	{
		source:   "TestNextBuilderHaltedHalts",
		name:     "a halted builder halts the chain",
		state:    eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1),
		event:    chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1, Outcome: reporttail.OutcomeHalted, Reason: "gate red"},
		reworded: "build halted: gate red",
	},
	{
		source:   "TestNextBuilderBlockedHalts",
		name:     "a blocked builder halts the chain",
		state:    eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1),
		event:    chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1, Outcome: reporttail.OutcomeBlocked, Reason: "waiting on a decision"},
		reworded: "build blocked: waiting on a decision",
	},
	{
		source:   "TestNextBuilderUnstructuredHalts",
		name:     "an unstructured builder report halts the chain",
		state:    eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1),
		event:    chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1, Outcome: reporttail.OutcomeUnstructured},
		reworded: "build unstructured",
	},
	{
		source: "TestNextReviewerPassAdvancesPlan",
		name:   "a reviewer pass advances the plan",
		state:  eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:  chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2, Verdict: chain.VerdictPass},
	},
	{
		source: "TestNextReviewerPassLastPlanStartsSecurity",
		name:   "a pass on the last plan starts the security scan",
		state: func() chain.State {
			s := eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2)
			s.Plan = 2
			s.Settings.Security = true
			return s
		}(),
		event: chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2, Verdict: chain.VerdictPass},
	},
	{
		source: "TestNextReviewerPassLastPlanWithoutSecurityFinishes",
		name:   "a pass on the last plan with no security finishes",
		state: func() chain.State {
			s := eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2)
			s.Plan = 2
			return s
		}(),
		event: chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2, Verdict: chain.VerdictPass},
	},
	{
		source: "TestNextReviewerPassInSecurityFinishes",
		name:   "a pass in the security phase finishes",
		state: func() chain.State {
			s := eqAwaited(chain.StepReviewing, chain.MemberReviewer, 3)
			s.Phase = chain.PhaseSecurity
			s.Settings.Security = true
			return s
		}(),
		event: chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 3, Verdict: chain.VerdictPass},
	},
	{
		source: "TestNextReviewerChangesSeedsCorrection",
		name:   "a changes verdict seeds the planner",
		state:  eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:  chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2, Verdict: chain.VerdictChanges},
	},
	{
		source: "TestNextReviewerChangesAtBudgetHalts",
		name:   "changes at the corrections budget halts",
		state: func() chain.State {
			s := eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2)
			s.Corrections = 3
			return s
		}(),
		event: chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2, Verdict: chain.VerdictChanges},
	},
	{
		source: "TestNextReviewerChangesAtBudgetHalts",
		name:   "changes with a zero corrections budget halts",
		state: func() chain.State {
			s := eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2)
			s.Settings.MaxCorrections = 0
			return s
		}(),
		event: chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2, Verdict: chain.VerdictChanges},
	},
	{
		source:   "TestNextReviewerNoVerdictHalts",
		name:     "a reviewer with no verdict halts",
		state:    eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:    chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2},
		reworded: "review halted: verdict: no relevo block carries it",
	},
	{
		source:   "TestNextReviewerNoVerdictHalts",
		name:     "a reviewer with an unknown verdict halts",
		state:    eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:    chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 2, Verdict: chain.Verdict("maybe")},
		reworded: "review halted: verdict: no relevo block carries it",
	},
	{
		source: "TestNextCorrectionPlanSendsBuilderAndCounts",
		name:   "a correction plan goes to the builder and counts",
		state:  eqAwaited(chain.StepCorrecting, chain.MemberPlanner, 2),
		event:  chain.Event{Kind: chain.EventPlannerClosed, Member: chain.MemberPlanner, Round: 2, PlanPresent: true},
	},
	{
		source:   "TestNextCorrectionPlanMissingHalts",
		name:     "a missing correction plan halts",
		state:    eqAwaited(chain.StepCorrecting, chain.MemberPlanner, 2),
		event:    chain.Event{Kind: chain.EventPlannerClosed, Member: chain.MemberPlanner, Round: 2},
		reworded: "correct halted: plan: not written or empty",
	},
	{
		source: "TestNextSecurityNoFindingsFinishes",
		name:   "a clean scan finishes",
		state: func() chain.State {
			s := eqAwaited(chain.StepScanning, chain.MemberSecurity, 1)
			s.Phase = chain.PhaseSecurity
			return s
		}(),
		event: chain.Event{Kind: chain.EventSecurityClosed, Member: chain.MemberSecurity, Round: 1, Findings: 0, FindingsGiven: true},
	},
	{
		source: "TestNextSecurityFindingsSeedFixPlanner",
		name:   "a scan with findings seeds the fix planner",
		state: func() chain.State {
			s := eqAwaited(chain.StepScanning, chain.MemberSecurity, 1)
			s.Phase = chain.PhaseSecurity
			return s
		}(),
		event: chain.Event{Kind: chain.EventSecurityClosed, Member: chain.MemberSecurity, Round: 1, Findings: 2, FindingsGiven: true},
	},
	{
		source: "TestNextSecurityWithoutACountHalts",
		name:   "a scan with no count halts",
		state: func() chain.State {
			s := eqAwaited(chain.StepScanning, chain.MemberSecurity, 1)
			s.Phase = chain.PhaseSecurity
			return s
		}(),
		event:    chain.Event{Kind: chain.EventSecurityClosed, Member: chain.MemberSecurity, Round: 1},
		reworded: "scan halted: findings: no relevo block carries it",
	},
	{
		source: "TestNextFixPlanSendsBuilderAndResetsCorrections",
		name:   "a fix plan goes to the builder and resets corrections",
		state: func() chain.State {
			s := eqAwaited(chain.StepPlanningFixes, chain.MemberPlanner, 3)
			s.Phase = chain.PhaseSecurity
			s.Corrections = 2
			return s
		}(),
		event: chain.Event{Kind: chain.EventPlannerClosed, Member: chain.MemberPlanner, Round: 3, PlanPresent: true},
	},
	{
		source: "TestNextFixPlanMissingHalts",
		name:   "a missing fix plan halts",
		state: func() chain.State {
			s := eqAwaited(chain.StepPlanningFixes, chain.MemberPlanner, 3)
			s.Phase = chain.PhaseSecurity
			return s
		}(),
		event:    chain.Event{Kind: chain.EventPlannerClosed, Member: chain.MemberPlanner, Round: 3},
		reworded: "fix-plan halted: plan: not written or empty",
	},
	{
		source: "TestNextNeedsYouHaltsWithTheMembersReason",
		name:   "needs_you halts with the member's reason",
		state:  eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:  chain.Event{Kind: chain.EventNeedsYou, Member: chain.MemberReviewer, Round: 2, Reason: "the harness asked for a decision"},
	},
	{
		source: "TestNextStoppedStops",
		name:   "stopped stops the chain",
		state:  eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1),
		event:  chain.Event{Kind: chain.EventStopped, Member: chain.MemberBuilder, Round: 1},
	},
	{
		source: "TestNextIgnoresAnotherRound",
		name:   "a reviewer close on another round is ignored",
		state:  eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:  chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 3, Verdict: chain.VerdictPass},
	},
	{
		source: "TestNextIgnoresAnotherRound",
		name:   "a reviewer close from another member is ignored",
		state:  eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:  chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberPlanner, Round: 2, Verdict: chain.VerdictPass},
	},
	{
		source: "TestNextIgnoresAnotherRound",
		name:   "a stopped close for another member is ignored",
		state:  eqAwaited(chain.StepReviewing, chain.MemberReviewer, 2),
		event:  chain.Event{Kind: chain.EventStopped, Member: chain.MemberBuilder, Round: 1},
	},
	{
		source: "TestNextTerminalIsInert",
		name:   "a closed close on a halted chain is inert",
		state:  eqTerminal(chain.StatusHalted),
		event:  eqTerminalClose(),
	},
	{
		source: "TestNextTerminalIsInert",
		name:   "a closed close on a stopped chain is inert",
		state:  eqTerminal(chain.StatusStopped),
		event:  eqTerminalClose(),
	},
	{
		source: "TestNextTerminalIsInert",
		name:   "a closed close on a done chain is inert",
		state:  eqTerminal(chain.StatusDone),
		event:  eqTerminalClose(),
	},
}

// equivCase is one row of the equivalence table: an old state and close, and,
// when the default rewords the halt reason, the new wording to accept.
type equivCase struct {
	source   string
	name     string
	state    chain.State
	event    chain.Event
	reworded string
}

// eqAwaited is a running chain in the build phase, waiting on one member round.
func eqAwaited(step chain.Step, member string, round int) chain.State {
	return chain.State{
		Status:   chain.StatusRunning,
		Phase:    chain.PhaseBuild,
		Step:     step,
		Plan:     1,
		Plans:    2,
		Settings: chain.Settings{MaxCorrections: 3},
		Awaiting: chain.Awaiting{Member: member, Round: round},
	}
}

// eqTerminal is a chain in a terminal status, still nominally on the builder.
func eqTerminal(status chain.Status) chain.State {
	s := eqAwaited(chain.StepBuilding, chain.MemberBuilder, 1)
	s.Status = status
	return s
}

// eqTerminalClose is a builder close a terminal chain must ignore.
func eqTerminalClose() chain.Event {
	return chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1,
		Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen}
}

func TestEquivalenceWithChainNext(t *testing.T) {
	for _, tc := range equivCases {
		t.Run(tc.name, func(t *testing.T) {
			oldGot, oldAct := chain.Next(tc.state, tc.event)
			def := equivDef(t, tc.state.Settings, equivGateParam(tc.event))
			ns := equivState(def, tc.state)
			nstate, nacts := equivReplay(t, def, ns, tc.event, tc.event.Round)
			checkEquiv(t, tc, oldGot, oldAct, ns, nstate, nacts)
		})
	}
}

// equivNextCaseRE matches a TestNext* function header in the old table.
var equivNextCaseRE = regexp.MustCompile(`(?m)^func (TestNext\w+)\(`)

// TestEquivalenceCoversEveryChainNextCase fails when the old chain.Next table
// grows a case the equivalence table does not replay.
func TestEquivalenceCoversEveryChainNextCase(t *testing.T) {
	data, err := os.ReadFile("../chain/chain_test.go")
	if err != nil {
		t.Fatalf("read the old chain.Next table: %v", err)
	}
	old := map[string]bool{}
	for _, m := range equivNextCaseRE.FindAllStringSubmatch(string(data), -1) {
		old[m[1]] = true
	}
	if len(old) == 0 {
		t.Fatal("no TestNext functions found in ../chain/chain_test.go")
	}
	have := map[string]bool{}
	for _, tc := range equivCases {
		have[tc.source] = true
	}
	var missing []string
	for name := range old {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the equivalence table lacks a row for: %s", strings.Join(missing, ", "))
	}
	for _, tc := range equivCases {
		if !old[tc.source] {
			t.Errorf("the equivalence table names %q, which the old table no longer has", tc.source)
		}
	}
}

// equivDef is the shipped default with the params a chain's settings and the
// round's gate resolve to.
func equivDef(t *testing.T, s chain.Settings, gate string) Definition {
	t.Helper()
	def, err := WithParams(Default(), map[string]string{
		"reviewer":        equivNameOr(s.ReviewerActor, "reviewer"),
		"planner":         equivNameOr(s.PlannerActor, "lite-planner"),
		"security":        equivNameOr(s.SecurityActor, "security"),
		"scan":            strconv.FormatBool(s.Security),
		"max_corrections": strconv.Itoa(s.MaxCorrections),
		"regate":          strconv.Itoa(s.Regate),
		"gate":            gate,
	})
	if err != nil {
		t.Fatalf("resolve the default's params: %v", err)
	}
	return def
}

// equivNameOr is an actor setting, or the default it falls back to.
func equivNameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

// equivGateParam is the gate param an old close implies: empty when the round
// ran no check, the shipped command otherwise.
func equivGateParam(e chain.Event) string {
	if e.Kind == chain.EventBuilderClosed && e.Gate == chain.GateNone {
		return ""
	}
	return "make check"
}

// equivState is the new state a chain state migrates to, by phase and
// corrections. The plans walk is placed on the plan the chain is on, and the
// corrections budget holds the count the chain has spent.
func equivState(def Definition, s chain.State) State {
	items := make([]string, 0, s.Plans)
	for i := 1; i <= s.Plans; i++ {
		items = append(items, "plan-"+strconv.Itoa(i)+".md")
	}
	at := equivStepAt(s)
	corrections := s.Corrections
	if at == "correct" || at == "fix-correct" {
		corrections++
	}
	ns := State{
		Status:  Status(s.Status),
		Reason:  s.Reason,
		At:      at,
		Visits:  map[string]int{},
		Iter:    map[string]Iter{"plans": {Index: s.Plan - 1, Items: items}},
		Results: map[string]Result{},
	}
	if s.Phase == chain.PhaseSecurity {
		ns.Visits["fix-correct"] = corrections
	} else {
		ns.Visits["correct"] = corrections
	}
	if at != "" {
		ns.Awaiting = Awaiting{Step: at, Member: equivActorOfStep(def, at), Round: s.Awaiting.Round}
	}
	return ns
}

// equivStepAt is the new step an old step migrates to.
func equivStepAt(s chain.State) string {
	security := s.Phase == chain.PhaseSecurity
	switch s.Step {
	case chain.StepBuilding:
		if security {
			if s.Corrections == 0 {
				return "fix-build"
			}
			return "fix-rebuild"
		}
		if s.Corrections == 0 {
			return "build"
		}
		return "build-fix"
	case chain.StepReviewing:
		if security {
			return "fix-review"
		}
		return "review"
	case chain.StepCorrecting:
		if security {
			return "fix-correct"
		}
		return "correct"
	case chain.StepScanning:
		return "scan"
	case chain.StepPlanningFixes:
		return "fix-plan"
	}
	return ""
}

// equivActorOfStep is the actor a mapped step runs.
func equivActorOfStep(def Definition, at string) string {
	switch at {
	case "build", "build-fix", "fix-build", "fix-rebuild":
		return def.Params["builder"].Str
	case "review", "fix-review":
		return def.Params["reviewer"].Str
	case "correct", "fix-correct", "fix-plan":
		return def.Params["planner"].Str
	case "scan":
		return def.Params["security"].Str
	}
	return ""
}

// equivStepMember is the fixed part a new step's actor fills, for comparing a
// new send against the old member the chain named.
var equivStepMember = map[string]string{
	"build": chain.MemberBuilder, "build-fix": chain.MemberBuilder,
	"fix-build": chain.MemberBuilder, "fix-rebuild": chain.MemberBuilder,
	"review": chain.MemberReviewer, "fix-review": chain.MemberReviewer,
	"correct": chain.MemberPlanner, "fix-correct": chain.MemberPlanner,
	"fix-plan": chain.MemberPlanner,
	"scan":     chain.MemberSecurity,
}

// equivSeedByOld maps an old seed kind to the shipped seed the default names.
var equivSeedByOld = map[chain.SeedKind]string{
	chain.SeedReviewer:   "shipped:review",
	chain.SeedCorrection: "shipped:correct",
	chain.SeedFixes:      "shipped:fix",
	chain.SeedSecurity:   "shipped:scan",
}

// equivReviewerOutputs is the declaration the reviewer's outcome is parsed
// against, mirroring the shipped actors.
var equivReviewerOutputs = Outputs{"verdict": {Kind: OutputOneOf, Values: []string{"pass", "changes"}}}

// equivPlannerOutputs is the planner's declared artifact.
var equivPlannerOutputs = Outputs{"plan": {Kind: OutputArtifact}}

// equivSecurityOutputs is the security actor's declared outputs.
var equivSecurityOutputs = Outputs{"findings": {Kind: OutputCount}, "report": {Kind: OutputArtifact}}

// equivReplay translates one old close into the new events it becomes and
// feeds them to the new engine, returning the actions in order. A builder
// close answers the engine's own actions until the next send that is not a
// repair round, or a terminal state.
func equivReplay(t *testing.T, def Definition, st State, e chain.Event, round int) (State, []Action) {
	t.Helper()
	var all []Action
	feed := func(ev Event) {
		s, acts := Next(def, st, ev)
		st = s
		all = append(all, acts...)
	}
	feedRun := func(ev Event) {
		if st.Awaiting.Member != "" {
			st.Awaiting.Round = round
		}
		feed(ev)
	}
	switch e.Kind {
	case chain.EventBuilderClosed:
		feed(Event{Kind: EventStepClosed, Step: st.At, Member: equivActorMember(def, e.Member), Round: round,
			Status: e.Outcome, Reason: e.Reason})
		for len(all) > 0 {
			last := all[len(all)-1]
			switch last.Kind {
			case ActionRunCheck:
				feed(Event{Kind: EventCheckClosed, Step: last.Step, Run: st.Awaiting.Run,
					Result: equivCheckResult(e.Gate), Log: "check.log"})
			case ActionSend:
				if last.Step == "repair" || last.Step == "fix-repair" {
					feedRun(Event{Kind: EventStepClosed, Step: last.Step, Member: last.Actor, Round: round, Status: "done"})
					continue
				}
				return st, all
			default:
				return st, all
			}
		}
		return st, all
	case chain.EventReviewerClosed:
		outcomes, reason := equivReviewOutcomes(e.Verdict)
		feed(equivCloseEvent(def, st, e, round, outcomes, reason))
	case chain.EventPlannerClosed:
		outcomes, reason := equivPlannerOutcomes(e.PlanPresent)
		feed(equivCloseEvent(def, st, e, round, outcomes, reason))
	case chain.EventSecurityClosed:
		outcomes, reason := equivFindingsOutcomes(e.Findings, e.FindingsGiven)
		feed(equivCloseEvent(def, st, e, round, outcomes, reason))
	case chain.EventNeedsYou:
		feed(Event{Kind: EventNeedsYou, Step: st.At, Member: equivActorMember(def, e.Member), Round: round, Reason: e.Reason})
	case chain.EventStopped:
		feed(Event{Kind: EventStopped, Step: st.At, Member: equivActorMember(def, e.Member), Round: round})
	default:
		feed(Event{Kind: EventKind(e.Kind), Step: st.At, Member: equivActorMember(def, e.Member), Round: round})
	}
	return st, all
}

// equivCloseEvent is a member close as a step_closed: done, or halted with the
// reason the outcome parse returned.
func equivCloseEvent(def Definition, st State, e chain.Event, round int, outcomes map[string]string, reason string) Event {
	status := "done"
	if reason != "" {
		status = "halted"
	}
	return Event{Kind: EventStepClosed, Step: st.At, Member: equivActorMember(def, e.Member), Round: round,
		Status: status, Outcomes: outcomes, Reason: reason}
}

// equivActorMember is the new actor name an old member maps to.
func equivActorMember(def Definition, member string) string {
	switch member {
	case chain.MemberBuilder:
		return def.Params["builder"].Str
	case chain.MemberReviewer:
		return def.Params["reviewer"].Str
	case chain.MemberPlanner:
		return def.Params["planner"].Str
	case chain.MemberSecurity:
		return def.Params["security"].Str
	}
	return member
}

// equivCheckResult is the new check result an old gate result implies.
func equivCheckResult(gate string) string {
	if gate == chain.GateRed {
		return "red"
	}
	return "green"
}

// equivReviewOutcomes parses a synthetic reviewer body carrying the verdict.
// With no verdict the body carries no block, so the parse reports the missing
// key.
func equivReviewOutcomes(v chain.Verdict) (map[string]string, string) {
	body := []byte("the reviewer gave no block\n")
	switch v {
	case chain.VerdictPass:
		body = []byte("```relevo\nverdict: pass\n```\n")
	case chain.VerdictChanges:
		body = []byte("```relevo\nverdict: changes\n```\n")
	}
	return ParseOutcomes(equivReviewerOutputs, body)
}

// equivPlannerOutcomes reports a planner close: a present plan is done, and an
// absent one names the missing artifact.
func equivPlannerOutcomes(present bool) (map[string]string, string) {
	if present {
		return map[string]string{}, ""
	}
	return nil, MissingArtifact(equivPlannerOutputs, map[string]int64{"plan": 0})
}

// equivFindingsOutcomes parses a synthetic security body carrying the finding
// count. A close with no count carries no block.
func equivFindingsOutcomes(findings int, given bool) (map[string]string, string) {
	body := []byte("the security actor gave no block\n")
	if given {
		body = []byte("```relevo\nfindings: " + strconv.Itoa(findings) + "\n```\n")
	}
	return ParseOutcomes(equivSecurityOutputs, body)
}

// checkEquiv asserts the new engine's transition matches the old one: the same
// send per member and seed, the same terminal status, the same no-op, and a
// halt reason equal to the old one or its listed rewording.
func checkEquiv(t *testing.T, tc equivCase, oldGot chain.State, oldAct chain.Action, ns, nstate State, nacts []Action) {
	t.Helper()
	if oldAct.Kind == chain.ActionNone {
		if len(nacts) != 0 {
			t.Fatalf("the old engine ignored the close but the new engine acted: %+v", nacts)
		}
		if !reflect.DeepEqual(nstate, ns) {
			t.Fatalf("the old engine ignored the close but the new state changed:\n got %+v\nwant %+v", nstate, ns)
		}
		return
	}
	if string(nstate.Status) != string(oldGot.Status) {
		t.Fatalf("status: the old engine is %s, the new engine is %s", oldGot.Status, nstate.Status)
	}
	last := Action{}
	if len(nacts) > 0 {
		last = nacts[len(nacts)-1]
	}
	switch oldAct.Kind {
	case chain.ActionSend:
		if last.Kind != ActionSend {
			t.Fatalf("the old engine sent to %s; the new engine's last action is %+v", oldAct.Member, last)
		}
		assertSendEquiv(t, oldAct, last)
		if nstate.Awaiting.Round != 0 {
			t.Fatalf("the new send did not clear the awaited round (%d)", nstate.Awaiting.Round)
		}
	case chain.ActionHalt:
		if last.Kind != ActionHalt {
			t.Fatalf("the old engine halted; the new engine's last action is %+v", last)
		}
		if nstate.Reason != oldAct.Reason && nstate.Reason != tc.reworded {
			t.Fatalf("halt reason: the old engine said %q, the new engine says %q (want it or %q)",
				oldAct.Reason, nstate.Reason, tc.reworded)
		}
	case chain.ActionStop:
		if last.Kind != ActionStop {
			t.Fatalf("the old engine stopped; the new engine's last action is %+v", last)
		}
	case chain.ActionFinish:
		if last.Kind != ActionFinish {
			t.Fatalf("the old engine finished; the new engine's last action is %+v", last)
		}
	}
}

// assertSendEquiv compares a new send to the old one by fixed part and seed. A
// builder send carries a single file reference where the old engine carried no
// seed at all.
func assertSendEquiv(t *testing.T, oldAct chain.Action, newAct Action) {
	t.Helper()
	wantMember, ok := equivStepMember[newAct.Step]
	if !ok {
		t.Fatalf("the new engine sent from an unmapped step %q", newAct.Step)
	}
	if wantMember != oldAct.Member {
		t.Fatalf("send member: the old engine sent to %s, the new step %s fills %s", oldAct.Member, newAct.Step, wantMember)
	}
	if string(oldAct.Seed) == "" {
		if !IsSingleRef(newAct.Seed) {
			t.Fatalf("builder seed: the old engine sent none, the new seed %q is not a single reference", newAct.Seed)
		}
		return
	}
	want, ok := equivSeedByOld[oldAct.Seed]
	if !ok || want != newAct.Seed {
		t.Fatalf("send seed: the old engine sent %s, the new engine sent %q (want %q)", oldAct.Seed, newAct.Seed, want)
	}
}

// eqScenario is a whole event script both engines are driven through.
type eqScenario struct {
	name     string
	plans    []string
	settings chain.Settings
	closes   []chain.Event
}

// eqBuilder is a builder close with only its outcome, gate and reason set.
func eqBuilder(outcome, gate, reason string) chain.Event {
	return chain.Event{Kind: chain.EventBuilderClosed, Outcome: outcome, Gate: gate, Reason: reason}
}

// eqReview is a reviewer close with only its verdict set.
func eqReview(v chain.Verdict) chain.Event {
	return chain.Event{Kind: chain.EventReviewerClosed, Verdict: v}
}

// eqPlan is a planner close with only its artifact presence set.
func eqPlan(present bool) chain.Event {
	return chain.Event{Kind: chain.EventPlannerClosed, PlanPresent: present}
}

// eqSecurity is a security close carrying a finding count.
func eqSecurity(findings int) chain.Event {
	return chain.Event{Kind: chain.EventSecurityClosed, Findings: findings, FindingsGiven: true}
}

// eqNeedsYou is a needs_you close.
func eqNeedsYou(reason string) chain.Event {
	return chain.Event{Kind: chain.EventNeedsYou, Reason: reason}
}

// eqSettings is the default workflow's settings with the given budget and scan.
func eqSettings(maxCorrections, regate int, security bool) chain.Settings {
	return chain.Settings{MaxCorrections: maxCorrections, Regate: regate, Security: security}
}

// equivScenarios drive both engines from their starts. The old engine has no
// start, so its script begins at its first builder send.
var equivScenarios = []eqScenario{
	{
		name:     "two plans pass without security",
		plans:    []string{"plan-1.md", "plan-2.md"},
		settings: eqSettings(3, 1, false),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
		},
	},
	{
		name:     "two plans with a clean scan",
		plans:    []string{"plan-1.md", "plan-2.md"},
		settings: eqSettings(3, 1, true),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
			eqSecurity(0),
		},
	},
	{
		name:     "findings, a fix, a correction, then a pass",
		plans:    []string{"plan-1.md"},
		settings: eqSettings(3, 1, true),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
			eqSecurity(2),
			eqPlan(true),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictChanges),
			eqPlan(true),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
		},
	},
	{
		name:     "corrections exhausted on the second plan after the first used two",
		plans:    []string{"plan-1.md", "plan-2.md"},
		settings: eqSettings(2, 1, false),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictChanges),
			eqPlan(true),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictChanges),
			eqPlan(true),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictChanges),
			eqPlan(true),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictChanges),
			eqPlan(true),
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictChanges),
		},
	},
	{
		name:     "a red gate with regate 1",
		plans:    []string{"plan-1.md"},
		settings: eqSettings(3, 1, false),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateRed, ""),
			eqReview(chain.VerdictPass),
		},
	},
	{
		name:     "a red gate with regate 0",
		plans:    []string{"plan-1.md"},
		settings: eqSettings(3, 0, false),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateRed, ""),
			eqReview(chain.VerdictPass),
		},
	},
	{
		name:     "a builder blocked mid-chain",
		plans:    []string{"plan-1.md", "plan-2.md"},
		settings: eqSettings(3, 1, false),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqReview(chain.VerdictPass),
			eqBuilder(reporttail.OutcomeBlocked, "", "out of quota"),
		},
	},
	{
		name:     "needs_you during review",
		plans:    []string{"plan-1.md"},
		settings: eqSettings(3, 1, false),
		closes: []chain.Event{
			eqBuilder(reporttail.OutcomeDone, chain.GateGreen, ""),
			eqNeedsYou("the harness asked for a decision"),
		},
	},
}

// eqTerminalStatus reports whether a chain status ends the run.
func eqTerminalStatus(s chain.Status) bool {
	return s == chain.StatusHalted || s == chain.StatusStopped || s == chain.StatusDone
}

// eqSend is a send reduced to its fixed part and its old seed spelling.
type eqSend struct {
	member string
	seed   string
}

// TestEquivalenceScenarios drives both engines through whole scripts and
// compares the send sequence per member and the terminal status. Repair rounds
// are internal to a builder close and are not compared.
func TestEquivalenceScenarios(t *testing.T) {
	for _, sc := range equivScenarios {
		t.Run(sc.name, func(t *testing.T) {
			runEquivScenario(t, sc)
		})
	}
}

// runEquivScenario plays one script through both engines.
func runEquivScenario(t *testing.T, sc eqScenario) {
	t.Helper()
	def := equivDef(t, sc.settings, "make check")

	oldState := chain.State{
		Status:   chain.StatusRunning,
		Phase:    chain.PhaseBuild,
		Step:     chain.StepBuilding,
		Plan:     1,
		Plans:    len(sc.plans),
		Settings: sc.settings,
		Awaiting: chain.Awaiting{Member: chain.MemberBuilder},
	}
	newState, start := Start(def, StartInputs{Plans: sc.plans})

	oldSends := []eqSend{{member: chain.MemberBuilder}}
	newSends := eqScenarioSends(t, start)
	round := 0
	for i, raw := range sc.closes {
		if eqTerminalStatus(oldState.Status) || eqTerminalStatus(chain.Status(newState.Status)) {
			t.Fatalf("close %d arrived after a terminal state", i+1)
		}
		round++
		e := raw
		e.Member = oldState.Awaiting.Member
		e.Round = round
		oldState.Awaiting.Round = round

		var oldAct chain.Action
		oldState, oldAct = chain.Next(oldState, e)
		switch oldAct.Kind {
		case chain.ActionSend:
			oldSends = append(oldSends, eqSend{member: oldAct.Member, seed: string(oldAct.Seed)})
		case chain.ActionNone:
			t.Fatalf("close %d: the old engine ignored the close", i+1)
		}

		if newState.Awaiting.Member != "" {
			newState.Awaiting.Round = round
		}
		var nacts []Action
		newState, nacts = equivReplay(t, def, newState, e, round)
		newSends = append(newSends, eqScenarioSends(t, nacts)...)
	}

	if got, want := chain.Status(newState.Status), oldState.Status; got != want {
		t.Fatalf("terminal status: the old engine is %s, the new engine is %s", want, got)
	}
	if len(newSends) != len(oldSends) {
		t.Fatalf("send count: the old engine sent %d times %v, the new engine %d times %v", len(oldSends), oldSends, len(newSends), newSends)
	}
	for i := range oldSends {
		if oldSends[i] != newSends[i] {
			t.Fatalf("send %d: the old engine sent %+v, the new engine sent %+v", i+1, oldSends[i], newSends[i])
		}
	}
}

// eqScenarioSends reduces a transition's actions to the sends that compare: a
// repair round is internal to a builder close and is dropped.
func eqScenarioSends(t *testing.T, acts []Action) []eqSend {
	t.Helper()
	var out []eqSend
	for _, a := range acts {
		if a.Kind != ActionSend || a.Step == "repair" || a.Step == "fix-repair" {
			continue
		}
		member, ok := equivStepMember[a.Step]
		if !ok {
			t.Fatalf("the new engine sent from an unmapped step %q", a.Step)
		}
		out = append(out, eqSend{member: member, seed: eqOldSeed(a.Seed)})
	}
	return out
}

// eqOldSeed is a new seed folded back to the old seed it replaces: a builder's
// single file reference carries no old seed.
func eqOldSeed(seed string) string {
	switch seed {
	case "shipped:review":
		return string(chain.SeedReviewer)
	case "shipped:correct":
		return string(chain.SeedCorrection)
	case "shipped:fix":
		return string(chain.SeedFixes)
	case "shipped:scan":
		return string(chain.SeedSecurity)
	}
	if IsSingleRef(seed) {
		return ""
	}
	return seed
}
