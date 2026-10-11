//go:build !unix

package upgrade

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/store"
)

// SelfImageIdentity is unsupported off unix, as ExeIdentity is: there is no
// dev/ino to read.
func SelfImageIdentity() (store.FileID, error) {
	return store.FileID{}, errors.ErrUnsupported
}
