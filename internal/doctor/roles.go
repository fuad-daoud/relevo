package doctor

import (
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/store"
)

// BindingRoleChecks reports one FAIL row per binding that is not DONE whose
// role (b.Role, or "builder" when it is empty) the registry does not define.
// A remote binding's role is resolved by its server, so the local registry
// cannot judge it. known is the caller's registry lookup, so internal/doctor
// needs no internal/relevo import.
func BindingRoleChecks(bindings []store.Binding, known func(role string) bool) []Check {
	sorted := append([]store.Binding(nil), bindings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var out []Check
	for _, b := range sorted {
		if b.State == store.StateDone {
			continue
		}
		if b.Builder.Remote() {
			continue
		}
		role := b.Role
		if role == "" {
			role = "builder"
		}
		if known(role) {
			continue
		}
		out = append(out, Check{
			Name:     "binding actor",
			Severity: SevFail,
			Detail:   fmt.Sprintf("binding %s runs actor %q, which config actors no longer defines", b.Name, role),
			Fix:      "restore the actor in config actors, or relevo done " + b.Name,
		})
	}
	return out
}
