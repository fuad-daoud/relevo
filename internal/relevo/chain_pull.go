package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainPullMaxPlanBytes bounds one pulled round prompt. The server already
// caps plans at create, so a body past this is refused rather than held whole.
const chainPullMaxPlanBytes = 1 << 20

// chainPullServers pulls every mirror chain that is not done. It runs without
// the state lock; each member's install and the final row update take their
// own short locks. Failures join, so one gone or unreachable server cannot
// stop the rest.
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
		if !chain.OnServer(c) || c.Status == string(chain.StatusDone) || chain.GoneMirror(c) {
			continue
		}
		if err := chainPullOne(ctx, rt, c); err != nil {
			errs = append(errs, fmt.Errorf("chain %s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}

// chainPullOne moves one mirror a pass forward: it reads the server's view,
// names added members, installs each member's missing closed rounds in order,
// acks them, and copies the view's state on. A chain the server no longer
// holds halts the mirror once.
func chainPullOne(ctx context.Context, rt Runtime, c db.ChainRow) error {
	if rt.Remote == nil {
		return nil
	}
	view, err := rt.Remote.GetChain(ctx, c.Server, c.Name)
	if err != nil {
		if remote.Is404(err) {
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

// chainPullGone ends a mirror the server no longer knows: it halts the row
// once and queues the one end delivery. An already-terminal mirror is left
// alone, so a repeated 404 queues nothing more; an already-halted one is
// stamped with the gone reason so the walk stops reading it.
func chainPullGone(ctx context.Context, rt Runtime, c db.ChainRow) error {
	name, server := c.Name, c.Server
	gone := chain.GoneReason(name, server)
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
			TS: rt.Now().UTC(), Round: chainTerminalRound(tx, carrier),
			Direction: store.DirToMasterMind, Kind: store.KindChain,
			Payload: chainTerminalPayload(cur, 0),
		}
		return delivery.Queue(ctx, deliveryDeps(rt), tx, member, entry)
	})
}

// chainPullMembers names every member the server's view holds: an unnamed
// part gets its mirror binding created and its column written, so the pull
// installs that member's rounds from the same pass. It returns the row it
// wrote. With every member gone there is no sibling to model a new binding
// on, so an unbuildable member is skipped.
func chainPullMembers(rt Runtime, c db.ChainRow, view remote.ChainView) (db.ChainRow, error) {
	plan, repoRef, haveSibling, err := chainPullPlan(rt, c)
	if err != nil {
		return c, err
	}
	return chainPullPersistMembers(rt, c, view, plan, repoRef, haveSibling)
}

// chainPullAddedBindings names the view's added members inside the pull's
// lock: a missing binding gets a mirror built from the start plan, and every
// member's column is written. With every member gone there is no sibling to
// model a new binding on, so an unbuildable member is skipped.
func chainPullAddedBindings(tx *store.Tx, cur *db.ChainRow, view remote.ChainView, plan chainServerPlan, repoRef *store.RepoRef, haveSibling bool, existing []db.ChainMemberRow) ([]db.ChainMemberRow, bool, error) {
	var added []db.ChainMemberRow
	changed := false
	for _, mv := range view.Members {
		if mv.Part == "" || mv.Name == "" || chain.MemberName(*cur, mv.Part) == mv.Name {
			continue
		}
		if _, lerr := tx.Load(mv.Name); errors.Is(lerr, store.ErrNotFound) {
			if !haveSibling {
				continue
			}
			opts := ChainOptions{Name: cur.Name, Feature: cur.Feature, Ticket: cur.Ticket}
			nb := chainMirrorMember(opts, plan, mv, db.NewID(), repoRef)
			// A fresh mirror holds none of this member's rounds: the install
			// in this same pass walks them from the first.
			nb.Round = 1
			if err := tx.Save(nb); err != nil {
				return nil, false, err
			}
			added = append(added, db.ChainMemberRow{
				Binding: nb.Name, Actor: nb.Role, Seq: len(existing) + len(added),
			})
		} else if lerr != nil {
			return nil, false, lerr
		}
		setChainMemberColumn(cur, mv.Part, mv.Name)
		changed = true
	}
	return added, changed, nil
}

// chainPullPersistMembers writes the view's members in one lock section: the
// added bindings beside their chain_member rows, then the row itself when a
// column moved. It returns the row it wrote.
func chainPullPersistMembers(rt Runtime, c db.ChainRow, view remote.ChainView, plan chainServerPlan, repoRef *store.RepoRef, haveSibling bool) (db.ChainRow, error) {
	out := c
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Chain(c.Name)
		if err != nil {
			return err
		}
		// An added member gets a chain_member row beside its binding, so the
		// state reads and the end verbs walking chain_member see it. The
		// existing rows set the next sequence.
		existing, err := tx.ChainMembers(cur.Name)
		if err != nil {
			return err
		}
		added, changed, err := chainPullAddedBindings(tx, &cur, view, plan, repoRef, haveSibling, existing)
		if err != nil {
			return err
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
// stored settings and labels, and the mastermind and repo reference an
// existing member carries. haveSibling is false when every record is gone,
// the one case a new mirror binding cannot be shaped.
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
	for _, name := range view.MembersOf(c) {
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

// chainPullMember installs one member's missing closed rounds in server
// order. The fetch runs unlocked; the install takes one lock and writes every
// round in that one critical section, so a pass either lands a whole run or
// leaves the member where it was. An unfetchable round stops the run, and the
// next pass resumes there.
// chainPulledRound is one fetched round awaiting install: the round number,
// its view, the fetched files, and its prompt.
type chainPulledRound struct {
	round  int
	view   remote.BindingView
	cf     *catchUpFetch
	prompt []byte
}

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

	fs, bcf := chainPullFetchRounds(ctx, rt, b, mv, closed)
	err = chainPullInstallRounds(ctx, rt, mv, b, fs, bcf)
	for _, f := range fs {
		f.cf.release()
	}
	if bcf != nil {
		bcf.release()
	}
	if err != nil {
		return err
	}

	chainPullAckMember(ctx, rt, mv, name, closed)
	return nil
}

// chainPullFetchRounds fetches every missing closed round unlocked: each
// round's files and prompt in server order, then the builder's catch-up
// bundle. An unfetchable round stops the run, and the next pass resumes
// there.
func chainPullFetchRounds(ctx context.Context, rt Runtime, b store.Binding, mv remote.ChainMemberView, closed int) ([]chainPulledRound, *catchUpFetch) {
	name := b.Name
	var fs []chainPulledRound
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
		fs = append(fs, chainPulledRound{round: r, view: rv, cf: cf, prompt: prompt})
	}

	// The builder's branch catches up from one bundle: the newest closed
	// round's tree. A reader never fetches one; it has no branch of its own.
	var bcf *catchUpFetch
	if mv.Part == chain.MemberBuilder && closed > 0 &&
		mv.View.ResultCommit != "" && mv.View.ResultCommit != b.Builder.LastKnown {
		bcf = &catchUpFetch{Round: closed}
		fetchCatchUpBundle(ctx, rt, b, mv.View, bcf)
	}
	return fs, bcf
}

// chainPullInstallRounds installs fetched rounds in one lock: every round in
// that one critical section, so a pass either lands a whole run or leaves the
// member where it was.
func chainPullInstallRounds(ctx context.Context, rt Runtime, mv remote.ChainMemberView, b store.Binding, fs []chainPulledRound, bcf *catchUpFetch) error {
	name := b.Name
	return rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(name)
		if err != nil {
			return err
		}
		// The fetch named the round the store held when it started; a member
		// another pass moved on stays with that pass.
		if cur.Round != b.Round {
			return nil
		}
		snap := cur
		for _, f := range fs {
			next := cur
			next.Round = f.round
			// The mirror's binding carries the server's start for the round it
			// installs, so a reader asking the binding sees the round's real
			// start too.
			next.RoundStartedAt = chainPullStart(rt, f.view.RoundStartedAt)
			if !applyCatchUpFiles(rt, tx, next, f.view, f.cf) {
				// The round's files did not land. Stop here and fall through to the
				// save, so earlier rounds commit with the member advanced past
				// them while this round does not: the next pass resumes at
				// f.round instead of re-installing and doubling entries.
				break
			}
			cur = next
			if err := chainPullPromptEntry(rt, tx, cur, f.round, f.prompt, f.view.RoundStartedAt); err != nil {
				return err
			}
			if entry, listed := chainRoundEntry(mv, f.round); listed {
				if err := chainPullQueueEntry(rt, tx, cur, f.round, entry); err != nil {
					return err
				}
			}
			a := &catchUpAck{
				Server: cur.Builder.Server, Name: name, Round: f.round,
				BindingRound: f.round, View: f.view,
				HaveReport: f.cf.ReportTemp != "", HaveDiff: f.cf.Diff != nil,
				GatePath: f.cf.GatePath,
			}
			next, err := applyCatchUpReport(ctx, rt, tx, cur, a)
			if err != nil {
				return err
			}
			cur = next
		}
		if bcf != nil {
			next, stop, aerr := applyCatchUpAbsorb(ctx, rt, tx, cur, mv.View, bcf)
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
		return chainPullSaveIfChanged(tx, snap, cur)
	})
}

// chainPullAckMember acks the installed rounds once stored: the ack is
// cumulative, so a lost one is re-sent by the next pass while the server's
// own AckedRound still trails the closed round.
func chainPullAckMember(ctx context.Context, rt Runtime, mv remote.ChainMemberView, name string, closed int) {
	cur, lerr := rt.Store.Load(name)
	if lerr == nil && closed > 0 && cur.Round > closed && mv.View.AckedRound < closed {
		if _, aerr := rt.Remote.Ack(ctx, cur.Builder.Server, name, closed); aerr != nil {
			slog.Warn("chain pull ack failed", "server", cur.Builder.Server, "name", name, "round", closed, "err", aerr)
		}
	}
}

// chainPullFinish copies the server's state onto the mirror and, when the
// view is terminal and every member caught up, ends the mirror and queues the
// one chain delivery. The terminal status is written only on the running to
// terminal step, so a later pass over the same view queues nothing, while a
// view running again makes the mirror running.
func chainPullFinish(ctx context.Context, rt Runtime, c db.ChainRow, view remote.ChainView) error {
	now := rt.Now().UTC()
	return rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Chain(c.Name)
		if err != nil {
			return err
		}
		before := cur.Status
		snap := cur
		row, err := chainRowFromView(cur, view, now)
		if err != nil {
			return err
		}
		switch {
		case chain.Status(view.Status) == chain.StatusRunning:
			row.Status = string(chain.StatusRunning)
		case before == string(chain.StatusRunning) && chain.Status(view.Status) == chain.StatusStopped:
			// A stopped view settles the mirror even while a member sits
			// behind: the server ended the chain, and an unfetchable member
			// would hold the mirror open for good. The install keeps running
			// on later passes, so late rounds still land.
			row.Status = view.Status
		case before == string(chain.StatusRunning) && chain.StatusTerminal(view.Status) && chainAllCaughtUp(tx, view):
			row.Status = view.Status
		default:
			row.Status = before
		}
		if err := chainPullPutIfChanged(tx, snap, row); err != nil {
			return err
		}
		if err := chainPullTrace(tx, c.Name, view.Trace); err != nil {
			return err
		}
		if before != string(chain.StatusRunning) || !chain.StatusTerminal(row.Status) {
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
			TS: now, Round: chainTerminalRound(tx, carrier),
			Direction: store.DirToMasterMind, Kind: store.KindChain,
			Payload: chainTerminalPayload(row, view.Findings),
		}
		return delivery.Queue(ctx, deliveryDeps(rt), tx, member, entry)
	})
}

// chainAllCaughtUp reports whether every member the view names sits past its
// closed round: the round's close advances a member's round, so caught up
// means greater than the round the server closed.
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

// chainPullSaveIfChanged saves a pull's member only when the pass moved it:
// an unchanged record is dropped rather than rewritten with a new stamp, so a
// repeated pass over an unchanged view writes nothing. The snapshot and the
// mutable copy are compared with the UpdatedAt stamp Save writes dropped on
// both sides, so any moved field still saves.
func chainPullSaveIfChanged(tx *store.Tx, snap, cur store.Binding) error {
	snap.UpdatedAt = time.Time{}
	cur.UpdatedAt = time.Time{}
	if reflect.DeepEqual(snap, cur) {
		return nil
	}
	return tx.Save(cur)
}

// chainPullPutIfChanged is chainPullSaveIfChanged for the chain row the pull
// rebuilds from the view.
func chainPullPutIfChanged(tx *store.Tx, snap, row db.ChainRow) error {
	snap.UpdatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	if reflect.DeepEqual(snap, row) {
		return nil
	}
	return tx.ChainPut(row)
}

// chainRowFromView is the mirror row carrying the server's state. The row's
// own stored facts -- plan copies, settings, branch -- stay as this machine
// wrote them, because the server does not own them. It copies the server's
// workflow and engine state when carried, falling back to workflow.FromLegacy
// for an older server view.
func chainRowFromView(c db.ChainRow, v remote.ChainView, now time.Time) (db.ChainRow, error) {
	c.Status, c.Reason = chainOr(v.Status, c.Status), v.Reason
	c.Phase, c.Step = chainOr(v.Phase, c.Phase), chainOr(v.Step, c.Step)
	c.Plan, c.Plans = chainIntOr(v.Plan, c.Plan), chainIntOr(v.Plans, c.Plans)
	c.Corrections = v.Corrections
	c.AwaitingMember = chainOr(v.AwaitingMember, c.AwaitingMember)
	c.AwaitingRound = chainIntOr(v.AwaitingRound, c.AwaitingRound)
	if v.PlanStartCommit != "" {
		c.PlanStartCommit = v.PlanStartCommit
	}
	c.UpdatedAt = now

	if len(v.Workflow) > 0 && len(v.State) > 0 {
		c.WorkflowJSON, c.StateJSON = v.Workflow, v.State
		return c, nil
	}

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

// chainViewLegacy is the legacy row a server's view describes: the view's
// merged columns, the mirror's own plan copies and settings, and the viewed
// builder actor.
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

// chainViewBuilderActor is the actor the view's builder member runs, or ""
// when the view names no builder.
func chainViewBuilderActor(v remote.ChainView) string {
	for _, m := range v.Members {
		if m.Part == chain.MemberBuilder {
			return m.Actor
		}
	}
	return ""
}
