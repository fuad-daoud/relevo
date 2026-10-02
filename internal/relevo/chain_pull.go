package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainPullMaxPlanBytes bounds one pulled round prompt. The server caps a
// chain's plans when it creates one, so a body past this is not a prompt and
// is refused rather than held whole.
const chainPullMaxPlanBytes = 1 << 20

// chainPullServers pulls every mirror chain that is not done. It runs without
// the state lock and reads the server itself; each member's install and the
// final row update take their own short locks. A chain's failure is joined
// with the others', so one gone or unreachable server cannot stop the rest.
func chainPullServers(ctx context.Context, rt Runtime) error {
	if rt.Remote == nil {
		return nil
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range chains {
		if !chainOnServer(c) || c.Status == string(chain.StatusDone) || chainGoneMirror(c) {
			continue
		}
		if err := chainPullOne(ctx, rt, c); err != nil {
			errs = append(errs, fmt.Errorf("chain %s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}

// chainPullOne moves one mirror chain a pass forward: it reads the server's
// view, names every member the server added, installs each member's missing
// closed rounds in order, acks them, and copies the view's state onto the
// mirror. A chain the server no longer holds halts the mirror once.
func chainPullOne(ctx context.Context, rt Runtime, c db.ChainRow) error {
	if rt.Remote == nil {
		return nil
	}
	view, err := rt.Remote.GetChain(ctx, c.Server, c.Name)
	if err != nil {
		if is404(err) {
			return chainPullGone(ctx, rt, c)
		}
		return err
	}
	c, err = chainPullMembers(rt, c, view)
	if err != nil {
		return err
	}
	for _, mv := range view.Members {
		if err := chainPullMember(ctx, rt, mv); err != nil {
			return err
		}
	}
	return chainPullFinish(ctx, rt, c, view)
}

// chainPullGone ends a mirror whose chain the server no longer knows: it halts
// the row once, with the chain's absence as the reason, and queues the one end
// delivery. A mirror that is already terminal is left alone, so a repeated 404
// queues nothing more; a mirror already halted is stamped with the gone reason
// so the walk stops reading it, and nothing is delivered a second time.
func chainPullGone(ctx context.Context, rt Runtime, c db.ChainRow) error {
	name, server := c.Name, c.Server
	gone := chainGoneReason(name, server)
	return rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Chain(name)
		if err != nil {
			return err
		}
		if cur.Status == string(chain.StatusHalted) {
			if cur.Reason == gone {
				return nil
			}
			cur.Reason = gone
			cur.UpdatedAt = rt.Now().UTC()
			return tx.ChainPut(cur)
		}
		if cur.Status != string(chain.StatusRunning) {
			return nil
		}
		cur.Status = string(chain.StatusHalted)
		cur.Reason = gone
		cur.UpdatedAt = rt.Now().UTC()
		if err := tx.ChainPut(cur); err != nil {
			return err
		}
		member, carrier, ok, err := chainDeliveryMember(tx, cur)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		entry := store.LogEntry{
			TS: rt.Now().UTC(), Round: carrier.Round,
			Direction: store.DirToMasterMind, Kind: store.KindChain,
			Payload: chainTerminalPayload(cur, 0),
		}
		return delivery.Queue(ctx, deliveryDeps(rt), tx, member, entry)
	})
}

// chainPullMembers names every member the server's view holds: a part the row
// does not name yet gets its mirror binding created and its column written, so
// the chain pull can install that member's rounds from the same pass. It
// returns the row it wrote. A chain whose members are all gone has no sibling
// to model a new binding on, so a member it cannot build is skipped.
func chainPullMembers(rt Runtime, c db.ChainRow, view remote.ChainView) (db.ChainRow, error) {
	plan, repoRef, haveSibling, err := chainPullPlan(rt, c)
	if err != nil {
		return c, err
	}
	out := c
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Chain(c.Name)
		if err != nil {
			return err
		}
		// A member the server added gets a chain_member row beside its binding,
		// so the state reads and the end verbs that walk chain_member see it.
		// The existing rows set the next sequence.
		existing, err := tx.ChainMembers(cur.Name)
		if err != nil {
			return err
		}
		var added []db.ChainMemberRow
		changed := false
		for _, mv := range view.Members {
			if mv.Part == "" || mv.Name == "" || chainMemberName(cur, mv.Part) == mv.Name {
				continue
			}
			if _, lerr := tx.Load(mv.Name); errors.Is(lerr, store.ErrNotFound) {
				if !haveSibling {
					continue
				}
				m := chainMember{
					part: mv.Part, name: mv.Name, actor: mv.Actor,
					shape: chainPullShape(mv), writer: mv.Part == chain.MemberBuilder,
				}
				opts := ChainOptions{Name: cur.Name, Feature: cur.Feature, Ticket: cur.Ticket}
				nb := chainMirrorMember(opts, plan, m, mv, db.NewID(), repoRef)
				// A fresh mirror holds none of this member's rounds, whatever
				// round the server is on: the install in this same pass walks
				// them from the first.
				nb.Round = 1
				if err := tx.Save(nb); err != nil {
					return err
				}
				added = append(added, db.ChainMemberRow{
					Binding: nb.Name, Actor: nb.Role, Seq: len(existing) + len(added),
				})
			} else if lerr != nil {
				return lerr
			}
			setChainMemberColumn(&cur, mv.Part, mv.Name)
			changed = true
		}
		if len(added) > 0 {
			if err := tx.ChainMembersPut(cur.Name, added); err != nil {
				return err
			}
		}
		if changed {
			cur.UpdatedAt = rt.Now().UTC()
			if err := tx.ChainPut(cur); err != nil {
				return err
			}
		}
		out = cur
		return nil
	})
	if err != nil {
		return c, err
	}
	return out, nil
}

// chainPullPlan rebuilds the start plan a late member is built from: the
// chain's stored settings and labels, and the mastermind and repo reference an
// existing member carries. haveSibling is false when every member's record is
// gone, which is the one case a new mirror binding cannot be shaped.
func chainPullPlan(rt Runtime, c db.ChainRow) (plan chainServerPlan, repoRef *store.RepoRef, haveSibling bool, err error) {
	var set chain.Settings
	if len(c.SettingsJSON) > 0 {
		if uerr := json.Unmarshal(c.SettingsJSON, &set); uerr != nil {
			return chainServerPlan{}, nil, false, fmt.Errorf("chain %s settings: %w", c.Name, uerr)
		}
	}
	plan = chainServerPlan{
		settings: set, repo: c.Repo, ticket: c.Ticket,
		base: c.Base, server: c.Server, mastermindID: c.MasterMindID,
	}
	for _, name := range chainMembersOf(c) {
		b, lerr := rt.Store.Load(name)
		if errors.Is(lerr, store.ErrNotFound) {
			continue
		}
		if lerr != nil {
			return chainServerPlan{}, nil, false, lerr
		}
		plan.mastermind = b.MasterMind
		plan.mastermindID = b.MasterMindID
		return plan, b.RepoRef, true, nil
	}
	return plan, nil, false, nil
}

// chainPullShape is a member view's shape: the server's own word, or the shape
// the part implies when a server predates the field.
func chainPullShape(mv remote.ChainMemberView) string {
	if mv.View.Shape != "" {
		return mv.View.Shape
	}
	if mv.Part == chain.MemberBuilder {
		return ""
	}
	return store.ShapeReader
}

// setChainMemberColumn writes a part's binding name onto the row's column.
func setChainMemberColumn(c *db.ChainRow, part, name string) {
	switch part {
	case chain.MemberBuilder:
		c.Builder = name
	case chain.MemberReviewer:
		c.Reviewer = name
	case chain.MemberPlanner:
		c.Planner = name
	case chain.MemberSecurity:
		c.Security = name
	}
}

// chainRoundView is the closed-round facts of round r as the catch-up reads
// them: the member's binding view, its ClosedRound set to r and every other
// closed-round fact taken from the view's Round r. That is what makes one
// member's older rounds installable by the same code a single closed round
// uses.
func chainRoundView(mv remote.ChainMemberView, r int) remote.BindingView {
	v := mv.View
	v.ClosedRound = r
	for _, cr := range mv.Rounds {
		if cr.Round != r {
			continue
		}
		v.ReportOutcome = cr.ReportOutcome
		v.GateResult = cr.GateResult
		v.Stopped = cr.Stopped
		v.DiffNote = cr.DiffNote
		v.DiffCommits = cr.DiffCommits
		v.DiffTree = cr.DiffTree
		v.Usage = cr.Usage
		v.Rusage = cr.Rusage
		break
	}
	return v
}

// chainPullMember installs one member's missing closed rounds, in the order
// the server closed them. The fetch runs unlocked; the install takes one lock
// and writes every round in that one critical section, so a pass either lands
// a whole run of rounds or leaves the member where it was. A round that cannot
// be fetched stops the run there: the next pass resumes at that round.
func chainPullMember(ctx context.Context, rt Runtime, mv remote.ChainMemberView) error {
	name := mv.Name
	if name == "" {
		return nil
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		return err
	}
	closed := mv.View.ClosedRound

	type pulled struct {
		round  int
		view   remote.BindingView
		cf     *catchUpFetch
		prompt []byte
	}
	var fs []pulled
	for r := b.Round; r <= closed; r++ {
		rv := chainRoundView(mv, r)
		rb := b
		rb.Round = r
		cf := &catchUpFetch{Round: r}
		if !fetchCatchUpFiles(ctx, rt, rb, rv, cf) {
			slog.Warn("chain pull could not fetch a round", "chain", name, "round", r)
			cf.release()
			break
		}
		prompt, ok := chainPullPrompt(ctx, rt, rb, r)
		if !ok {
			cf.release()
			break
		}
		fs = append(fs, pulled{round: r, view: rv, cf: cf, prompt: prompt})
	}

	// The builder's branch catches up from one bundle: the newest closed
	// round's tree, from wherever the branch last stood. A reader never
	// fetches one -- it has no branch of its own.
	var bcf *catchUpFetch
	if mv.Part == chain.MemberBuilder && closed > 0 &&
		mv.View.ResultCommit != "" && mv.View.ResultCommit != b.Builder.LastKnown {
		bcf = &catchUpFetch{Round: closed}
		fetchCatchUpBundle(ctx, rt, b, mv.View, bcf)
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(name)
		if err != nil {
			return err
		}
		// The fetch named the round the store held when it started; a member
		// another pass moved on is left to that pass.
		if cur.Round != b.Round {
			return nil
		}
		for _, f := range fs {
			next := cur
			next.Round = f.round
			if !applyCatchUpFiles(rt, tx, next, f.view, f.cf) {
				// The round's files did not land. Stop here and fall through
				// to the save, so the rounds before it commit with the member
				// advanced past them while this round does not: the next pass
				// resumes at f.round instead of re-installing what already
				// landed and doubling its entries.
				break
			}
			cur = next
			if err := chainPullPromptEntry(rt, tx, cur, f.round, f.prompt); err != nil {
				return err
			}
			a := &catchUpAck{
				Server: cur.Builder.Server, Name: name, Round: f.round,
				BindingRound: f.round, View: f.view,
				HaveReport: f.cf.ReportTemp != "", HaveDiff: f.cf.Diff != nil,
			}
			next, err := applyCatchUpReport(ctx, rt, tx, cur, a)
			if err != nil {
				return err
			}
			cur = next
		}
		if bcf != nil {
			next, stop, aerr := applyCatchUpAbsorb(ctx, rt, cur, mv.View, bcf)
			if aerr != nil {
				return aerr
			}
			if !stop {
				cur = next
				cur.Builder.LastKnown = mv.View.ResultCommit
				cur.RoundClosedTree = mv.View.ResultCommit
			}
		}
		cur.Builder.RemoteStatus = string(mv.View.RoundState)
		cur.Builder.RemoteQueue = nil
		cur.Builder.RemoteLive = nil
		return tx.Save(cur)
	})
	for _, f := range fs {
		f.cf.release()
	}
	if bcf != nil {
		bcf.release()
	}
	if err != nil {
		return err
	}

	// The ack is cumulative and leaves only once the rounds are stored: a lost
	// one is re-sent by the next pass, because the server's own AckedRound
	// still trails the round it closed.
	cur, lerr := rt.Store.Load(name)
	if lerr == nil && closed > 0 && cur.Round > closed && mv.View.AckedRound < closed {
		if _, aerr := rt.Remote.Ack(ctx, cur.Builder.Server, name, closed); aerr != nil {
			slog.Warn("chain pull ack failed", "server", cur.Builder.Server, "name", name, "round", closed, "err", aerr)
		}
	}
	return nil
}

// chainPullPrompt fetches one round's prompt. The server keeps the prompt it
// staged at the round's own path and serves it as the round's plan file.
func chainPullPrompt(ctx context.Context, rt Runtime, b store.Binding, r int) ([]byte, bool) {
	rc, err := rt.Remote.RoundFile(ctx, b.Builder.Server, b.Name, r, "plan")
	if err != nil {
		if !is404(err) {
			slog.Warn("chain prompt fetch failed", "server", b.Builder.Server, "name", b.Name, "round", r, "err", err)
		}
		return nil, false
	}
	defer rc.Close()
	body, rerr := io.ReadAll(io.LimitReader(rc, chainPullMaxPlanBytes+1))
	if rerr != nil || len(body) > chainPullMaxPlanBytes {
		slog.Warn("chain prompt over cap; not stored", "server", b.Builder.Server, "name", b.Name, "round", r)
		return nil, false
	}
	return body, true
}

// chainPullPromptEntry writes the round's prompt file and records the entry
// that says the member was handed it. A round whose entry already exists is
// left alone, so a re-run of an interrupted install never doubles it.
func chainPullPromptEntry(rt Runtime, tx *store.Tx, b store.Binding, r int, body []byte) error {
	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return err
	}
	if HasPromptEntry(entries, r) {
		return nil
	}
	path := rt.Store.PromptPath(b.Name, r)
	if err := stagePlan(path, body); err != nil {
		return fmt.Errorf("%s: stage prompt at %s: %w", b.Name, path, err)
	}
	return tx.AppendLog(b.Name, store.LogEntry{
		TS: rt.Now().UTC(), Round: r, Direction: store.DirToBuilder,
		Kind: store.KindPrompt, Path: path, Confirmed: true,
		Note: chainStepNote(tx, b.Name),
	})
}

