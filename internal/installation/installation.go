// Package installation identifies the relevo installation a state root
// belongs to. The identity is a plain file beside the database, not a row:
// a database copy, a backup or a template would otherwise clone one
// installation's id onto another machine, and a shared database would make
// every installation read the same key.
package installation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// FileName is the installation file's name under the state root, beside
// relevo.db.
const FileName = "installation.json"

// fileMode is the installation file's mode. The id names this machine on
// every shared row, so the file is owner-only like the database beside it.
const fileMode = 0o600

// dirMode is the mode of the state root the file is minted into.
const dirMode = 0o700

// Installation is one installation's identity: the origin stamped on the
// shared rows this installation writes, and the label a reader shows for it.
type Installation struct {
	// ID is the installation's ULID. It is the value in every row's origin
	// column, and uniqueness comes from it alone.
	ID string `json:"id"`
	// Label is display-only, and defaults to the hostname. It is not an
	// identity: the laptop and the server may share a hostname.
	Label string `json:"label"`
	// CreatedAt is when the file was first minted.
	CreatedAt time.Time `json:"created_at"`
}

// Load returns the installation at root, minting the file when it is absent.
// The file is authoritative: a second Load returns the same id, and two roots
// get different ids because each mints its own.
func Load(root string) (Installation, error) {
	path := filepath.Join(root, FileName)

	inst, err := read(path)
	if err == nil {
		return inst, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Installation{}, err
	}
	return mint(path)
}

// read decodes the installation file at path. A missing file reports
// os.ErrNotExist so Load can mint one; a file that is not this shape is an
// error, because guessing an identity would silently restamp a machine.
func read(path string) (Installation, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Installation{}, fmt.Errorf("installation: read %s: %w", path, err)
	}
	var inst Installation
	if err := json.Unmarshal(raw, &inst); err != nil {
		return Installation{}, fmt.Errorf("installation: decode %s: %w", path, err)
	}
	if inst.ID == "" {
		return Installation{}, fmt.Errorf("installation: %s has no id: %w", path, db.ErrInvalid)
	}
	return inst, nil
}

// mint creates the installation file at path. O_EXCL is what makes a racing
// first open keep one winner: the loser re-reads the winner's file instead of
// overwriting it with a second id.
func mint(path string) (Installation, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return Installation{}, fmt.Errorf("installation: create %s: %w", filepath.Dir(path), err)
	}

	inst := Installation{ID: db.NewID(), Label: hostname(), CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(inst)
	if err != nil {
		return Installation{}, fmt.Errorf("installation: encode %s: %w", path, err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return read(path)
		}
		return Installation{}, fmt.Errorf("installation: create %s: %w", path, err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return Installation{}, fmt.Errorf("installation: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return Installation{}, fmt.Errorf("installation: write %s: %w", path, err)
	}
	return inst, nil
}

// hostname is the default label. A hostname that cannot be read leaves the
// label empty: the label is display-only, and the id is the identity.
func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}
