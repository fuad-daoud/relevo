//go:build !unix

package upgrade

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ExeIdentity is unsupported off unix: there is no dev/ino to read.
func ExeIdentity(string) (store.FileID, error) {
	return store.FileID{}, errors.ErrUnsupported
}
