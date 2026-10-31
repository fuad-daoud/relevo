package chain

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/store"
)

// SnapshotRef is the ref that holds a round's uncommitted work: the
// same namespace the remote allow-list admits and the remote note names, keyed
// on the closing writer and the round that closed.
func SnapshotRef(b store.Binding, round int) string {
	return fmt.Sprintf("refs/relevo/%s/round-%d", b.Name, round)
}

// Dir returns the absolute path to chain's directory in the store
// when name is a member of a chain, or "" when it is not.
func Dir(s *store.Store, tx *store.Tx, name string) string {
	if s == nil {
		return ""
	}
	if tx != nil {
		if c, err := tx.ChainByMember(name); err == nil {
			return s.ChainDir(c.Name)
		}
		return ""
	}
	if c, err := s.ChainByMember(name); err == nil {
		return s.ChainDir(c.Name)
	}
	return ""
}
