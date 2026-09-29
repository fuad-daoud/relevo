package store

import (
	"github.com/fuad-daoud/relevo/internal/db"
)

// MachineOpener opens the machine database at path without going through the
// store's own file open. ok is true when the opener owns the answer -- the
// returned handle when err is nil, the refusal when it is not -- and false when
// the caller should open the file itself, which is how a store rooted anywhere
// but the machine root is reached.
//
// The handle an opener returns is the store's handle: the store neither loads
// the installation file nor mints one beside it.
type MachineOpener func(path string) (*db.DB, bool, error)

// machineOpener is the process-wide route an opener installs; nil means every
// store opens <root>/relevo.db directly, which is what a test gets.
var machineOpener MachineOpener

// SetMachineOpener installs open as the machine-database route. Passing nil
// restores the direct open.
func SetMachineOpener(open MachineOpener) { machineOpener = open }
