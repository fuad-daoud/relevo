package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// daemonKVKey is the kv row holding the running daemon's pointer.
const daemonKVKey = "serve.daemon"

var initialisedMarkers = []string{"clients.json", "server.key", "bindings"}

// DaemonPointer identifies the state root of a running serve daemon.
type DaemonPointer struct {
	Root      string    `json:"root"`
	PID       int       `json:"pid"`
	Listen    string    `json:"listen"`
	StartedAt time.Time `json:"started_at"`
}

// WriteDaemonPointer records the running daemon in the serve.daemon row.
func WriteDaemonPointer(d *db.DB, p DaemonPointer) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	if err := d.KVPut(daemonKVKey, data); err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	return nil
}

// ReadDaemonPointer reads the serve.daemon row; a nil database is no pointer.
func ReadDaemonPointer(d *db.DB) (p DaemonPointer, ok bool, err error) {
	if d == nil {
		return DaemonPointer{}, false, nil
	}
	data, ok, err := d.KVGet(daemonKVKey)
	if err != nil {
		return DaemonPointer{}, false, fmt.Errorf("read daemon pointer: %w", err)
	}
	if !ok {
		return DaemonPointer{}, false, nil
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return DaemonPointer{}, false, fmt.Errorf("read daemon pointer: %w", err)
	}
	return p, true, nil
}

// RemoveDaemonPointer deletes the serve.daemon row, if it exists.
func RemoveDaemonPointer(d *db.DB) error {
	if d == nil {
		return nil
	}
	return d.KVDelete(daemonKVKey)
}

// Initialised reports whether root has any serve state marker: the clients kv
// row, the TLS key secret, or the bindings directory.
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

	for _, marker := range initialisedMarkers {
		info, err := os.Stat(filepath.Join(root, marker))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if marker == "bindings" {
			if info.IsDir() {
				return true, nil
			}
			continue
		}
		if !info.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// ResolveAdminRoot resolves the root an administrative serve command should use:
// an explicit --state wins; otherwise a live serve.daemon row's root does, with
// a note, and a stale row falls back to default.
func ResolveAdminRoot(explicitState string, d *db.DB, defaultRoot string, alive func(pid int) bool) (root string, note string, err error) {
	if explicitState != "" {
		return filepath.Join(explicitState, "serve"), "", nil
	}
	p, ok, err := ReadDaemonPointer(d)
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
