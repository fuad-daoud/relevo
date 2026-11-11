package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WALHealFailure records a WAL heal the daemon gave up on: the poisoned pool
// survived the whole attempt budget, so a human has to look at the database.
// It lives in a file rather than the database because the pool behind the
// database is the broken thing.
type WALHealFailure struct {
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
}

// walHealFailureFileName is the marker's name in the state root.
const walHealFailureFileName = "wal-heal-failed.json"

// walHealFailurePath is the marker's path: beside the database, never inside
// it.
func (s *Store) walHealFailurePath() string {
	return filepath.Join(s.root, walHealFailureFileName)
}

// WriteWALHealFailure records a heal the daemon is not retrying. One short
// write, so a reader never sees a half-written record.
func (s *Store) WriteWALHealFailure(attempts int, at time.Time, reason string) error {
	raw, err := json.Marshal(WALHealFailure{Attempts: attempts, At: at, Reason: reason})
	if err != nil {
		return fmt.Errorf("marshal wal heal failure: %w", err)
	}
	if err := os.WriteFile(s.walHealFailurePath(), raw, bindingFileMode); err != nil {
		return fmt.Errorf("write wal heal failure: %w", err)
	}
	return nil
}

// ReadWALHealFailure reads the heal marker. A missing file is (zero, false,
// nil), not an error: most daemons never give up on a heal. Malformed JSON is
// an error.
func (s *Store) ReadWALHealFailure() (WALHealFailure, bool, error) {
	raw, err := os.ReadFile(s.walHealFailurePath())
	if errors.Is(err, os.ErrNotExist) {
		return WALHealFailure{}, false, nil
	}
	if err != nil {
		return WALHealFailure{}, false, fmt.Errorf("read wal heal failure: %w", err)
	}
	var failure WALHealFailure
	if err := json.Unmarshal(raw, &failure); err != nil {
		return WALHealFailure{}, false, fmt.Errorf("decode wal heal failure: %w", err)
	}
	return failure, true, nil
}

// ClearWALHealFailure removes the heal marker. A missing file is nil: a heal
// that worked clears the marker whether or not one was ever written.
func (s *Store) ClearWALHealFailure() error {
	if err := os.Remove(s.walHealFailurePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear wal heal failure: %w", err)
	}
	return nil
}
