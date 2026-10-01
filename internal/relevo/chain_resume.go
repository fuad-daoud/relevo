package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ResumeOptions is what one `relevo chain --resume` was asked for: the chain to
// continue and the settings the flags override. A flag that was not given
// keeps the chain's stored setting, so a nil pointer and an empty actor name
// both read as "not given", the same rule bind's own resume uses.
type ResumeOptions struct {
	// Name is the chain to resume. A missing chain is refused.
	Name string
	// MaxCorrections overrides the stored chain.max_corrections; nil keeps it.
	MaxCorrections *int
	// ReviewerActor, PlannerActor and SecurityActor override the stored actor
	// each part runs; "" keeps it.
	ReviewerActor, PlannerActor, SecurityActor string
	// Security overrides the stored chain.security; nil keeps it. Turning it on
	// for a chain that was started without the phase creates the security
	// member now.
	Security *bool
	// Gate is the builder member's new acceptance command; "" keeps the stored
	// one unless NoGate is set. NoGate clears it, winning over Gate exactly as
	// bind's --no-gate does.
	Gate   string
	NoGate bool
	// Regate is the builder member's new repair-round budget after a failing
	// gate; nil keeps the stored one.
	Regate *int
}

// resumeStep is the resume decision, as a pure function of the chain row and
// two round numbers: the seed the resume must apply. The zero seed is the
// builder's, which carries no seed at all -- the builder is handed the chain's
// own copy of the plan, exactly as every other send to it is.
//
//   - newestBuilderRound > lastEventRound: the builder closed a round the chain
//     never mapped -- a manual round sent while the chain was halted or stopped
//     -- so the resume reviews that round, and the seed is the reviewer's.
//   - otherwise: the chain re-runs the step it halted or was stopped on. A stop
//     during building, and a halt with no newer round, both re-send the plan.
//
// The step word decides the rest: reviewing seeds the reviewer, correcting
// seeds the correction planner, scanning seeds the security member and
// planning-fixes seeds the fixes planner.
func resumeStep(ch db.ChainRow, lastEventRound, newestBuilderRound int) chain.SeedKind {
	if newestBuilderRound > lastEventRound {
		return chain.SeedReviewer
	}
	switch chain.Step(ch.Step) {
	case chain.StepReviewing:
		return chain.SeedReviewer
	case chain.StepCorrecting:
		return chain.SeedCorrection
	case chain.StepScanning:
		return chain.SeedSecurity
	case chain.StepPlanningFixes:
		return chain.SeedFixes
	}
	return ""
}

// ChainResume continues a chain a human has looked at: `relevo chain --resume
// --name <n>`. The flags override the chain's stored settings (a flag that was
// not given keeps its setting), and the chain re-runs the step it halted or was
// stopped on -- or reviews a builder round that closed after the chain last
// heard from it, which is a manual round sent while the chain was down.
//
// It refuses a missing chain, a chain that is still running, and a chain that is
// done; a halted or stopped chain is what it is for. The resumed round is
// started through the chain's own sender, the new state and its one trace row
// are written in one transaction, and `corrections` for the current plan is
// reset to 0, because a human has looked at the plan.
func ChainResume(ctx context.Context, rt Runtime, opts ResumeOptions) (ChainResult, error) {
	c, err := rt.Store.Chain(opts.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ChainResult{}, fmt.Errorf("chain %s: %w", opts.Name, store.ErrNotFound)
	}
	if err != nil {
		return ChainResult{}, err
	}
	if chainOnServer(c) {
		return chainServerResume(ctx, rt, c, opts)
	}
	if err := resumeRefusal(c); err != nil {
		return ChainResult{}, err
	}
	// A remote builder's check is fixed at create: the wire has no route that
	// updates a served binding's gate, so a resume cannot change it. --regate
	// still travels, because the repair budget is the chain's own client-side
	// fact.
	if opts.Gate != "" || opts.NoGate {
		if err := resumeRemoteGateRefusal(rt, c); err != nil {
			return ChainResult{}, err
		}
	}
	set, err := resumeSettings(rt, c, opts)
	if err != nil {
		return ChainResult{}, err
	}

	var out ChainResult
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain(opts.Name)
		if err != nil {
			return err
		}
		// The refusal runs again under the lock: a chain that started running
		// between the read above and here is refused, not raced.
		if err := resumeRefusal(row); err != nil {
			return err
		}
		res, err := chainResumeLocked(ctx, rt, tx, row, set, opts)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		return ChainResult{}, err
	}
	// A resume whose target member is remote has only staged its round: the
	// unlocked step ships it now, outside the state lock, exactly as a start's
	// plan 1 is shipped. A failed ship records the halt and returns nil.
	if name := chainMemberName(out.Chain, out.Chain.AwaitingMember); name != "" {
		if b, lerr := rt.Store.Load(name); lerr == nil && b.Builder.Remote() {
			if serr := chainSendPending(ctx, rt); serr != nil {
				return ChainResult{}, fmt.Errorf("chain %s resumed, but its remote member could not be handed its round: %w", opts.Name, serr)
			}
		}
	}
	return out, nil
}

