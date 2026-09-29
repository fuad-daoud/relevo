package store

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// PutRoundFile writes one round file of a live binding directly as a
// round_file row, with no file on disk: the artifacts relevo produces without
// ever writing bytes to the binding directory (a drift or diff patch, a
// consult's findings). It is a Tx method, so the state lock is already held.
//
// A path outside the binding's directory, a name without the round-file NNN-
// prefix, a mismatched NNN-, a relative path containing ".." and a round below
// 1 are refused with nothing written. A path inside a top-level NNN-<actor>/
// directory of the binding is accepted and the row's name is
// "NNN-<actor>/<rel>"; the first segment's NNN must equal round. A name with
// no live record is an ErrNotFound error, never a silent no-op: an unrecorded
// put would vanish, which is what writing a file never did.
func (t *Tx) PutRoundFile(name string, round int, path string, body []byte) error {
	file, ok := roundFileRel(t.s.Dir(name), path)
	if !ok {
		return fmt.Errorf("put round file %s: not a round file of binding %q", path, name)
	}
	if r, ok := roundOfFile(file); !ok || r != round {
		return fmt.Errorf("put round file %s: name %q is round %d, not %d", path, file, r, round)
	}
	if round < 1 {
		return fmt.Errorf("put round file %s: round %d is below 1", path, round)
	}

	d, err := t.s.dbForWrite()
	if err != nil {
		return err
	}
	rec, ok, err := d.RecordGet(t.s.owner, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}

	now := time.Now().UTC()
	return d.Tx(func(dtx *db.Tx) error {
		return dtx.RoundFilePut(rec.ID, file, round, body, now, now)
	})
}
