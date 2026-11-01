package availability

import (
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The sync half of a ledger write: what one person's edit of a gate leaves
// behind besides the ledger itself. The ledger row, the per-subject write
// stamps and the document signal all live in one machine-local file, so a write
// that stamped its subject in a second transaction could spend a stamp on a
// ledger it then failed to keep.

// mutateLedgerLocked loads, prunes, applies fn and saves the ledger for a
// caller that already holds the state lock, so every ledger write serialises on
// the state lock the caller already holds. It does not take Store.WithLock
// itself: that lock is a plain mutex and is not reentrant, so a second Lock
// from the goroutine that already holds it blocks forever rather than erroring.
//
// subjects names the ledger subjects the write touched, and are what the write
// stamps: a stamp says this machine last wrote that subject's value, so it
// belongs to the subjects whose value moved and to no other. It is empty for a
// write that is not a person's edit of a gate -- a seed, a migration, an entry
// applied from a peer, a join -- which leaves the ledger changed and the
// document untouched.
func mutateLedgerLocked(d Deps, subjects []string, fn func(Ledger) Ledger) error {
	if d.Gates == nil {
		return ErrNoGates
	}
	l, err := LoadLedger(d.Gates)
	if err != nil {
		return err
	}
	l = fn(l.Prune(d.Now()))
	return saveLedger(d, l, subjects)
}

// saveLedger writes the ledger, and for the subjects a person's edit touched
// records their write stamps and the document signal in the same transaction,
// so a stamp is never spent on a ledger the machine did not keep.
//
// The stamps and the signal land only when the ledger is the machine-local file
// itself. A ledger bound to anything else -- the serve-wide ledger under its
// own prefix, a test double -- is nobody's gates document: there is no stamp to
// record for it and no signal any exporter here would read.
func saveLedger(d Deps, l Ledger, subjects []string) error {
	if len(subjects) == 0 || d.Local == nil || db.KV(d.Local) != d.Gates {
		return SaveLedger(d.Gates, l)
	}
	if err := d.Local.Tx(func(tx *db.Tx) error {
		if err := SaveLedger(tx, l); err != nil {
			return err
		}
		clock := synclog.NewHybridClockAt(tx, d.Now)
		for _, subject := range subjects {
			stamp, err := clock.Now()
			if err != nil {
				return err
			}
			if err := tx.ConfigWriteStampPut(stampKey(subject), stamp.String()); err != nil {
				return err
			}
		}
		return tx.SignalDoc(GatesName)
	}); err != nil {
		return err
	}
	syncNow(d)
	return nil
}

func syncNow(d Deps) {
	if d.SyncNow != nil {
		d.SyncNow()
	}
}