// resumeRemoteGateRefusal refuses a --gate/--no-gate on a resume whose builder
// member is remote: the served binding's check is fixed when the binding is
// created, and the wire has no route that updates it. A missing builder record
// is left to chainResumeLocked's own gate-flag error.
func resumeRemoteGateRefusal(rt Runtime, c db.ChainRow) error {
	if c.Builder == "" {
		return nil
	}
	b, err := rt.Store.Load(c.Builder)
	if err != nil {
		return nil
	}
	if !b.Builder.Remote() {
		return nil
	}
	return fmt.Errorf("chain %s: a remote builder's check is fixed at create; unbind and start again", c.Name)
}

// resumeRefusal is the one refusal a resume makes on its own chain: running and
// done are both terminal for this verb, and the wording is the chain's own.
func resumeRefusal(c db.ChainRow) error {
	switch chain.Status(c.Status) {
	case chain.StatusRunning:
		return fmt.Errorf("chain %s is running", c.Name)
	case chain.StatusDone:
		return fmt.Errorf("chain %s is done", c.Name)
	}
	return nil
}

// resumeSettings applies the resume's flags to the chain's stored settings: a
// flag that was given replaces the stored value, one that was not keeps it. The
// settings come off the chain row, never from today's policy: the row is what
// the chain has been running under. The gate flags are the exception: an
// explicit --gate or --no-gate resolves against today's policy exactly as a
// start would, because the human is naming the command now.
func resumeSettings(rt Runtime, c db.ChainRow, opts ResumeOptions) (chain.Settings, error) {
	var set chain.Settings
	if len(c.SettingsJSON) > 0 {
		if err := json.Unmarshal(c.SettingsJSON, &set); err != nil {
			return chain.Settings{}, fmt.Errorf("chain %s settings: %w", c.Name, err)
		}
	}
	if opts.MaxCorrections != nil {
		set.MaxCorrections = *opts.MaxCorrections
	}
	if opts.Gate != "" || opts.NoGate {
		set.Gate = resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), "builder"))
	}
	if opts.Regate != nil {
		set.Regate = resolveRegate(opts.Regate, rt.Policy)
	}
	if opts.ReviewerActor != "" {
		set.ReviewerActor = opts.ReviewerActor
	}
	if opts.PlannerActor != "" {
		set.PlannerActor = opts.PlannerActor
	}
	if opts.SecurityActor != "" {
		set.SecurityActor = opts.SecurityActor
	}
	if opts.Security != nil {
		set.Security = *opts.Security
	}
	return set, nil
}

