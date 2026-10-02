package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// servedChainDefinition resolves the shipped default with a served create's
// wire settings on its params, so a served chain plans and runs through the
// same engine a local default start does. internal/remote does not import
// internal/workflow, so the values are assembled here.
func servedChainDefinition(req ServedChainRequest) (workflow.Definition, error) {
	s := req.Settings
	values := map[string]string{
		"builder":         chainActorOrBuilder(req.BuilderActor),
		"reviewer":        s.ReviewerActor,
		"planner":         s.PlannerActor,
		"scan":            strconv.FormatBool(s.Security),
		"gate":            s.Gate,
		"regate":          strconv.Itoa(s.Regate),
		"max_corrections": strconv.Itoa(s.MaxCorrections),
	}
	// A blank security actor keeps the default's own value, so a later resume
	// that turns the scan on still has an actor to name.
	if s.SecurityActor != "" {
		values["security"] = s.SecurityActor
	}
	def, err := workflow.WithParams(workflow.Default(), values)
	if err != nil {
		return workflow.Definition{}, refuse("%v", err)
	}
	return def, nil
}

// servedChainEngineCreate writes the engine half of a served create: it stores
// the resolved definition and its start state on the row, creates the row and
// every member atomically, copies the plans, and runs the start actions, so
// plan 1 is queued for the server's admit exactly as a served send queues. It
// returns the row as the store now holds it.
func servedChainEngineCreate(ctx context.Context, rt Runtime, plan ServedChainPlan, row db.ChainRow, built []store.Binding, planPaths []string) (db.ChainRow, error) {
	defJSON, err := json.Marshal(plan.def)
	if err != nil {
		return row, fmt.Errorf("encode chain workflow: %w", err)
	}
	start, acts := workflow.Start(plan.def, workflow.StartInputs{Plans: planPaths})
	stateJSON, err := json.Marshal(start)
	if err != nil {
		return row, fmt.Errorf("encode chain state: %w", err)
	}
	row.WorkflowJSON = defJSON
	row.StateJSON = stateJSON
	row.Status = string(start.Status)
	row.Plans = len(planPaths)
	row.Plan = 1
	applyChainLegacy(&row, plan.def, start)

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.CreateChain(row, built)
	}); err != nil {
		return row, err
	}
	bodies := make([][]byte, len(plan.req.Plans))
	for i, p := range plan.req.Plans {
		bodies[i] = []byte(p)
	}
	if err := chainCopyPlans(rt, row.Name, bodies); err != nil {
		return row, fmt.Errorf("chain %q started, but copying its plans failed: %w", row.Name, err)
	}
	if err := chainRunStartActions(ctx, rt, row.Name, plan.def, start, acts); err != nil {
		return row, fmt.Errorf("chain %q started, but plan 1 could not be handed to %s: %w", row.Name, row.Name, err)
	}
	// The start actions opened the first round; the store's row is the one that
	// names it.
	if current, cerr := rt.Store.Chain(row.Name); cerr == nil {
		row = current
	}
	return row, nil
}

// servedChainAddMembers creates the members a served chain's resume brought in:
// each is built in the served shape, the server picking its own candidate and
// tier, beside the served builder, and its chain_member row is written with it.
// seq is the next member sequence the chain's existing rows leave off at.
func servedChainAddMembers(rt Runtime, tx *store.Tx, c db.ChainRow, builder store.Binding, add []chainMember, seq int) (db.ChainRow, error) {
	if builder.Serve == nil {
		return c, fmt.Errorf("chain %s has a served builder with no serve facts", c.Name)
	}
	facts := servedFacts{
		owner: builder.Owner, repoID: builder.Serve.RepoID, worktree: builder.CWD,
		bare: builder.Serve.BareRepo, base: builder.Base, feature: c.Feature, ticket: c.Ticket,
		authorName: builder.Serve.AuthorName, authorEmail: builder.Serve.AuthorEmail,
		now: rt.Now().UTC(),
	}
	rows := make([]db.ChainMemberRow, 0, len(add))
	for i, m := range add {
		pick, err := servedActorPick(rt, m.actor)
		if err != nil {
			return c, err
		}
		b := servedChainMember(m, pick, facts)
		if _, lerr := tx.Load(b.Name); lerr == nil {
			return c, fmt.Errorf("binding %q already exists: `relevo unbind %s` first", b.Name, b.Name)
		} else if !errors.Is(lerr, store.ErrNotFound) {
			return c, lerr
		}
		if err := tx.Save(b); err != nil {
			return c, err
		}
		rows = append(rows, db.ChainMemberRow{Binding: b.Name, Actor: b.Role, Seq: seq + i})
		setChainMemberColumn(&c, m.part, b.Name)
	}
	if err := tx.ChainMembersPut(c.Name, rows); err != nil {
		return c, err
	}
	c.UpdatedAt = rt.Now().UTC()
	return c, tx.ChainPut(c)
}