// chainPullFinish copies the server's state onto the mirror and, when the view
// is terminal and every member has caught up, ends the mirror and queues the
// one chain delivery. The terminal status is written only on the running to
// terminal step, so a later pass over the same view queues nothing, while a
// view that is running again makes the mirror running.
func chainPullFinish(ctx context.Context, rt Runtime, c db.ChainRow, view remote.ChainView) error {
	now := rt.Now().UTC()
	return rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Chain(c.Name)
		if err != nil {
			return err
		}
		before := cur.Status
		row, err := chainRowFromView(cur, view, now)
		if err != nil {
			return err
		}
		switch {
		case chain.Status(view.Status) == chain.StatusRunning:
			row.Status = string(chain.StatusRunning)
		case before == string(chain.StatusRunning) && chainStatusTerminal(view.Status) && chainAllCaughtUp(tx, view):
			row.Status = view.Status
		default:
			row.Status = before
		}
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		if before != string(chain.StatusRunning) || !chainStatusTerminal(row.Status) {
			return nil
		}
		member, carrier, ok, err := chainDeliveryMember(tx, row)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		entry := store.LogEntry{
			TS: now, Round: carrier.Round,
			Direction: store.DirToMasterMind, Kind: store.KindChain,
			Payload: chainTerminalPayload(row, view.Findings),
		}
		return delivery.Queue(ctx, deliveryDeps(rt), tx, member, entry)
	})
}