// chainResumeLocked is the resume's work under the state lock: it decides which
// step to re-run, refuses a target member whose round is still open, settles the
// settings, creates the security member when the phase was just turned on,
// starts that round, and writes the new state and the resume's one trace row in
// a single transaction.
func chainResumeLocked(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, set chain.Settings, opts ResumeOptions) (ChainResult, error) {
	settingsJSON, err := json.Marshal(set)
	if err != nil {
		return ChainResult{}, fmt.Errorf("encode chain %s settings: %w", c.Name, err)
	}
	c.SettingsJSON = settingsJSON

	before, err := chainStateOf(c)
	if err != nil {
		return ChainResult{}, err
	}
	// The two rounds the decision reads: the builder's newest closed round,
	// and the newest round the chain itself mapped as a builder close.
	newest := memberNewestClosedRound(tx, c.Builder)
	last := chainLastBuilderRound(tx, c)
	seed := resumeStep(c, last, newest)

	act, targetPart := chainResumeAction(seed)
	memberName := chainMemberName(c, targetPart)
	if memberName == "" {
		return ChainResult{}, fmt.Errorf("chain %s has no %s member", c.Name, targetPart)
	}
	act.Member = targetPart
	step, closedRound := chainResumeWhere(tx, c, seed, newest)

	// The target member's own round must be closed before anything is written:
	// starting a new one would overwrite the round in flight. The refusal is
	// the same RoundOpenError the send path raises, so the CLI maps it to a
	// conflict and names `relevo stop <member>`.
	if err := resumeOpenRoundRefusal(tx, c, memberName); err != nil {
		return ChainResult{}, err
	}

	// The gate flags are settings, but they also live on the builder member.
	// Only the flags that were given are written, so a resume that names none
	// leaves the stored check untouched -- a chain started before the settings
	// existed must not have a member gate it never saw cleared. A gate flag on
	// a chain whose builder record is gone is an error: the resume fails with
	// nothing written rather than quietly skipping the update.
	if opts.Gate != "" || opts.NoGate || opts.Regate != nil {
		builder, err := tx.Load(c.Builder)
		if err != nil {
			return ChainResult{}, fmt.Errorf("chain %s has no builder member to update its check: %w", c.Name, err)
		}
		if opts.Gate != "" || opts.NoGate {
			builder.Gate = set.Gate
		}
		if opts.Regate != nil {
			builder.Regate = set.Regate
		}
		if err := tx.Save(builder); err != nil {
			return ChainResult{}, err
		}
	}

	// Turning security on for a chain that was started without the phase
	// creates the member now, through the helper a start builds its members
	// with, so the late member is shaped exactly like the others.
	if set.Security && c.Security == "" {
		name, err := chainCreateSecurityMember(ctx, rt, tx, c, set)
		if err != nil {
			return ChainResult{}, err
		}
		c.Security = name
	}

	next := before
	next.Status = chain.StatusRunning
	next.Reason = ""
	next.Step = step
	// A human has looked at the plan: the correction rounds it already spent
	// say nothing about the round the resume is about to run.
	next.Corrections = 0
	next.Settings = set
	// The send fills the round; until it does, the chain waits on no round.
	next.Awaiting = chain.Awaiting{Member: targetPart}

	text, err := chainResumeText(rt, tx, c, before, next, act, closedRound, opts.Gate != "" || opts.NoGate)
	if err != nil {
		return ChainResult{}, err
	}
	member, err := tx.Load(memberName)
	if err != nil {
		return ChainResult{}, err
	}
	sent, err := chainSendMember(ctx, rt, tx, member, text)
	if err != nil {
		// The member could not start: the chain stays halted, named with the
		// member's own reason, exactly as a failed advance leaves it.
		next.Status = chain.StatusHalted
		next.Reason = fmt.Sprintf("member %s could not start: %v", memberName, err)
		ev := chain.Event{Kind: chain.EventNeedsYou, Member: targetPart, Reason: next.Reason}
		halt := chain.Action{Kind: chain.ActionHalt, Reason: next.Reason}
		if serr := chainSaveWithTrace(rt, tx, c, before, next, ev, halt, memberName); serr != nil {
			return ChainResult{}, serr
		}
		return ChainResult{}, err
	}
	next.Awaiting.Round = sent.Round

	ev := chain.Event{Kind: chain.EventNeedsYou, Member: targetPart, Round: sent.Round, Reason: chain.ResumeReason(next.Step)}
	if err := chainSaveWithTrace(rt, tx, c, before, next, ev, act, memberName); err != nil {
		return ChainResult{}, err
	}

	members, err := chainLoadMembers(tx, c)
	if err != nil {
		return ChainResult{}, err
	}
	return ChainResult{Chain: chainRowWithState(c, next, rt.Now().UTC()), Members: members, Plans: c.Plans, Check: chainBuilderCheck(members, c.Builder)}, nil
}

