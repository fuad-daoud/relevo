package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
)

// memberNames maps a member list to name->actor for the tests that only care
// which member takes which name.
func memberNames(t *testing.T, members []chain.PlannedMember) map[string]string {
	t.Helper()
	out := make(map[string]string, len(members))
	for _, m := range members {
		out[m.Name] = m.Actor
	}
	return out
}