// chainAllCaughtUp reports whether every member the view names sits past its
// closed round: the round's close advances a member's round, so a caught-up
// member is one whose round is greater than the round the server closed.
func chainAllCaughtUp(tx *store.Tx, view remote.ChainView) bool {
	for _, mv := range view.Members {
		if mv.Name == "" {
			continue
		}
		b, err := tx.Load(mv.Name)
		if err != nil {
			return false
		}
		if b.Round <= mv.View.ClosedRound {
			return false
		}
	}
	return len(view.Members) > 0
}

// chainStatusTerminal reports whether a chain status word is one of the three
// the state machine stops on.
func chainStatusTerminal(status string) bool {
	switch chain.Status(status) {
	case chain.StatusDone, chain.StatusHalted, chain.StatusStopped:
		return true
	}
	return false
}

// chainRowFromView is the mirror row carrying the server's state: the fields a
// chain row shows and the view can answer for. The row's own stored facts --
// its plan copies, its settings and its branch -- stay as this machine wrote
// them, because the server does not own them. It also stores the shipped
// default and the engine state the row's legacy columns describe, through
// workflow.FromLegacy, so the mirror's read surfaces see the engine state the
// server drove.
func chainRowFromView(c db.ChainRow, v remote.ChainView, now time.Time) (db.ChainRow, error) {
	c.Status = chainOr(v.Status, c.Status)
	c.Reason = v.Reason
	c.Phase = chainOr(v.Phase, c.Phase)
	c.Step = chainOr(v.Step, c.Step)
	c.Plan = chainIntOr(v.Plan, c.Plan)
	c.Plans = chainIntOr(v.Plans, c.Plans)
	c.Corrections = v.Corrections
	c.AwaitingMember = chainOr(v.AwaitingMember, c.AwaitingMember)
	c.AwaitingRound = chainIntOr(v.AwaitingRound, c.AwaitingRound)
	if v.PlanStartCommit != "" {
		c.PlanStartCommit = v.PlanStartCommit
	}
	c.UpdatedAt = now

	leg, err := chainViewLegacy(c, v)
	if err != nil {
		return db.ChainRow{}, err
	}
	def, st, err := workflow.FromLegacy(leg)
	if err != nil {
		return db.ChainRow{}, fmt.Errorf("chain %s: %w", c.Name, err)
	}
	if c.WorkflowJSON, err = json.Marshal(def); err != nil {
		return db.ChainRow{}, fmt.Errorf("chain %s workflow: %w", c.Name, err)
	}
	if c.StateJSON, err = json.Marshal(st); err != nil {
		return db.ChainRow{}, fmt.Errorf("chain %s state: %w", c.Name, err)
	}
	return c, nil
}

