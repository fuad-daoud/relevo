package db

import "fmt"

// The epoch cuts this file has verified: per origin, the epoch that origin's
// entries above the cut must verify under, and the sequence the cut sits at.
// The table is machine-local beside sync_signed_from -- it records what this
// machine has checked, so a remote row removed or rewritten after the check
// changes nothing -- and the only write is a raise, so a late or replayed row
// cannot move a cut backwards.

// EpochCut is one origin's drop point as this file holds it. The tag is not
// here: what the row keeps is the verdict, and the tag was the evidence.
type EpochCut struct {
	// Epoch is the epoch entries above the cut must verify under.
	Epoch int
	// Seq is the highest sequence the cut vouches for as history.
	Seq int
}

// EpochCuts returns, per origin, the highest (epoch, seq) cut this file has
// verified. A file whose schema predates the table has none.
func (d *DB) EpochCuts() (map[string]EpochCut, error) {
	rows, err := d.sqlDB.Query(`SELECT origin, epoch, seq FROM sync_epoch_cut`)
	if err != nil {
		if isMissingTable(err) {
			return map[string]EpochCut{}, nil
		}
		return nil, fmt.Errorf("db: epoch cuts: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	out := map[string]EpochCut{}
	for rows.Next() {
		var origin string
		var cut EpochCut
		if err := rows.Scan(&origin, &cut.Epoch, &cut.Seq); err != nil {
			return nil, fmt.Errorf("db: epoch cuts: %w", mapBusy(err))
		}
		out[origin] = cut
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: epoch cuts: %w", mapBusy(err))
	}
	return out, nil
}

// RaiseEpochCut records the cut for origin, only ever raising it: a cut this
// file has verified stands until a higher epoch, or a higher sequence at the
// same epoch, replaces it.
func (d *DB) RaiseEpochCut(origin string, epoch, seq int) error {
	return d.Tx(func(t *Tx) error { return t.RaiseEpochCut(origin, epoch, seq) })
}

// RaiseEpochCut is the in-transaction form; the guard is in the statement so
// every caller gets it.
func (t *Tx) RaiseEpochCut(origin string, epoch, seq int) error {
	if epoch < 1 || seq < 0 {
		return fmt.Errorf("db: raise epoch cut %s (%d, %d): %w",
			origin, epoch, seq, ErrInvalid)
	}
	if _, err := t.exec(`INSERT INTO sync_epoch_cut (origin, epoch, seq) VALUES (?, ?, ?)
		ON CONFLICT(origin) DO UPDATE SET epoch = excluded.epoch, seq = excluded.seq
		WHERE excluded.epoch > sync_epoch_cut.epoch
		   OR (excluded.epoch = sync_epoch_cut.epoch AND excluded.seq > sync_epoch_cut.seq)`,
		origin, epoch, seq); err != nil {
		return fmt.Errorf("db: raise epoch cut %s: %w", origin, mapBusy(err))
	}
	return nil
}
