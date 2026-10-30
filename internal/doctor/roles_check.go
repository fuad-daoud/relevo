package doctor

import (
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// roleSourceChecks is the one global row saying where the roles came from,
// plus one warning row per legacy field the actors section makes irrelevant.
// In legacy mode there is nothing stale to warn about, so only the OK row is
// printed.
func roleSourceChecks(reg *roles.Registry, set *candidate.Set, pol policy.Policy) []Check {
	detail := "legacy: candidates.json roles, policy.json order and tier"
	if reg.FileMode() {
		detail = "config actors"
	}
	out := []Check{{Name: "actor source", Severity: SevOK, Detail: detail}}
	for _, w := range resolve.LegacyRoleFieldWarnings(reg, set, pol) {
		out = append(out, Check{
			Name:     "actor source",
			Severity: SevWarn,
			Detail:   w,
			Fix:      "delete it; config actors is the source",
		})
	}
	return out
}
