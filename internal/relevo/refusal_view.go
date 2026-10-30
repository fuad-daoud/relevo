package relevo

import (
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/resolve"
)

// RoleRefusals lists, in harness.RoleNames() order, every role with at
// least one serving candidate that resolve would refuse with no token
// named. It is the one source of doctor's policy rows and relevo
// policy's "would refuse" lines.
func RoleRefusals(set *candidate.Set, pol policy.Policy, gates []availability.Gate) []resolve.RoleRefusal {
	return resolve.RoleRefusals(set, pol, gates)
}
