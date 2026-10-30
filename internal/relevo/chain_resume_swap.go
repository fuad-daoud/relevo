package relevo

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// splitPlannedMembers divides members needed by def into members that must be
// newly created and members whose existing binding requires actor rebinding.
func splitPlannedMembers(members []chainMember, rows []db.ChainMemberRow) (add, swap []chainMember) {
	have := make(map[string]db.ChainMemberRow, len(rows))
	for _, m := range rows {
		have[m.Binding] = m
	}
	for _, m := range members {
		stored, ok := have[m.name]
		if !ok {
			add = append(add, m)
		} else if chainFlowActorName(stored.Actor) != m.actor {
			swap = append(swap, m)
		}
	}
	return add, swap
}

// overlaySwapBinding mutates an existing local member binding with the newly
// resolved actor's fields while preserving its identity, round, and logs.
func overlaySwapBinding(existing, fresh store.Binding, m chainMember) store.Binding {
	existing = abandonSession(existing)
	existing.Builder.StreamSessionID = ""
	existing.RoundSwitches = 0
	existing.Role = resolve.NormRole(m.actor)
	existing.Shape = fresh.Shape
	existing.Builder = fresh.Builder
	existing.Builder.StreamSessionID = ""
	existing.BuilderCandidate = fresh.BuilderCandidate
	existing.BuilderAccount = fresh.BuilderAccount
	existing.BuilderPinned = fresh.BuilderPinned
	existing.Tier = fresh.Tier
	return existing
}

// overlayServedSwapBinding mutates an existing served member binding with the
// server's pick for the new actor while preserving server placement and link.
func overlayServedSwapBinding(existing store.Binding, m chainMember, pick servedPick) store.Binding {
	existing = abandonSession(existing)
	existing.Builder.StreamSessionID = ""
	existing.RoundSwitches = 0
	existing.Role = resolve.NormRole(m.actor)
	existing.Shape = m.shape
	existing.Builder.Kind = pick.kind
	existing.BuilderCandidate = pick.token
	existing.Tier = pick.tier
	return existing
}

func validateSwapOpenRounds(tx *store.Tx, c db.ChainRow, swap []chainMember) error {
	for _, m := range swap {
		if err := resumeOpenRoundRefusal(tx, c, m.name); err != nil {
			return err
		}
	}
	return nil
}

func validateServedSwapActors(rt Runtime, members []chainMember) (map[string]servedPick, error) {
	picks := make(map[string]servedPick, len(members))
	for _, m := range members {
		shape, err := resolve.ActorShape(rt.RoleRegistry(), m.actor)
		if err != nil {
			return nil, err
		}
		if shape != m.shape {
			return nil, fmt.Errorf("chain %s actor %q must be a %s actor, not a %s", m.part, m.actor, m.shape, shape)
		}
		pick, err := servedActorPick(rt, m.actor)
		if err != nil {
			return nil, err
		}
		picks[m.name] = pick
	}
	return picks, nil
}

func applyLocalSwaps(tx *store.Tx, swap []chainMember, built []store.Binding, rows []db.ChainMemberRow, c *db.ChainRow, snap *chainResumeSnapshot) ([]db.ChainMemberRow, error) {
	for i, m := range swap {
		existing, err := tx.Load(m.name)
		if err != nil {
			return nil, err
		}
		snap.recordSwapped(existing)
		updated := overlaySwapBinding(existing, built[i], m)
		if err := tx.Save(updated); err != nil {
			return nil, err
		}
		setChainMemberColumn(c, m.part, m.name)
		for j := range rows {
			if rows[j].Binding == m.name {
				rows[j].Actor = resolve.NormRole(m.actor)
			}
		}
	}
	return rows, nil
}

func applyServedSwaps(tx *store.Tx, swap []chainMember, picks map[string]servedPick, rows []db.ChainMemberRow, c *db.ChainRow, snap *chainResumeSnapshot) ([]db.ChainMemberRow, error) {
	for _, m := range swap {
		existing, err := tx.Load(m.name)
		if err != nil {
			return nil, err
		}
		snap.recordSwapped(existing)
		updated := overlayServedSwapBinding(existing, m, picks[m.name])
		if err := tx.Save(updated); err != nil {
			return nil, err
		}
		setChainMemberColumn(c, m.part, m.name)
		for j := range rows {
			if rows[j].Binding == m.name {
				rows[j].Actor = resolve.NormRole(m.actor)
			}
		}
	}
	return rows, nil
}

func applyLocalAdds(tx *store.Tx, add []chainMember, built []store.Binding, rows []db.ChainMemberRow, c *db.ChainRow, snap *chainResumeSnapshot) ([]db.ChainMemberRow, error) {
	for i, b := range built {
		if _, lerr := tx.Load(b.Name); lerr == nil {
			return nil, fmt.Errorf("binding %q already exists: `relevo unbind %s` first", b.Name, b.Name)
		} else if !errors.Is(lerr, store.ErrNotFound) {
			return nil, lerr
		}
		snap.recordAdded(b.Name)
		if err := tx.Save(b); err != nil {
			return nil, err
		}
		rows = append(rows, db.ChainMemberRow{Binding: b.Name, Actor: b.Role, Seq: len(rows)})
		setChainMemberColumn(c, add[i].part, b.Name)
	}
	return rows, nil
}

