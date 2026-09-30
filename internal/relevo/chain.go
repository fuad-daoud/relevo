package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
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
// and gate describe a builder's close. stopped is the caller's fact that the
// close was a stop.
func chainEventFromClose(rt Runtime, part string, b store.Binding, body []byte, outcome string, gate *store.GateRecord, stopped bool) chain.Event {
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
	case chain.MemberReviewer:
		ev.Kind = chain.EventReviewerClosed
		ev.Verdict = chain.ParseVerdict(body)
	case chain.MemberPlanner:
		ev.Kind = chain.EventPlannerClosed
		if size, _, ok, err := rt.Store.StatFile(reportPathFor(rt, b)); err == nil && ok && size > 0 {
			ev.PlanPresent = true
		}
	case chain.MemberSecurity:
		ev.Kind = chain.EventSecurityClosed
		ev.Findings, ev.FindingsGiven = chain.ParseFindings(body)
	}
	return ev
}

// chainGateResult is the builder close's gate word: green when no gate ran or
// it passed, red otherwise. A red gate has already spent the regate budget, so
// it still reaches the reviewer rather than a halt.
func chainGateResult(gate *store.GateRecord) string {
	if gate == nil || gate.Result == "pass" {
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
func chainApply(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, ev chain.Event, gate *store.GateRecord) error {
	c, err := tx.ChainByMember(b.Name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	before, err := chainStateOf(c)
	if err != nil {
		return err
	}
	// Next owns the two refusals: it ignores a close that does not name the
	// awaited (member, round), and it ignores every close once the chain is
	// terminal -- which is what makes the end delivery happen exactly once.
	next, act := chain.Next(before, ev)
	if act.Kind == chain.ActionNone {
		return nil
	}

	// A builder's red gate with a repair still in budget is not a chain
	// transition: the chain raises no event and writes no trace row, it only
	// waits again for the same member's new round. The close has already
	// advanced the binding, so b.Round is that repair round's number, and the
	// wiring's later half opens the round on this same decision.
	if ev.Kind == chain.EventBuilderClosed && ev.Gate == chain.GateRed && gate != nil {
		if ok, _ := repairDecision(b, gateSignature(rt.Store.ReadFile, gate.LogPath)); ok {
			next = before
			next.Awaiting = chain.Awaiting{Member: chain.MemberBuilder, Round: b.Round}
			return tx.ChainPut(chainRowWithState(c, next, rt.Now().UTC()))
		}
	}

	if act.Kind != chain.ActionSend {
		return chainTerminal(ctx, rt, tx, c, before, next, ev, act, b.Name)
	}

	text, err := chainSeedText(rt, c, next, act, ev.Round)
	if err != nil {
		return err
	}
	memberName := chainMemberName(c, act.Member)
	if memberName == "" {
		return fmt.Errorf("chain %s has no %s member", c.Name, act.Member)
	}
	member, err := tx.Load(memberName)
	if err != nil {
		return err
	}
	sent, err := sendChainRound(ctx, rt, tx, member, text)
	if err != nil {
		// The member could not start. The chain halts in the same critical
		// section; sendChainRound has already left the member NEEDS YOU.
		next.Status = chain.StatusHalted
		next.Reason = fmt.Sprintf("member %s could not start: %v", memberName, err)
		return chainTerminal(ctx, rt, tx, c, before, next, ev, chain.Action{Kind: chain.ActionHalt, Reason: next.Reason}, b.Name)
	}
	// The member's own round is the round the chain now awaits; it is never 0.
	next.Awaiting.Round = sent.Round
	return chainSaveWithTrace(rt, tx, c, before, next, ev, act, b.Name)
}

// chainSeedText is the prompt a send action hands over: a builder gets the
// chain's own copy of the plan the transition advanced to, every other member
// gets its rendered seed.
func chainSeedText(rt Runtime, c db.ChainRow, s chain.State, act chain.Action, closedRound int) (string, error) {
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
	return chain.Seed(act.Seed, chainSeedView(rt, c, s, act, closedRound))
}

// chainSeedView builds one send's inputs: the plan copies the chain holds and
// the members' artifacts for the round that just closed.
func chainSeedView(rt Runtime, c db.ChainRow, s chain.State, act chain.Action, closedRound int) chain.SeedView {
	v := chain.SeedView{
		Plan: s.Plan, Plans: s.Plans, Corrections: s.Corrections,
		Branch: c.Branch, Base: c.Base,
	}
	if paths, err := chainPlanPaths(c); err == nil && s.Plan >= 1 && s.Plan <= len(paths) {
		v.PlanPath = paths[s.Plan-1]
	}
	v.ReportPath = rt.Store.ReportPath(c.Builder, closedRound)
	v.DiffPath = rt.Store.DiffPath(c.Builder, closedRound)
	v.BranchDiffPath = v.DiffPath
	switch act.Seed {
	case chain.SeedCorrection:
		v.OutputPath = rt.Store.ReportPath(c.Reviewer, closedRound)
	case chain.SeedSecurity, chain.SeedFixes:
		v.OutputPath = rt.Store.ReportPath(c.Security, closedRound)
	}
	return v
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
		Event:  ev.Encode(),
		Action: act.Encode(),
		Reason: act.Reason,
	}
	return tx.ChainSaveWithEvent(row, trace)
}

// chainTerminal ends a chain: it writes the status, the reason and the trace
// row, then queues exactly one end delivery on the builder member. The
// transition to a terminal status happens once -- a later tick's close is
// ignored by the state machine -- so the payload exists once.
func chainTerminal(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, before, next chain.State, ev chain.Event, act chain.Action, closing string) error {
	if err := chainSaveWithTrace(rt, tx, c, before, next, ev, act, closing); err != nil {
		return err
	}
	builder, err := tx.Load(c.Builder)
	if err != nil {
		return err
	}
	findings, err := chainFindings(tx, c.Name)
	if err != nil {
		return err
	}
	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: builder.Round,
		Direction: store.DirToMasterMind, Kind: store.KindChain,
		Payload: chainTerminalPayload(c, next, findings),
	}
	return delivery.Queue(ctx, deliveryDeps(rt), tx, c.Builder, entry)
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
