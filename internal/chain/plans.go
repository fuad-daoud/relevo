package chain

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// BuilderCheck is the builder member's resolved acceptance command from a
// chain's members: "" when the member carries no gate.
func BuilderCheck(members []store.Binding, builder string) string {
	for _, m := range members {
		if m.Name == builder {
			return m.Gate
		}
	}
	return ""
}
