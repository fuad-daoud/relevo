package relevo

import (
	"os"

	"github.com/fuad-daoud/relevo/internal/store"
)

// chainCopyPlans writes the chain's own plan copies. The row stores them and
// every later send reads them, so a later edit of a source plan changes
// nothing.
func chainCopyPlans(rt Runtime, name string, bodies [][]byte) error {
	if err := os.MkdirAll(rt.Store.ChainDir(name), 0o755); err != nil {
		return err
	}
	for i, body := range bodies {
		if err := os.WriteFile(rt.Store.ChainPlanPath(name, i+1), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// chainStoredMembers reads back the stored members in part order, so the
// caller and its document carry what was actually written: the store fills in
// the fields it owns, like the round cap.
func chainStoredMembers(rt Runtime, members []chainMember) ([]store.Binding, error) {
	out := make([]store.Binding, 0, len(members))
	for _, m := range members {
		b, err := rt.Store.Load(m.name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}