// resumeOpenRoundRefusal refuses a resume whose target member's current round
// is still open: the round a resume would start would overwrite the one in
// flight, and the human must stop it first. It returns the send path's
// RoundOpenError, whose wording and CLI conflict mapping name `relevo stop
// <member>`. A member
// record that is missing or unreadable has no open round it can prove, and is
// left to the send's own failure.
func resumeOpenRoundRefusal(tx *store.Tx, c db.ChainRow, memberName string) error {
	member, err := tx.Load(memberName)
	if err != nil {
		return nil
	}
	entries, err := tx.ReadLog(memberName)
	if err != nil {
		return nil
	}
	if roundOpenIn(entries, member.Round) {
		return &RoundOpenError{Member: memberName, Round: member.Round}
	}
	return nil
}

// chainResumeText is the text a resumed send hands over. When the resume's
// action is a builder send and the chain was waiting on the builder's own
// nonzero round, the text is that round's staged prompt -- the exact bytes the
// stopped or halted round was handed, whether the plan copy, the planner's
// correction or fix text, or the repair prompt. Anything else, and a staged
// prompt that is gone, falls back to chainSeedText's plan copy, so a plain plan
// round reads exactly as it always did.
//
// gateChanged says the resume replaced the builder's check. A repair prompt
// names the check that failed and the failed round's gate log, so those bytes
// are wrong under a new check: the resume then walks back past the
// repair prompts to the round the step seeded and re-sends that -- the plan,
// correction or fix text -- under the new check.
func chainResumeText(rt Runtime, tx *store.Tx, c db.ChainRow, before, next chain.State, act chain.Action, closedRound int, gateChanged bool) (string, error) {
	if act.Member == chain.MemberBuilder &&
		before.Awaiting.Member == chain.MemberBuilder && before.Awaiting.Round > 0 {
		round := before.Awaiting.Round
		if gateChanged {
			for round > 1 {
				body, err := rt.Store.ReadFile(rt.Store.PromptPath(c.Builder, round))
				if err != nil || !isRepairPlan(string(body)) {
					break
				}
				round--
			}
		}
		if body, err := rt.Store.ReadFile(rt.Store.PromptPath(c.Builder, round)); err == nil {
			return string(body), nil
		}
	}
	return chainSeedText(rt, tx, c, next, act, closedRound, false)
}

// chainResumeAction is the action a resume's seed names: the member it sends to
// and the seed it renders. The zero seed is the builder's, which is handed the
// chain's own plan rather than a rendered seed.
func chainResumeAction(seed chain.SeedKind) (chain.Action, string) {
	switch seed {
	case chain.SeedReviewer:
		return chain.Action{Kind: chain.ActionSend, Seed: chain.SeedReviewer}, chain.MemberReviewer
	case chain.SeedCorrection, chain.SeedFixes:
		return chain.Action{Kind: chain.ActionSend, Seed: seed}, chain.MemberPlanner
	case chain.SeedSecurity:
		return chain.Action{Kind: chain.ActionSend, Seed: chain.SeedSecurity}, chain.MemberSecurity
	}
	return chain.Action{Kind: chain.ActionSend}, chain.MemberBuilder
}

// chainResumeWhere is the state a resumed chain takes: the step the re-run
// leaves it on, and the round whose artifacts its seed names. The round is the
// one the real transition would have passed -- the closing member's own -- so a
// seed built here points at the same files it pointed at before the halt.
func chainResumeWhere(tx *store.Tx, c db.ChainRow, seed chain.SeedKind, newest int) (chain.Step, int) {
	switch seed {
	case chain.SeedReviewer:
		return chain.StepReviewing, newest
	case chain.SeedCorrection:
		return chain.StepCorrecting, memberNewestClosedRound(tx, c.Reviewer)
	case chain.SeedSecurity:
		return chain.StepScanning, memberNewestClosedRound(tx, c.Reviewer)
	case chain.SeedFixes:
		return chain.StepPlanningFixes, memberNewestClosedRound(tx, c.Security)
	}
	return chain.StepBuilding, 0
}

