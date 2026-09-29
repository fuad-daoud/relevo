package bugreport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// LastErrorFile is the state-root slot the failure record lives in: one file,
// replaced by the next failure rather than appended to.
const LastErrorFile = "last-error.json"

// LastError is what one coded `internal` failure left behind: enough for a
// bundle to carry the failing command without a person retyping it.
type LastError struct {
	Time    time.Time `json:"time"`
	Version string    `json:"version"`
	Verb    string    `json:"verb"`
	Argv    []string  `json:"argv"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Next    string    `json:"next"`
}

// WriteLastError writes e to <root>/last-error.json, replacing whatever was
// there: the slot holds the newest failure, not a log. The write is a temp file
// in the same directory and then a rename, owner-only, so a reader never sees
// half a record. The root itself is created owner-only when it is missing.
func WriteLastError(root string, e LastError) error {
	if root == "" {
		return errors.New("last error: no state root")
	}
	if err := os.MkdirAll(root, store.StateRootMode); err != nil {
		return fmt.Errorf("last error: create %s: %w", root, err)
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("last error: encode: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(root, ".last-error-*.json.tmp")
	if err != nil {
		return fmt.Errorf("last error: temp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("last error: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("last error: close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("last error: mode %s: %w", name, err)
	}
	if err := os.Rename(name, filepath.Join(root, LastErrorFile)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("last error: rename: %w", err)
	}
	return nil
}

// ReadLastError reads the slot. A missing file is (LastError{}, false, nil):
// most runs, and most machines, have never failed.
func ReadLastError(root string) (LastError, bool, error) {
	if root == "" {
		return LastError{}, false, nil
	}
	raw, err := os.ReadFile(filepath.Join(root, LastErrorFile))
	if errors.Is(err, os.ErrNotExist) {
		return LastError{}, false, nil
	}
	if err != nil {
		return LastError{}, false, fmt.Errorf("last error: read: %w", err)
	}
	var e LastError
	if err := json.Unmarshal(raw, &e); err != nil {
		return LastError{}, false, fmt.Errorf("last error: decode: %w", err)
	}
	return e, true, nil
}
