//go:build !unix

package upgrade

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/store"
)

func SelfImageIdentity() (store.FileID, error) {
	return store.FileID{}, errors.ErrUnsupported
}
