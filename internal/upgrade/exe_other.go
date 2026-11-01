//go:build !unix

package upgrade

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/store"
)

func ExeIdentity(string) (store.FileID, error) {
	return store.FileID{}, errors.ErrUnsupported
}