// chainCreateSecurityMember creates the security member a resume turned on
// after the chain had started: the reader binding `<n>-sec`, the builder's
// tree, and the security actor, built through the same helper a start builds
// its members with. The binding is written before the chain row names it.
func chainCreateSecurityMember(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, set chain.Settings) (string, error) {
	builder, err := tx.Load(c.Builder)
	if err != nil {
		return "", fmt.Errorf("chain %s has no builder member to place the security member beside: %w", c.Name, err)
	}
	member := chainMember{
		part: chain.MemberSecurity, name: c.Name + "-sec",
		actor: set.SecurityActor, shape: store.ShapeReader,
	}
	if err := store.ValidName(member.name); err != nil {
		return "", err
	}
	if _, err := tx.Load(member.name); err == nil {
		return "", fmt.Errorf("binding %q already exists: `relevo unbind %s` first", member.name, member.name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	// A served builder's chain has no local mastermind and no local candidate
	// policy to consult: the security member is created beside it in the served
	// shape, the server picking its own candidate and tier.
	if builder.Owner != "" {
		if builder.Serve == nil {
			return "", fmt.Errorf("chain %s has a served builder with no serve facts", c.Name)
		}
		pick, err := servedActorPick(rt, member.actor)
		if err != nil {
			return "", err
		}
		facts := servedFacts{
			owner: builder.Owner, repoID: builder.Serve.RepoID, worktree: builder.CWD,
			bare: builder.Serve.BareRepo, base: builder.Base, feature: c.Feature, ticket: c.Ticket,
			authorName: builder.Serve.AuthorName, authorEmail: builder.Serve.AuthorEmail,
			now: rt.Now().UTC(),
		}
		if err := tx.Save(servedChainMember(member, pick, facts)); err != nil {
			return "", err
		}
		return member.name, nil
	}
	resolutions, err := chainResolveActors(rt, []chainMember{member})
	if err != nil {
		return "", err
	}
	base := chainBase{
		cwd: builder.CWD, mastermind: builder.MasterMind, mastermindID: c.MasterMindID,
		repo: c.Repo, repoRef: builder.RepoRef, feature: c.Feature, ticket: c.Ticket,
		worktree: builder.CWD,
	}
	built, err := chainBuildMembers(ctx, rt, []chainMember{member}, resolutions, base, set)
	if err != nil {
		return "", err
	}
	if err := tx.Save(built[0]); err != nil {
		return "", err
	}
	return member.name, nil
}

// memberNewestClosedRound is a member's newest closed round: the highest round
// its log holds a report entry for, or 0 when the record is gone or no round
// has closed. It is the round a resume's seed points its artifacts at.
func memberNewestClosedRound(tx *store.Tx, name string) int {
	if name == "" {
		return 0
	}
	entries, err := tx.ReadLog(name)
	if err != nil {
		return 0
	}
	newest := 0
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round > newest {
			newest = e.Round
		}
	}
	return newest
}

// chainLastBuilderRound is the newest round the chain itself has seen from the
// builder: the round it is waiting on when it waits on the builder, and the
// newest builder_closed round in its trace. A builder round that closed after
// both is a manual round the chain never mapped -- a send a human made while the
// chain was halted or stopped -- which is the round a resume reviews.
//
// The awaiting round is part of it because a stopped builder's close raises a
// `stopped` event rather than a builder_closed one: without it, a chain stopped
// while building would read its own round as a manual one and review it.
func chainLastBuilderRound(tx *store.Tx, c db.ChainRow) int {
	last := 0
	if c.AwaitingMember == chain.MemberBuilder {
		last = c.AwaitingRound
	}
	events, err := tx.ChainEvents(c.Name)
	if err != nil {
		return last
	}
	for _, e := range events {
		ev, derr := chain.DecodeEvent(e.Event)
		if derr != nil {
			continue
		}
		if ev.Kind == chain.EventBuilderClosed && e.Round > last {
			last = e.Round
		}
	}
	return last
}

// chainLoadMembers reads a chain's member bindings back, in part order.
func chainLoadMembers(tx *store.Tx, c db.ChainRow) ([]store.Binding, error) {
	out := make([]store.Binding, 0, len(chainMembersOf(c)))
	for _, name := range chainMembersOf(c) {
		b, err := tx.Load(name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}
