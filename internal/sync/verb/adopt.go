package verb

import (
	"fmt"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/installation"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// adoption is the enable's adoption, nil unless the verb carried --as; the
// installation file lives beside the shared database.
func (v *Runner) adoption(verb *wire.SyncVerb) *relevosync.Adoption {
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

// adoptedWarning tells the owner what adoption leaves undone: the daemon reads
// its origin at open, so sync stays off until restart.
func adoptedWarning(id string) string {
	return fmt.Sprintf("this machine is now installation %s; restart the relevo daemon, then run `relevo db sync enable` to finish", id)
}
