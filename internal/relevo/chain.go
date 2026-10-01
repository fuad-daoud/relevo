package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrRunningChainMember reports a manual send refused because the binding is a
// member of a chain that is still running. The chain owns the member's rounds
// until it halts, stops or finishes; after that a manual send works again.
var ErrRunningChainMember = errors.New("a running chain owns this binding")

// refuseRunningChainMember is the refusal a manual send gets when name belongs
// to a chain with status running. A name that is no chain member -- or whose
// chain is terminal -- passes. It is read-only: it takes the caller's tx, so a
// send under the lock can call it, and refuseRunningChainMemberStore is its
// store-reading twin for the lock-free preflight.
func refuseRunningChainMember(tx *store.Tx, name string) error {
	c, err := tx.ChainByMember(name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return runningChainRefusal(name, c)
}

// refuseRunningChainMemberStore is refuseRunningChainMember's read-only twin
// for the preflight, which holds no tx.
func refuseRunningChainMemberStore(s *store.Store, name string) error {
	c, err := s.ChainByMember(name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return runningChainRefusal(name, c)
}

// runningChainRefusal is the one wording, shared by the store and tx checks:
// only a chain in status running refuses; a halted, stopped or done chain has
// handed its member back.
func runningChainRefusal(name string, c db.ChainRow) error {
	if c.Status != string(chain.StatusRunning) {
		return nil
	}
	return fmt.Errorf("binding %q belongs to running chain %s; relevo stop %s first: %w", name, c.Name, c.Name, ErrRunningChainMember)
}

// chainOwnsMember reports whether name is one of any chain's member bindings.
// The wiring owns a chain member's red gate and its repair rounds, so the
// ordinary paths ask this before acting on their own.
func chainOwnsMember(tx *store.Tx, name string) bool {
	_, err := tx.ChainByMember(name)
	return err == nil
}

// chainPartOf names the part a member fills, from the chain row's columns.
func chainPartOf(c db.ChainRow, name string) string {
	if name == "" {
		return ""
	}
	switch name {
	case c.Builder:
		return chain.MemberBuilder
	case c.Reviewer:
		return chain.MemberReviewer
	case c.Planner:
		return chain.MemberPlanner
	case c.Security:
		return chain.MemberSecurity
	}
	return ""
}

// chainMemberName is chainPartOf's inverse: the binding name the row holds for
// a part. "" when the part is not on this chain (the security member of a chain
// whose phase is off).
func chainMemberName(c db.ChainRow, part string) string {
	switch part {
	case chain.MemberBuilder:
		return c.Builder
	case chain.MemberReviewer:
		return c.Reviewer
	case chain.MemberPlanner:
		return c.Planner
	case chain.MemberSecurity:
		return c.Security
	}
	return ""
}

// chainStateOf is a chain row as the pure state machine's State.
func chainStateOf(c db.ChainRow) (chain.State, error) {
	var set chain.Settings
	if len(c.SettingsJSON) > 0 {
		if err := json.Unmarshal(c.SettingsJSON, &set); err != nil {
			return chain.State{}, fmt.Errorf("chain %s settings: %w", c.Name, err)
		}
	}
	return chain.State{
		Status:      chain.Status(c.Status),
		Reason:      c.Reason,
		Phase:       chain.Phase(c.Phase),
		Step:        chain.Step(c.Step),
		Plan:        c.Plan,
		Plans:       c.Plans,
		Corrections: c.Corrections,
		Awaiting:    chain.Awaiting{Member: c.AwaitingMember, Round: c.AwaitingRound},
		Settings:    set,
	}, nil
}

// chainPlanPaths decodes the chain's stored plan copies.
func chainPlanPaths(c db.ChainRow) ([]string, error) {
	if len(c.PlanPathsJSON) == 0 {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal(c.PlanPathsJSON, &paths); err != nil {
		return nil, fmt.Errorf("chain %s plan paths: %w", c.Name, err)
	}
	return paths, nil
}

// chainRowWithState is c carrying the state machine's state.
func chainRowWithState(c db.ChainRow, s chain.State, now time.Time) db.ChainRow {
	c.Status = string(s.Status)
	c.Reason = s.Reason
	c.Phase = string(s.Phase)
	c.Step = string(s.Step)
	c.Plan = s.Plan
	c.Plans = s.Plans
	c.Corrections = s.Corrections
	c.AwaitingMember = s.Awaiting.Member
	c.AwaitingRound = s.Awaiting.Round
	c.UpdatedAt = now
	return c
}

// chainEventFromClose maps one member close onto the event the state machine
// reads. part names the closing member's part; b is the closing binding and
// body is its in-memory output (before a reader's block is stripped). outcome
// and gate describe a builder's close; tail is the parsed report tail and note
// the close's own note, both of which a non-done builder close folds into its
// one-line reason. stopped is the caller's fact that the close was a stop.
func chainEventFromClose(rt Runtime, part string, b store.Binding, body []byte, outcome string, gate *store.GateRecord, stopped bool, tail reporttail.Tail, note string) chain.Event {
	// The event names the member by its part: that is the vocabulary the
	// state machine's Awaiting column stores, and Next compares against it.
	ev := chain.Event{Member: part, Round: b.Round}
	if stopped {
		ev.Kind = chain.EventStopped
		return ev
	}
	switch part {
	case chain.MemberBuilder:
		ev.Kind = chain.EventBuilderClosed
		ev.Outcome = outcome
		ev.Gate = chainGateResult(gate)
		// A builder close that is not done carries why in one line from its
		// report tail. A done close carries no reason, and every other part's
		// event is left as it was.
		if outcome != reporttail.OutcomeDone {
			ev.Reason = chain.BuilderHaltReason(tail, note, outcome)
		}
	case chain.MemberReviewer:
		ev.Kind = chain.EventReviewerClosed
		ev.Verdict = chainReaderVerdict(rt, b, body)
	case chain.MemberPlanner:
		ev.Kind = chain.EventPlannerClosed
		if size, _, ok, err := rt.Store.StatFile(reportPathFor(rt, b)); err == nil && ok && size > 0 {
			ev.PlanPresent = true
		}
	case chain.MemberSecurity:
		ev.Kind = chain.EventSecurityClosed
		ev.Findings, ev.FindingsGiven = chainReaderFindings(rt, b, body)
	}
	return ev
}

// chainGateResult is the builder close's gate word: none when no check ran,
// green when one passed, red otherwise. A red gate has already spent the
// regate budget, so it still reaches the reviewer rather than a halt.
func chainGateResult(gate *store.GateRecord) string {
	if gate == nil {
		return chain.GateNone
	}
	if gate.Result == "pass" {
		return chain.GateGreen
	}
	return chain.GateRed
}

// chainApply advances the chain a closing member belongs to: it loads the
// chain with the caller's tx, runs the pure transition and performs the action
// in the same critical section as the close. A close that is not the awaited
// one -- or that arrives once the chain has stopped -- changes nothing and
// writes no trace row. gate is the closing round's gate record when one ran;
// a builder's red gate may be spent on a repair instead of an event.
//
// It returns the closing binding as the caller should save it: every arm
// returns the value it wrote, so a remote member's staged repair -- which sets
// the chain's own bookkeeping on the member record -- is what the close's
// caller persists.
func chainApply(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, ev chain.Event, gate *store.GateRecord) (store.Binding, error) {
	c, err := tx.ChainByMember(b.Name)
	if errors.Is(err, store.ErrNotFound) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	// A chain that runs on a server is advanced by that server's daemon, not
	// by a close here: this machine mirrors it, and the chain pull is what
	// moves the mirror.
	if chainOnServer(c) {
		return b, nil
	}

	before, err := chainStateOf(c)
	if err != nil {
		return b, err
	}
	// Next owns the two refusals: it ignores a close that does not name the
	// awaited (member, round), and it ignores every close once the chain is
	// terminal -- which is what makes the end delivery happen exactly once.
	next, act := chain.Next(before, ev)
	if act.Kind == chain.ActionNone {
		return b, nil
	}

	// A builder's red gate with a repair still in budget is not a chain
	// transition: the chain raises no event and writes no trace row, it only
	// waits again for the same member's new round. The close has already
	// advanced the binding, so b.Round is that repair round's number, and the
	// wiring's later half opens the round on this same decision.
	//
	// Only a done round may buy a repair: the gate runs on the done marker
	// whatever the report says, so a builder that reports halted, blocked or
	// unstructured with a red gate is the chain's halt, exactly as the design
	// says ("builder halted on plan i"), never a repair round.
	if ev.Kind == chain.EventBuilderClosed && ev.Gate == chain.GateRed && ev.Outcome == reporttail.OutcomeDone && gate != nil {
		sig := gateSignature(rt.Store.ReadFile, gate.LogPath)
		if ok, _ := repairDecision(b, sig); ok {
			next = before
			next.Awaiting = chain.Awaiting{Member: chain.MemberBuilder, Round: b.Round}
			// A remote member has no local wiring half to open its repair
			// round: the served binding never opens one, so the chain stages
			// the repair here, on the client's own member record, and the
			// pending-send step ships it. The staged text itself carries the
			// repair fact, so the step needs no repair wording of its own.
			if b.Builder.Remote() {
				text := repairPlan(b, ev.Round, rt.Store.PromptPath(b.Name, ev.Round), gate.LogPath,
					tailLines(rt.Store.ReadFile, gate.LogPath, repairTailLines))
				staged, serr := chainStageRemote(ctx, rt, b, text)
				if serr != nil {
					return b, serr
				}
				b = staged
				b.LastGateSig = sig
				b.RepairCount++
				if serr := tx.Save(b); serr != nil {
					return b, serr
				}
			}
			return b, tx.ChainPut(chainRowWithState(c, next, rt.Now().UTC()))
		}
	}

	if act.Kind != chain.ActionSend {
		return b, chainTerminal(ctx, rt, tx, c, before, next, ev, act, b.Name)
	}

	// The seed reads the closing member's own record -- the builder's closed
	// tree, for the plan's cumulative diff -- so persist the advanced round
	// before building it. The caller saves the same binding after this critical
	// section; doing it here just makes it visible to the seed's own reads.
	if err := tx.Save(b); err != nil {
		return b, err
	}
	text, err := chainSeedText(rt, tx, c, next, act, ev.Round, ev.Kind == chain.EventPlannerClosed)
	if err != nil {
		return b, err
	}
	memberName := chainMemberName(c, act.Member)
	if memberName == "" {
		return b, fmt.Errorf("chain %s has no %s member", c.Name, act.Member)
	}
	member, err := tx.Load(memberName)
	if err != nil {
		return b, err
	}
	// One start-or-stage helper every send to a member goes through: a local
	// member's round starts here, a remote member's is staged for the
	// pending-send step to ship.
	sent, err := chainSendMember(ctx, rt, tx, member, text)
	if err != nil {
		// The member could not start. The chain halts in the same critical
		// section; the helper has already left the member NEEDS YOU.
		next.Status = chain.StatusHalted
		next.Reason = fmt.Sprintf("member %s could not start: %v", memberName, err)
		return b, chainTerminal(ctx, rt, tx, c, before, next, ev, chain.Action{Kind: chain.ActionHalt, Reason: next.Reason}, b.Name)
	}
	// The member's own round is the round the chain now awaits; it is never 0.
	next.Awaiting.Round = sent.Round
	// A send that starts a plan records the commit the plan began at, so its
	// review can diff the plan's whole span (plan-start commit -> the closing
	// round's tree). A reviewer's pass onto the next plan, and the security
	// phase's fix plan, both start one; a correction, a repair and a resume
	// re-send do not, and keep the commit already recorded.
	if before.Step == chain.StepReviewing && next.Plan != before.Plan {
		c.PlanStartCommit = sent.RoundBaselineHead
	}
	if before.Step == chain.StepPlanningFixes {
		c.PlanStartCommit = sent.RoundBaselineHead
	}
	return b, chainSaveWithTrace(rt, tx, c, before, next, ev, act, b.Name)
}

// chainSeedText is the prompt a send action hands over. A builder send with
// fromPlanner set gets the planning member's own plan for the round that closed
// -- the correction or fix plan the planner wrote -- and the chain's own copy
// of the plan otherwise; every other member gets its rendered seed.
func chainSeedText(rt Runtime, tx *store.Tx, c db.ChainRow, s chain.State, act chain.Action, closedRound int, fromPlanner bool) (string, error) {
	if act.Member == chain.MemberBuilder && fromPlanner {
		path, err := memberReportPath(tx, c.Planner, closedRound)
		if err != nil {
			return "", err
		}
		body, err := rt.Store.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	paths, err := chainPlanPaths(c)
	if err != nil {
		return "", err
	}
	if act.Member == chain.MemberBuilder {
		if s.Plan < 1 || s.Plan > len(paths) {
			return "", fmt.Errorf("chain %s has no plan %d of %d", c.Name, s.Plan, len(paths))
		}
		body, err := rt.Store.ReadFile(paths[s.Plan-1])
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	v, err := chainSeedView(rt, tx, c, s, act, closedRound)
	if err != nil {
		return "", err
	}
	return chain.Seed(act.Seed, v)
}

// chainSeedView builds one send's inputs: the plan copies the chain holds and
// the members' artifacts for the round that just closed. Every path it names is
// made seed-openable by chainSeedInput -- the input's own path when the store
// reads it as itself, a copy under the chain's directory otherwise, and a
// path-free "not available" clause when it cannot be produced -- so a seed
// never names a round_file key a runner cannot open. The reviewer's and
// correction seeds name the builder round their closing event judged -- the
// closing round itself when the builder closed it, the builder's newest closed
// round when the reviewer did -- and carry that round's gate, the plan's
// cumulative diff and the round's own prompt when it differs from the plan
// copy. The correction and fixes seeds name a reader's own output file rather
// than the flat NNN-report.md no reader writes. The security and fixes seeds
// name the whole branch diff -- the chain's base to the builder's newest closed
// round -- and render no gate.
func chainSeedView(rt Runtime, tx *store.Tx, c db.ChainRow, s chain.State, act chain.Action, closedRound int) (chain.SeedView, error) {
	v := chain.SeedView{
		Plan: s.Plan, Plans: s.Plans, Corrections: s.Corrections,
		Branch: c.Branch, Base: c.Base,
	}
	chainSeedPlanView(rt, c, s.Plan, &v)

	// The correction and fixes seeds name a reader's own output: the path its
	// report entry carries, never the flat NNN-report.md no reader writes. A
	// lookup that fails is worded like any other missing input, never a failed
	// close.
	reviewerOutput, securityOutput := "", ""
	switch act.Seed {
	case chain.SeedCorrection:
		if p, err := memberReportPath(tx, c.Reviewer, closedRound); err != nil {
			reviewerOutput = chainSeedMissing(err)
		} else {
			reviewerOutput = chainSeedInput(rt, c, p)
		}
	case chain.SeedFixes:
		if p, err := memberReportPath(tx, c.Security, closedRound); err != nil {
			securityOutput = chainSeedMissing(err)
		} else {
			securityOutput = chainSeedInput(rt, c, p)
		}
	}

	switch act.Seed {
	case chain.SeedReviewer, chain.SeedCorrection:
		// The builder round the event judged: the reviewer's own close judged
		// the builder's round `closedRound`; a correction is seeded from the
		// reviewer's close, so it names the builder's newest closed round.
		builderRound := closedRound
		if act.Seed == chain.SeedCorrection {
			builderRound = memberNewestClosedRound(tx, c.Builder)
		}
		v.ReportPath = chainSeedInput(rt, c, rt.Store.ReportPath(c.Builder, builderRound))
		v.DiffPath = chainSeedInput(rt, c, rt.Store.DiffPath(c.Builder, builderRound))
		v.BranchDiffPath = v.DiffPath
		rec, err := chainRoundGate(tx, c.Builder, builderRound)
		if err != nil {
			return chain.SeedView{}, err
		}
		if rec != nil {
			v.GateResult = chainGateResult(rec)
			v.GateLogPath = chainSeedInput(rt, c, rec.LogPath)
		}
		v.PlanDiffPath = chainSeedInput(rt, c, chainPlanDiff(rt, tx, c, c.Builder, builderRound))
		v.RoundPromptPath = chainSeedInput(rt, c, chainRoundPromptPath(rt, v.PlanPath, c.Builder, builderRound))
		v.BuilderRoundKind, v.BuilderRoundOn, v.BuilderRounds, v.DiffFrom =
			chainBuilderPlanView(rt, tx, c, s, act, c.Builder, builderRound)
		if act.Seed == chain.SeedCorrection {
			v.OutputPath = reviewerOutput
		}
	case chain.SeedSecurity, chain.SeedFixes:
		if act.Seed == chain.SeedFixes {
			v.OutputPath = securityOutput
		}
		v.BranchDiffPath = chainSeedInput(rt, c,
			chainBranchDiff(rt, tx, c, c.Builder, memberNewestClosedRound(tx, c.Builder)))
	}
	return v, nil
}

// chainRoundGate reads the gate the builder's round wrote on its report entry:
// the KindReport entry for round in the builder's log, or nil when the round
// ran no check. The close writes that record on the entry, so a read under the
// same lock sees it.
func chainRoundGate(tx *store.Tx, builder string, round int) (*store.GateRecord, error) {
	entries, err := tx.ReadLog(builder)
	if err != nil {
		return nil, err
	}
	var rec *store.GateRecord
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round {
			rec = e.Gate
		}
	}
	return rec, nil
}

// chainSaveWithTrace writes the chain's new state and its one trace row in one
// database transaction. The trace names the phase and step before the event,
// the closing member and its round, the encoded event and action, and the halt
// reason when there is one.
func chainSaveWithTrace(rt Runtime, tx *store.Tx, c db.ChainRow, before, next chain.State, ev chain.Event, act chain.Action, closing string) error {
	now := rt.Now().UTC()
	row := chainRowWithState(c, next, now)
	trace := db.ChainEventRow{
		TS:     now,
		Phase:  string(before.Phase),
		Step:   string(before.Step),
		Member: closing,
		Round:  ev.Round,
		Plan:   before.Plan,
		Event:  ev.Encode(),
		Action: act.Encode(),
		Reason: act.Reason,
	}
	return tx.ChainSaveWithEvent(row, trace)
}

// chainTerminal ends a chain: it writes the status, the reason and the trace
// row, then queues exactly one end delivery on the first member whose record
// still exists. The transition to a terminal status happens once -- a later
// tick's close is ignored by the state machine -- so the payload exists once.
//
// The delivery does not belong to the builder: a chain whose builder record is
// the one that is gone must still tell the MasterMind, so the carrier is the
// first of builder, reviewer, planner and security that the store still holds.
// When every record is gone there is nowhere to deliver: the row is still ended
// and the fact is logged.
func chainTerminal(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, before, next chain.State, ev chain.Event, act chain.Action, closing string) error {
	if err := chainSaveWithTrace(rt, tx, c, before, next, ev, act, closing); err != nil {
		return err
	}
	// A done or stopped chain has no further use for its input copies; a
	// halted one keeps them. The sweep is best-effort and never fails the
	// transition.
	chainInputsSweep(rt, c.Name, string(next.Status))
	member, carrier, ok, err := chainDeliveryMember(tx, c)
	if err != nil {
		return err
	}
	findings, err := chainFindings(tx, c.Name)
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("chain ended with no member record to deliver on",
			"chain", c.Name, "status", string(next.Status))
		return nil
	}
	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: carrier.Round,
		Direction: store.DirToMasterMind, Kind: store.KindChain,
		Payload: chainTerminalPayload(c, next, findings),
	}
	if chainServedCarrier(carrier) {
		return nil
	}
	return delivery.Queue(ctx, deliveryDeps(rt), tx, member, entry)
}

// chainDeliveryMember names the member that carries a chain's end delivery: the
// first of the chain's member bindings whose record still exists, in the order
// builder, reviewer, planner, security. ok is false when every record is gone,
// which is the one case a chain ends silently. A read failure that is not
// "gone" is returned rather than treated as a missing member.
func chainDeliveryMember(tx *store.Tx, c db.ChainRow) (name string, b store.Binding, ok bool, err error) {
	for _, member := range chainMembersOf(c) {
		mb, lerr := tx.Load(member)
		if errors.Is(lerr, store.ErrNotFound) {
			continue
		}
		if lerr != nil {
			return "", store.Binding{}, false, lerr
		}
		return member, mb, true, nil
	}
	return "", store.Binding{}, false, nil
}

// chainFindings is the last security close's finding count from the trace. The
// chain row carries no findings column; a chain that never ran security
// carries 0.
func chainFindings(tx *store.Tx, name string) (int, error) {
	events, err := tx.ChainEvents(name)
	if err != nil {
		return 0, err
	}
	for _, e := range events {
		ev, err := chain.DecodeEvent(e.Event)
		if err != nil {
			continue
		}
		if ev.Kind == chain.EventSecurityClosed {
			return ev.Findings, nil
		}
	}
	return 0, nil
}

// chainTerminalPayload is what the mastermind reads when a chain ends: how it
// ended, where it got to, and the command that resumes it (on a halt) or shows
// its trace.
func chainTerminalPayload(c db.ChainRow, s chain.State, findings int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "chain %s %s: status %s, phase %s, plan %d/%d, corrections %d, findings %d.",
		c.Name, chainTerminalVerb(s.Status), string(s.Status), string(s.Phase), s.Plan, s.Plans, s.Corrections, findings)
	if s.Reason != "" {
		fmt.Fprintf(&b, " Reason: %s.", s.Reason)
	}
	if s.Status == chain.StatusHalted {
		fmt.Fprintf(&b, " relevo chain --resume --name %s resumes it.", c.Name)
	}
	fmt.Fprintf(&b, " Branch %s. relevo show %s --trace", c.Branch, c.Name)
	return b.String()
}

// chainTerminalVerb is the status as a verb for the payload's first clause.
func chainTerminalVerb(s chain.Status) string {
	switch s {
	case chain.StatusDone:
		return "finished"
	case chain.StatusHalted:
		return "halted"
	case chain.StatusStopped:
		return "stopped"
	}
	return string(s)
}
