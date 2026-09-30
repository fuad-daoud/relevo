package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// MasterMindStatus filters stored bindings to one mastermind id and builds rows
// through buildReport from the store alone. Each of that mastermind's live
// chains then replaces its member rows with one chain row (applyChains), so the
// statusline shows one entry per chain.
func MasterMindStatus(ctx context.Context, rt Runtime, mastermindID string) (view.Report, error) {
	if mastermindID == "" {
		return view.Report{}, nil
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	var kept []store.Binding
	for _, b := range bindings {
		if b.MasterMindID == mastermindID && b.State != store.StateDone {
			kept = append(kept, b)
		}
	}
	rep, err := buildReport(ctx, rt, kept)
	if err != nil {
		return view.Report{}, err
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return view.Report{}, err
	}
	var mine []db.ChainRow
	for _, c := range chains {
		if c.MasterMindID == mastermindID {
			mine = append(mine, c)
		}
	}
	return applyChains(rep, mine), nil
}
