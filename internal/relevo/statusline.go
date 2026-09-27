package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// MasterMindStatus filters stored bindings to one mastermind id and builds rows
// through buildReport from the store alone.
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
	return buildReport(ctx, rt, kept)
}
