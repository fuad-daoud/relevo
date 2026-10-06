package sync

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The seeding marker: the one local row that distinguishes a machine that is
// syncing from a machine whose enable died half way through. It lives in its own
// file because it is a separate question from the seed matrix -- what a machine
// is doing -- and the matrix answers a different one: how a machine that is
// turning sync on should seed a remote.

// MarkSeeding writes the marker that says this machine's first sync round has
// started and not finished.
//
// It is the difference between a machine that is syncing and a machine whose
// enable died half way through. The mark alone cannot tell them apart, because
// the mark is written before the first push and the first pull: a driver that
// aborts the process, or a connection that drops, leaves a machine marked on with
// nothing having moved. This marker is written before the mark and removed after
// the round, so it is set for exactly the window in which the round is unfinished.
//
// It is written before the mark rather than in the same transaction on purpose.
// A death between the two leaves a machine marked off with a stale marker, which
// costs nothing: only a machine that is marked on reads it. The other order would
// leave a machine marked on with no marker, which is the state this marker exists
// to escape.
func MarkSeeding(l Local, now time.Time) error {
	if l == nil {
		return fmt.Errorf("sync: no local file to mark seeding in: %w", db.ErrInvalid)
	}
	body, err := json.Marshal(seeding{At: now.UTC()})
	if err != nil {
		return fmt.Errorf("sync: encode the %s marker: %w", KeySeeding, err)
	}
	if err := l.KVPut(KeySeeding, body); err != nil {
		return fmt.Errorf("sync: write the %s marker: %w", KeySeeding, err)
	}
	return nil
}

// ClearSeeding removes the marker, which the enable does once its first round has
// finished. A failure is not returned: the round did move the data, and the only
// thing a stale marker costs afterwards is that the enable is re-runnable, which
// is the direction that re-decides the seed rather than the one that refuses.
func ClearSeeding(l Local) {
	if l == nil {
		return
	}
	if err := l.KVDelete(KeySeeding); err != nil {
		slog.Warn("sync: delete the "+KeySeeding+" marker", "err", err)
	}
}

// ReadSeeding reports whether this machine's first sync round is unfinished.
func ReadSeeding(l Local) (bool, error) {
	if l == nil {
		return false, fmt.Errorf("sync: no local file to read %s from: %w", KeySeeding, db.ErrInvalid)
	}
	_, ok, err := l.KVGet(KeySeeding)
	if err != nil {
		return false, fmt.Errorf("sync: marker %s: %w", KeySeeding, err)
	}
	return ok, nil
}

// seeding is the body the seeding marker carries. Only the moment is recorded,
// because the only question anyone asks of it is whether the window is open.
type seeding struct {
	At time.Time `json:"at"`
}

// demandSeedUpload makes the seed copy the upload path needs, if it is not
// already there, and returns the refusal that names it.
//
// A second run of an enable that already wrote its copy finds the file and does
// not try to write it again. That is the whole of idempotence here: the copy's
// own writer refuses an existing path, so a run that did not check would refuse
// for a reason the reader cannot act on -- "it already exists" where the fix is
// the upload command the refusal is already carrying. The refusal names the same
// path either way, so a reader who saw it once sees the same sentence again.
func (e *Enabler) demandSeedUpload() error {
	exists, err := e.seedExists()
	if err != nil {
		return fmt.Errorf("sync: enable: the seed copy: %w", err)
	}
	if !exists {
		if e.SeedCopy == nil {
			return ErrSeedUploadRequired
		}
		if err := e.SeedCopy(e.SeedPath); err != nil {
			return fmt.Errorf("sync: enable: seed copy: %w", err)
		}
	}
	return fmt.Errorf(
		"%w; upload %s with `turso db import %s`, then re-run this verb with --seed-uploaded",
		ErrSeedUploadRequired, e.SeedPath, e.SeedPath)
}

// seedExists reports whether the seed copy is already on disk. Nil reports false,
// which is what writing it means.
func (e *Enabler) seedExists() (bool, error) {
	if e.SeedExists == nil {
		return false, nil
	}
	return e.SeedExists(e.SeedPath)
}
