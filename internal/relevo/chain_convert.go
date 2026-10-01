package relevo

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainLegacyColumns names a row's four member columns in the order a
// conversion walks them: the builder keeps the chain's own name, and the rest
// fill the reviewer, planner and security parts. An actor shared by more than
// one column maps each of its bindings, and a lookup by actor returns the
// first, which is the builder.
func chainLegacyColumns(c db.ChainRow) []string {
	return []string{c.Builder, c.Reviewer, c.Planner, c.Security}
}

// chainLegacyConversion is one legacy chain row translated onto the engine:
// the row carrying the default workflow and its start state, the member rows
// its four columns name, and the member bindings whose own gate and repair
// budget must be cleared, because a check is now a step.
type chainLegacyConversion struct {
	row     db.ChainRow
	members []db.ChainMemberRow
	cleared []string
}

// ConvertLegacyChains rewrites every chain row that predates the workflow
// engine onto it: the row gains the shipped default workflow and the state its
// legacy step migrates to, its four member columns become chain_member rows,
// and its members' own gates are cleared so the next check is a step. A row
// that already carries a state is left alone, so running it twice changes
// nothing. The whole set converts in one transaction under the lock: a failure
// leaves every row as it was.
func ConvertLegacyChains(rt Runtime) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		rows, err := tx.Chains()
		if err != nil {
			return err
		}
		for _, c := range rows {
			if len(c.StateJSON) > 0 {
				continue
			}
			conv, cerr := chainConvertLegacy(tx, c)
			if cerr != nil {
				return cerr
			}
			if err := tx.ChainPut(conv.row); err != nil {
				return err
			}
			if len(conv.members) > 0 {
				if err := tx.ChainMembersPut(c.Name, conv.members); err != nil {
					return err
				}
			}
			for _, name := range conv.cleared {
				if err := chainClearMemberGate(tx, name); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// chainConvertLegacy is the pure half of a conversion: it reads the row's
// settings and plan copies, resolves its member bindings through tx, and
// returns the row the engine reads plus its member rows.
func chainConvertLegacy(tx *store.Tx, c db.ChainRow) (chainLegacyConversion, error) {
	leg, members, cleared, err := chainLegacyFacts(tx, c)
	if err != nil {
		return chainLegacyConversion{}, err
	}
	def, st, err := workflow.FromLegacy(leg)
	if err != nil {
		return chainLegacyConversion{}, fmt.Errorf("chain %s: %w", c.Name, err)
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return chainLegacyConversion{}, fmt.Errorf("chain %s workflow: %w", c.Name, err)
	}
	stateJSON, err := json.Marshal(st)
	if err != nil {
		return chainLegacyConversion{}, fmt.Errorf("chain %s state: %w", c.Name, err)
	}
	row := c
	row.WorkflowJSON = defJSON
	row.StateJSON = stateJSON
	return chainLegacyConversion{row: row, members: members, cleared: cleared}, nil
}

// chainLegacyFacts reads everything workflow.FromLegacy needs from a row and
// its member bindings. The builder's actor is read from its own binding, so a
// chain whose builder runs a named actor keeps it. A member binding whose
// record is gone is skipped: there is nothing to gate and no member row to
// write.
func chainLegacyFacts(tx *store.Tx, c db.ChainRow) (workflow.Legacy, []db.ChainMemberRow, []string, error) {
	var set chain.Settings
	if len(c.SettingsJSON) > 0 {
		if err := json.Unmarshal(c.SettingsJSON, &set); err != nil {
			return workflow.Legacy{}, nil, nil, fmt.Errorf("chain %s settings: %w", c.Name, err)
		}
	}
	var paths []string
	if len(c.PlanPathsJSON) > 0 {
		if err := json.Unmarshal(c.PlanPathsJSON, &paths); err != nil {
			return workflow.Legacy{}, nil, nil, fmt.Errorf("chain %s plan paths: %w", c.Name, err)
		}
	}
	leg := workflow.Legacy{
		Status: c.Status, Reason: c.Reason, Phase: c.Phase, Step: c.Step,
		Plan: c.Plan, Plans: c.Plans, Corrections: c.Corrections,
		AwaitingRound: c.AwaitingRound, PlanPaths: paths,
		Settings: workflow.LegacySettings{
			MaxCorrections: set.MaxCorrections, ReviewerActor: set.ReviewerActor,
			PlannerActor: set.PlannerActor, SecurityActor: set.SecurityActor,
			Security: set.Security, Gate: set.Gate, Regate: set.Regate,
		},
	}
	var members []db.ChainMemberRow
	var cleared []string
	seen := map[string]bool{}
	for _, name := range chainLegacyColumns(c) {
		if name == "" || seen[name] {
			continue
		}
		b, err := tx.Load(name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return workflow.Legacy{}, nil, nil, err
		}
		seen[name] = true
		if name == c.Builder {
			leg.Builder = BindingRole(b)
		}
		members = append(members, db.ChainMemberRow{Binding: name, Actor: b.Role, Seq: len(members)})
		cleared = append(cleared, name)
	}
	return leg, members, cleared, nil
}

// chainClearMemberGate drops a legacy member's own gate and repair budget: the
// workflow's check steps own them now. A member whose record has gone since
// the row was read is a no-op.
func chainClearMemberGate(tx *store.Tx, name string) error {
	b, err := tx.Load(name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.Gate == "" && b.Regate == 0 {
		return nil
	}
	b.Gate = ""
	b.Regate = 0
	return tx.Save(b)
}
