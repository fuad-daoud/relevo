package db

import (
	"encoding/json"
	"fmt"
	"time"
)

// daemonKVKey is the kv row holding the running daemon's pointer.
const daemonKVKey = "serve.daemon"

// DaemonPointer identifies the state root of a running serve daemon.
type DaemonPointer struct {
	Root      string    `json:"root"`
	PID       int       `json:"pid"`
	Listen    string    `json:"listen"`
	StartedAt time.Time `json:"started_at"`
}

// WriteDaemonPointer records the running daemon in the serve.daemon row. The
// row names a pid, a state root and a listen address on this machine, so it is
// written to -- and read from -- the machine-local file.
func WriteDaemonPointer(d *DB, p DaemonPointer) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	if err := d.LocalOrSelf().KVPut(daemonKVKey, data); err != nil {
		return fmt.Errorf("write daemon pointer: %w", err)
	}
	return nil
}

// ReadDaemonPointer reads the serve.daemon row; a nil database is no pointer.
func ReadDaemonPointer(d *DB) (p DaemonPointer, ok bool, err error) {
	if d == nil {
		return DaemonPointer{}, false, nil
	}
	data, ok, err := d.LocalOrSelf().KVGet(daemonKVKey)
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
func RemoveDaemonPointer(d *DB) error {
	if d == nil {
		return nil
	}
	return d.LocalOrSelf().KVDelete(daemonKVKey)
}
