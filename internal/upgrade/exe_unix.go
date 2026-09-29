//go:build unix

package upgrade

import (
	"errors"
	"os"
	"syscall"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ExeIdentity identifies path by device, inode, size and mtime, so a rewrite
// through a fresh inode (install's rename) changes it, a no-op stat does not.
func ExeIdentity(path string) (store.FileID, error) {
	info, err := os.Stat(path)
	if err != nil {
		return store.FileID{}, err
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return store.FileID{}, errors.New("upgrade: no stat_t for " + path)
	}

	return store.FileID{
		Dev:     uint64(st.Dev),
		Ino:     uint64(st.Ino),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}, nil
}
