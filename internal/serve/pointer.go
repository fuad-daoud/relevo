package serve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Initialised reports whether root has any serve state marker: the clients kv
// row, the TLS key secret, or the bindings directory beside them.
func Initialised(root string, d *db.DB) (bool, error) {
	if d != nil {
		if _, ok, err := d.KVGet(clientsKVKey); err != nil {
			return false, err
		} else if ok {
			return true, nil
		}
		if _, ok, err := d.SecretGet(tlsKeySecret); err != nil {
			return false, err
		} else if ok {
			return true, nil
		}
	}

	info, err := os.Stat(filepath.Join(root, "bindings"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// ResolveAdminRoot resolves the root an administrative serve command should use:
// an explicit --state wins; otherwise a live serve.daemon row's root does, with
// a note, and a stale row falls back to default.
func ResolveAdminRoot(explicitState string, d *db.DB, defaultRoot string, alive func(pid int) bool) (root string, note string, err error) {
	if explicitState != "" {
		return filepath.Join(explicitState, "serve"), "", nil
	}
	p, ok, err := db.ReadDaemonPointer(d)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return defaultRoot, "", nil
	}
	if alive(p.PID) {
		if p.Root != defaultRoot {
			return p.Root, fmt.Sprintf("using the running daemon's state: %s (pid %d)", p.Root, p.PID), nil
		}
		return defaultRoot, "", nil
	}
	return defaultRoot, fmt.Sprintf("stale daemon pointer: %s (pid %d is not running)", p.Root, p.PID), nil
}
