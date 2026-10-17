package relevo

import (
	"fmt"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/installation"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// adoption is the enable's adoption, nil unless the verb carried --as. The
// installation file lives beside the shared database.
func (v *VerbRunner) adoption(verb *wire.SyncVerb) *relevosync.Adoption {
	if verb.AdoptAs == "" {
		return nil
	}
	root := filepath.Dir(v.Path)
	return &relevosync.Adoption{
		ID: verb.AdoptAs,
		Identity: func(id string) error {
			_, err := installation.Adopt(root, id)
			return err
		},
	}
}

// AdoptedWarning tells the owner what an adoption leaves undone: the daemon
// reads its origin at open, so sync stays off until it restarts.
func AdoptedWarning(id string) string {
	return fmt.Sprintf("this machine is now installation %s; restart the relevo daemon, then run `relevo db sync enable` to finish", id)
}
