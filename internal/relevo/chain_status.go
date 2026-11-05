package relevo

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// ChainStatus is `relevo status <chain>`: the chain's own row with its member
// bindings listed underneath, builder first. Naming a chain asks for the
// whole chain, not only the roll-up.
func ChainStatus(ctx context.Context, rt Runtime, name string) (view.Report, error) {
	c, err := rt.Store.Chain(name)
	if err != nil {
		return view.Report{}, err
	}
	// A chain that runs on a server answers from the server's view, not this
	// machine's stale mirror.
	if chain.OnServer(c) {
		return chainServerStatus(ctx, rt, c)
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	rep, err := buildReport(ctx, rt, bindings)
	if err != nil {
		return view.Report{}, err
	}

	rows := []view.BindingStatus{view.ChainRow(rt.Store, c)}
	for _, member := range view.ReadMembers(rt.Store, c) {
		for _, b := range rep.Bindings {
			if b.Name == member {
				rows = append(rows, b)
				break
			}
		}
	}
	if warning := chainCleanupStatusWarning(rt, c); warning != "" {
		if rows[0].Detail != "" {
			rows[0].Detail += "; " + warning
		} else {
			rows[0].Detail = warning
		}
	}
	rep.Bindings = rows
	return rep, nil
}

// chainCleanupStatusWarning surfaces failed cleanups beside the chain state:
// every settled cleanup run that did not pass warns, even when the chain
// itself reached done. A chain with no workflow or no failed cleanup warns
// nothing.
func chainCleanupStatusWarning(rt Runtime, c db.ChainRow) string {
	if len(c.WorkflowJSON) == 0 {
		return ""
	}
	def, err := chain.WorkflowDef(c)
	if err != nil {
		return ""
	}
	var warnings []string
	for run := 1; ; run++ {
		row, rerr := rt.Store.ChainCheck(c.Name, run)
		if errors.Is(rerr, store.ErrNotFound) {
			break
		}
		if rerr != nil || !chainCheckSettled(row) {
			continue
		}
		if !isCleanupStep(def, row.Step) {
			continue
		}
		if row.Result == "pass" || row.Result == chainCheckStopped || row.Result == chainCheckStarting || row.Result == "" {
			continue
		}
		warnings = append(warnings, "cleanup "+row.Step+" failed (exit "+strconv.Itoa(row.ExitCode)+")")
	}
	return strings.Join(warnings, "; ")
}
