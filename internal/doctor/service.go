package doctor

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/serviceunit"
	"github.com/fuad-daoud/relevo/internal/shipped"
)

// serviceRow reports the daemon unit's state as warnings only: the daemon
// never restarts itself, so a refreshed unit waits for a restart a human
// chooses. No installed unit is no row: the daemon runs without one.
func serviceRow(env Env) (Check, bool) {
	u, err := env.ServiceUnit()
	if err != nil {
		return Check{Name: "service", Severity: SevWarn, Detail: fmt.Sprintf("probe error: %v", err), Fix: "relevo service install", ProbeFailed: true}, true
	}
	if !u.Present {
		return Check{}, false
	}
	fix := "systemctl --user restart relevo.service"
	if u.Doc == serviceunit.PlistName {
		fix = "relevo service install"
	}
	act := serviceunit.DecideUnitAction(u.Doc, u.Installed, true, u.Shipped, shipped.UnitBefore)
	switch act {
	case serviceunit.ActionStale:
		return Check{Name: "service", Severity: SevWarn, Detail: "unit older than shipped", Fix: "relevo service install"}, true
	case serviceunit.ActionEdited:
		return Check{Name: "service", Severity: SevWarn, Detail: "unit edited, not refreshed", Fix: "relevo service install --force"}, true
	}
	if info, ok, ierr := env.DaemonInfo(); ierr == nil && ok && info.StartedAt.Before(u.MTime) {
		return Check{Name: "service", Severity: SevWarn, Detail: "unit refreshed, restart due", Fix: fix}, true
	}
	return Check{Name: "service", Severity: SevOK, Detail: "unit current"}, true
}
