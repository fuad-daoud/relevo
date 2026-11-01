package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
)

// lockedPolicy is what a read-only config load does when another process
// holds the file.
type lockedPolicy int

const (
	// lockedSkip reads the files or defaults: the daemon's pre-lock load, which
	// must not dial the daemon it is about to become.
	lockedSkip lockedPolicy = iota
	// lockedDial reads through the owner, so a peek or a bundle sees the live
	// config; it never starts the owner.
	lockedDial
)

// openReadOnlyDB is the direct read-only opener; it is a var so a test can fail
// a held file's open without a real second process.
var openReadOnlyDB = func(path string, o db.Options) (*db.DB, error) {
	return db.OpenReadOnlySplit(path, o)
}

// loadConfigReadOnly reads config without creating, migrating or writing
// anything. When the file is held, onLocked decides: lockedSkip reads the
// files or defaults, and lockedDial reads through the owner.
func loadConfigReadOnly(root, dir string, onLocked lockedPolicy) (config.Loaded, error) {
	switch {
	case configFilesPresent(dir):
		return config.LoadFiles(dir)
	case fileExists(filepath.Join(root, "relevo.db")):
		L, err := loadConfigFromFile(filepath.Join(root, "relevo.db"))
		if err == nil {
			return L, nil
		}
		// A held file, or one this build has not converted yet, leaves the
		// database alone: the former's reader is the owner, and the latter is
		// converted by a writable open, not by this peek.
		if !errors.Is(err, db.ErrLocked) && !errors.Is(err, db.ErrNotConverted) {
			return config.Loaded{}, err
		}
		if onLocked == lockedSkip {
			return config.LoadFiles(dir)
		}
		return loadConfigFromOwner(root, err)
	default:
		return config.LoadFiles(dir)
	}
}

// loadConfigFromFile opens path read-only and loads the config through it.
func loadConfigFromFile(path string) (config.Loaded, error) {
	d, err := openReadOnlyDB(path, db.Options{})
	if err != nil {
		return config.Loaded{}, err
	}
	defer func() { _ = d.Close() }()
	return config.Open(d).Load()
}

// loadConfigFromOwner reads the config through the daemon that holds the file;
// the dial carries the verb budget and never starts the owner.
func loadConfigFromOwner(root string, lockErr error) (config.Loaded, error) {
	d, err := dialOwner(context.Background(), root, verbDialBudget)
	if err != nil {
		return config.Loaded{}, fmt.Errorf("relevo.db is held by the daemon and the owner did not answer: %w (the file open failed with: %v)", err, lockErr)
	}
	defer func() { _ = d.Close() }()
	return config.Open(d).Load()
}

// dialOwner dials the owner on root's socket within budget, without starting
// it; the caller's ctx can end the dial sooner than the budget.
func dialOwner(ctx context.Context, root string, budget time.Duration) (*db.DB, error) {
	sock, err := ownerSocket(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return db.DialContext(ctx, sock)
}

// dialOwnerAdHoc is dialOwner for the ad-hoc read path. The pool's connections
// mark themselves in the handshake, so the owner may refuse while reaping; the
// caller refuses on that answer rather than falling back.
func dialOwnerAdHoc(ctx context.Context, root string, budget time.Duration) (*db.DB, error) {
	sock, err := ownerSocket(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return db.DialContextAdHoc(ctx, sock)
}

// configFilesPresent reports whether any file the import consumes, or a hooks
// directory, is present.
func configFilesPresent(dir string) bool {
	for _, name := range config.Files() {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "hooks")); err == nil && info.IsDir() {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
