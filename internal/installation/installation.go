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

// mint creates the installation file at path. The id is written to a fresh
// temp file in the same directory, synced and closed, then linked into place:
// os.Link leaves the winner untouched, so a racing loader sees either no file
// or the winner's whole file -- never the zero-length or partial file an
// O_EXCL create-then-write leaves readable -- and every loser re-reads the
// winner's id instead of overwriting it with a second one. os.Rename would be
// wrong: it clobbers the winner, and each caller would return its own id. Sync
// is what makes "whole file" true after a crash, not just under scheduling.
// The temp file is removed by one deferred cleanup on every path; a crash
// between the write and the link may leave one stray installation.json.tmp-*,
// which read ignores. There is no history and no sweeper.
func mint(path string) (Installation, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return Installation{}, fmt.Errorf("installation: create %s: %w", dir, err)
	}

	inst := Installation{ID: db.NewID(), Label: hostname(), CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(inst)
	if err != nil {
		return Installation{}, fmt.Errorf("installation: encode %s: %w", path, err)
	}

	// os.CreateTemp opens 0600, the same mode the final file needs.
	f, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return Installation{}, fmt.Errorf("installation: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()

	if _, err := f.Write(raw); err != nil {
		return Installation{}, fmt.Errorf("installation: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return Installation{}, fmt.Errorf("installation: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return Installation{}, fmt.Errorf("installation: write %s: %w", tmp, err)
	}

	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return read(path)
		}
		return Installation{}, fmt.Errorf("installation: link %s: %w", path, err)
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
