package relevo

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// RetireRefusal is one name a server retire would not archive: a binding with
// a live local process, or a chain that has not finished. The reason tells the
// human what still holds it.
type RetireRefusal struct {
	Name   string
	Reason string
}

// RetirePlan is what retiring a server would do: the binding names to archive
// and the names refused. Both read sorted, so two plans over the same store
// compare equal.
type RetirePlan struct {
	Archive  []string
	Refusals []RetireRefusal
}

// RetireResult is what retiring a server did: the plan it ran and the names it
// actually archived. Archived is empty on a dry run and on a refusal.
type RetireResult struct {
	Plan     RetirePlan
	Archived []string
}

// PlanServerRetire plans archiving every binding on server without contacting
// it. Archive candidates are remote bindings naming server, deduped by name; a
// candidate with a live process is a refusal instead. Every unfinished chain
// on the server refuses too, naming the chain and its status: finished is done
// and stopped alone, so a halted chain still holds its members. Bindings on
// other servers, and non-remote bindings, appear in neither list.
func PlanServerRetire(bindings []store.Binding, chains []db.ChainRow, live map[string]bool, server string) RetirePlan {
	var plan RetirePlan
	seen := make(map[string]bool)
	for _, b := range bindings {
		if !b.Builder.Remote() || b.Builder.Server != server || seen[b.Name] {
			continue
		}
		seen[b.Name] = true
		if live[b.Name] {
			plan.Refusals = append(plan.Refusals, RetireRefusal{Name: b.Name, Reason: "live local process"})
			continue
		}
		plan.Archive = append(plan.Archive, b.Name)
	}
	for _, c := range chains {
		if c.Server != server || c.Status == string(chain.StatusDone) || c.Status == string(chain.StatusStopped) {
			continue
		}
		plan.Refusals = append(plan.Refusals, RetireRefusal{Name: c.Name, Reason: "chain is " + c.Status})
	}
	slices.Sort(plan.Archive)
	slices.SortFunc(plan.Refusals, func(a, b RetireRefusal) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Reason, b.Reason)
	})
	return plan
}

// RetireServerBindings archives every binding on server without contacting it,
// for a server that is gone from the config. A refusal or a dry run archives
// nothing. Otherwise each planned binding archives through the local-only
// unbind core; the first error stops the run keeping what landed, so a rerun
// over the remainder finishes the rest.
func RetireServerBindings(ctx context.Context, rt Runtime, server string, dryRun bool) (RetireResult, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return RetireResult{}, err
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return RetireResult{}, err
	}
	live := make(map[string]bool)
	for _, b := range bindings {
		if !b.Builder.Remote() || b.Builder.Server != server || b.Builder.PID == 0 {
			continue
		}
		if retireLive(ctx, rt, b) {
			live[b.Name] = true
		}
	}
	plan := PlanServerRetire(bindings, chains, live, server)
	res := RetireResult{Plan: plan}
	if dryRun || len(plan.Refusals) > 0 {
		return res, nil
	}
	for _, name := range plan.Archive {
		if _, err := unbindWithLocal(ctx, rt, name, true, false, true); err != nil {
			return res, err
		}
		res.Archived = append(res.Archived, name)
	}
	return res, nil
}

// retireLive reports whether a binding slated for retire still holds a local
// process. A missing runner and an unreadable check both count as live: the
// retire cannot prove the process is gone, so it fails closed.
func retireLive(ctx context.Context, rt Runtime, b store.Binding) bool {
	if rt.Runner == nil {
		return true
	}
	alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if err != nil {
		slog.Warn("retire liveness check failed; treating as live", "binding", b.Name, "pid", b.Builder.PID, "err", err)
		return true
	}
	return alive
}
