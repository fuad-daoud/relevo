package main

import (
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// roleSourceChecks is the one global row saying where the roles came from,
// plus one warning row per legacy field the actors section makes irrelevant.
// In legacy mode there is nothing stale to warn about, so only the OK row is
// printed.
func roleSourceChecks(reg *roles.Registry, set *candidate.Set, pol policy.Policy) []doctor.Check {
	detail := "legacy: candidates.json roles, policy.json order and tier"
	if reg.FileMode() {
		detail = "config actors"
	}
	out := []doctor.Check{{Name: "actor source", Severity: doctor.SevOK, Detail: detail}}
	for _, w := range relevo.LegacyRoleFieldWarnings(reg, set, pol) {
		out = append(out, doctor.Check{
			Name:     "actor source",
			Severity: doctor.SevWarn,
			Detail:   w,
			Fix:      "delete it; config actors is the source",
		})
	}
	return out
}