// chainViewLegacy is the legacy row a server's view describes: the merged
// columns the view carries, the mirror's own plan copies, its own settings, and
// the builder actor the view names.
func chainViewLegacy(c db.ChainRow, v remote.ChainView) (workflow.Legacy, error) {
	paths, err := chainPlanPaths(c)
	if err != nil {
		return workflow.Legacy{}, err
	}
	var set chain.Settings
	if len(c.SettingsJSON) > 0 {
		if err := json.Unmarshal(c.SettingsJSON, &set); err != nil {
			return workflow.Legacy{}, fmt.Errorf("chain %s settings: %w", c.Name, err)
		}
	}
	return workflow.Legacy{
		Status: c.Status, Reason: c.Reason, Phase: c.Phase, Step: c.Step,
		Plan: c.Plan, Plans: c.Plans, Corrections: c.Corrections,
		AwaitingRound: c.AwaitingRound, PlanPaths: paths,
		Settings: workflow.LegacySettings{
			MaxCorrections: set.MaxCorrections, ReviewerActor: set.ReviewerActor,
			PlannerActor: set.PlannerActor, SecurityActor: set.SecurityActor,
			Security: set.Security, Gate: set.Gate, Regate: set.Regate,
		},
		Builder: chainViewBuilderActor(v),
	}, nil
}

// chainViewBuilderActor is the actor the view's builder member runs, or "" when
// the view names no builder.
func chainViewBuilderActor(v remote.ChainView) string {
	for _, m := range v.Members {
		if m.Part == chain.MemberBuilder {
			return m.Actor
		}
	}
	return ""
}
