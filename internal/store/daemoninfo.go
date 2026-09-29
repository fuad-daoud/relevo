package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// FileID is a file's identity at one instant: the device and inode that name
// the bytes, plus the size and mtime that say whether those bytes changed in
// place. The zero value means "unknown" and never equals a real identity.
type FileID struct {
	Dev     uint64    `json:"dev"`
	Ino     uint64    `json:"ino"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// ReexecFailure records the binary an image refused: the identity that failed
// preflight, when, and why.
type ReexecFailure struct {
	ExeID  FileID    `json:"exe_id"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// DaemonInfo is the daemon's own record, stored in the kv table under the
// "daemon" key. A missing record while the daemon lock is held means a daemon
// older than the record itself: one that does not follow upgrades.
type DaemonInfo struct {
	Version    string    `json:"version"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	Exe        string    `json:"exe"` // resolved path, with no " (deleted)" suffix
	ExeID      FileID    `json:"exe_id"`
	ReexecFrom string    `json:"reexec_from,omitempty"` // previous image's version, on a re-exec
	// ReexecFailed is set while the file at Exe is a build this daemon
	// refused to load.
	ReexecFailed *ReexecFailure `json:"reexec_failed,omitempty"`
}

const daemonInfoKey = "daemon"

// WriteDaemonInfo upserts the daemon's record: one short write, so a reader
// never sees a half-written record.
func (s *Store) WriteDaemonInfo(info DaemonInfo) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal daemon info: %w", err)
	}
	d, err := s.DB()
	if err != nil {
		return err
	}
	return d.KVPut(daemonInfoKey, raw)
}

// ReadDaemonInfo reads the daemon's record. A missing row and a store root
// with no database are (zero, false, nil), not errors. Malformed JSON is an
// error.
func (s *Store) ReadDaemonInfo() (DaemonInfo, bool, error) {
	d, err := s.DBIfExists()
	if err != nil {
		return DaemonInfo{}, false, err
	}
	if d == nil {
		return DaemonInfo{}, false, nil
	}

	raw, ok, err := d.KVGet(daemonInfoKey)
	if err != nil {
		return DaemonInfo{}, false, fmt.Errorf("read daemon info: %w", err)
	}
	if !ok {
		return DaemonInfo{}, false, nil
	}

	var info DaemonInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return DaemonInfo{}, false, fmt.Errorf("decode daemon info: %w", err)
	}
	return info, true, nil
}

// RemoveDaemonInfo deletes the daemon's record. An absent key is nil: the
// clean-shutdown path runs this whether or not the write at start succeeded.
func (s *Store) RemoveDaemonInfo() error {
	d, err := s.DBIfExists()
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	return d.KVDelete(daemonInfoKey)
}
