package serve

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// settleServed confirms every unconfirmed mastermind-bound log entry for name
// whose round is at most upTo, using route "ack". The caller MUST hold the store
// lock: ConfirmIndex's indices are stable only while nothing appends. upTo <= 0
// is a no-op.
func settleServed(tx *store.Tx, name string, upTo int) (int, error) {
	if upTo <= 0 {
		return 0, nil
	}

	entries, err := tx.ReadLog(name)
	if err != nil {
		return 0, fmt.Errorf("settle %s: %w", name, err)
	}

	n := 0
	for i, e := range entries {
		if e.Direction != store.DirToMasterMind || e.Confirmed || e.Round > upTo {
			continue
		}
		if err := tx.ConfirmIndex(name, i, "ack"); err != nil {
			return n, fmt.Errorf("settle %s: %w", name, err)
		}
		n++
	}
	return n, nil
}

// settleAllServed backfills the ack older servers never wrote: for every owner
// it settles each served binding's mastermind-bound entries up to the round the
// owner acked -- or, for a DONE binding, the daemon's last closed round,
// whichever is further. A per-owner failure is logged and the walk continues;
// a missing bindings dir is nil.
func (s *Server) settleAllServed() error {
	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)

		settled := 0
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			bindings, err := tx.List()
			if err != nil {
				return err
			}
			for _, b := range bindings {
				if b.Serve == nil {
					continue
				}
				upTo := b.Serve.AckedRound
				if b.State == store.StateDone && b.Serve.ClosedRound > upTo {
					upTo = b.Serve.ClosedRound
				}
				n, err := settleServed(tx, b.Name, upTo)
				if err != nil {
					return err
				}
				settled += n
			}
			return nil
		})
		if err != nil {
			slog.Warn("settle served reports failed", "owner", id, "err", err)
			continue
		}
		if settled > 0 {
			slog.Info("settled served reports", "owner", id, "entries", settled)
		}
	}
	return nil
}