func applyServedAdds(tx *store.Tx, add []chainMember, picks map[string]servedPick, facts servedFacts, rows []db.ChainMemberRow, c *db.ChainRow, snap *chainResumeSnapshot) ([]db.ChainMemberRow, error) {
	for _, m := range add {
		b := servedChainMember(m, picks[m.name], facts)
		if _, lerr := tx.Load(b.Name); lerr == nil {
			return nil, fmt.Errorf("binding %q already exists: `relevo unbind %s` first", b.Name, b.Name)
		} else if !errors.Is(lerr, store.ErrNotFound) {
			return nil, lerr
		}
		snap.recordAdded(b.Name)
		if err := tx.Save(b); err != nil {
			return nil, err
		}
		rows = append(rows, db.ChainMemberRow{Binding: b.Name, Actor: b.Role, Seq: len(rows)})
		setChainMemberColumn(c, m.part, b.Name)
	}
	return rows, nil
}

func chainResumeSwapLocal(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, builder store.Binding, add, swap []chainMember, rows []db.ChainMemberRow, snap *chainResumeSnapshot) (db.ChainRow, error) {
	if err := validateSwapOpenRounds(tx, c, swap); err != nil {
		return c, err
	}
	swapRes, err := chainResolveActors(rt, swap)
	if err != nil {
		return c, err
	}
	addRes, err := chainResolveActors(rt, add)
	if err != nil {
		return c, err
	}
	set, err := resumeSettings(rt, c, ResumeOptions{})
	if err != nil {
		return c, err
	}
	base := chainBase{
		cwd: builder.CWD, mastermind: builder.MasterMind, mastermindID: c.MasterMindID,
		repo: c.Repo, repoRef: builder.RepoRef, feature: c.Feature, ticket: c.Ticket,
		subdir:   builder.Subdir,
		worktree: builder.CWD,
	}
	swapBuilt, err := chainBuildMembers(ctx, rt, swap, swapRes, base, set, c.Plans)
	if err != nil {
		return c, err
	}
	addBuilt, err := chainBuildMembers(ctx, rt, add, addRes, base, set, c.Plans)
	if err != nil {
		return c, err
	}
	rows, err = applyLocalSwaps(tx, swap, swapBuilt, rows, &c, snap)
	if err != nil {
		return c, err
	}
	rows, err = applyLocalAdds(tx, add, addBuilt, rows, &c, snap)
	if err != nil {
		return c, err
	}
	if err := tx.ChainMembersPut(c.Name, rows); err != nil {
		return c, err
	}
	c.UpdatedAt = rt.Now().UTC()
	return c, tx.ChainPut(c)
}

func chainResumeSwapServed(rt Runtime, tx *store.Tx, c db.ChainRow, builder store.Binding, add, swap []chainMember, rows []db.ChainMemberRow, snap *chainResumeSnapshot) (db.ChainRow, error) {
	if builder.Serve == nil {
		return c, fmt.Errorf("chain %s has a served builder with no serve facts", c.Name)
	}
	if err := validateSwapOpenRounds(tx, c, swap); err != nil {
		return c, err
	}
	swapPicks, err := validateServedSwapActors(rt, swap)
	if err != nil {
		return c, err
	}
	addPicks, err := validateServedSwapActors(rt, add)
	if err != nil {
		return c, err
	}
	set, err := resumeSettings(rt, c, ResumeOptions{})
	if err != nil {
		return c, err
	}
	facts := servedFacts{
		owner: builder.Owner, repoID: builder.Serve.RepoID, worktree: builder.CWD,
		bare: builder.Serve.BareRepo, base: builder.Base, feature: c.Feature, ticket: c.Ticket,
		subdir:     builder.Subdir,
		authorName: builder.Serve.AuthorName, authorEmail: builder.Serve.AuthorEmail,
		plans: c.Plans, settings: set,
		now: rt.Now().UTC(),
	}
	rows, err = applyServedSwaps(tx, swap, swapPicks, rows, &c, snap)
	if err != nil {
		return c, err
	}
	rows, err = applyServedAdds(tx, add, addPicks, facts, rows, &c, snap)
	if err != nil {
		return c, err
	}
	if err := tx.ChainMembersPut(c.Name, rows); err != nil {
		return c, err
	}
	c.UpdatedAt = rt.Now().UTC()
	return c, tx.ChainPut(c)
}

// chainResumeSyncMembers detects whether any planned members need to be added
// or swapped, validates their candidate gating and shapes before writing,
// overlays swapped bindings in place, creates added bindings, and updates the
// chain's member rows.
func chainResumeSyncMembers(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, snap *chainResumeSnapshot) (db.ChainRow, error) {
	actors := rt.RoleRegistry().WorkflowActors()
	planned, err := chainMemberNames(c.Name, def, actors)
	if err != nil {
		return c, err
	}
	keeper := chainWriterKeeper(def, workflow.UsedActors(def), actors)
	members := chainWorkflowMembers(def, planned, keeper)
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return c, err
	}
	add, swap := splitPlannedMembers(members, rows)
	if len(add) == 0 && len(swap) == 0 {
		return c, nil
	}
	builder, err := tx.Load(c.Builder)
	if err != nil {
		return c, fmt.Errorf("chain %s has no builder member to place the new members beside: %w", c.Name, err)
	}
	if builder.Owner != "" {
		return chainResumeSwapServed(rt, tx, c, builder, add, swap, rows, snap)
	}
	return chainResumeSwapLocal(ctx, rt, tx, c, builder, add, swap, rows, snap)
}
