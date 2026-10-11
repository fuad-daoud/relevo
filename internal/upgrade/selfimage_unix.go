//go:build unix && !linux

package upgrade

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// SelfImageIdentity falls back to a stat of the resolved executable path: only
// Linux names the running image independently of the path, so elsewhere a
// replacement that lands between exec and this call is still read as the
// baseline. The behaviour is the one that shipped, unchanged.
func SelfImageIdentity() (store.FileID, error) {
	exe, err := ResolveExe()
	if err != nil {
		return store.FileID{}, err
	}

	return ExeIdentity(exe)
}
