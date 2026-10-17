package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

type pendingDelivery struct {
	member string
	idx    int
}

// collectPendingChainDeliveries finds unconfirmed chain-level end deliveries for
// members of c that a resume should supersede upon success.
func collectPendingChainDeliveries(tx *store.Tx, c db.ChainRow) ([]pendingDelivery, error) {
	var out []pendingDelivery
	for _, member := range chainEndMembers(tx, c) {
		pending, err := tx.PendingForMasterMindThrough(member, 0)
		if err != nil {
			return nil, err
		}
		for _, p := range pending {
			if p.Entry.Kind == store.KindChain {
				out = append(out, pendingDelivery{member: member, idx: p.Idx})
			}
		}
	}
	return out, nil
}

// confirmPendingChainDeliveries marks the collected end deliveries superseded.
func confirmPendingChainDeliveries(tx *store.Tx, deliveries []pendingDelivery) error {
	for _, d := range deliveries {
		if err := tx.ConfirmIndex(d.member, d.idx, "superseded by resume"); err != nil {
			return err
		}
	}
	return nil
}

type chainResumeSnapshot struct {
	origRow     db.ChainRow
	origMembers []db.ChainMemberRow
	origEvents  []db.ChainEventRow
	swapped     map[string]store.Binding
	added       []string
}

func newChainResumeSnapshot(tx *store.Tx, row db.ChainRow) (*chainResumeSnapshot, error) {
	members, err := tx.ChainMembers(row.Name)
	if err != nil {
		return nil, err
	}
	events, err := tx.ChainEvents(row.Name)
	if err != nil {
		return nil, err
	}
	return &chainResumeSnapshot{
		origRow:     row,
		origMembers: members,
		origEvents:  events,
		swapped:     make(map[string]store.Binding),
	}, nil
}

func (s *chainResumeSnapshot) recordSwapped(b store.Binding) {
	if _, ok := s.swapped[b.Name]; !ok {
		s.swapped[b.Name] = b
	}
}

func (s *chainResumeSnapshot) recordAdded(name string) {
	s.added = append(s.added, name)
}

// restore recovers the pre-resume state if an error occurs after writes began.
func (s *chainResumeSnapshot) restore(tx *store.Tx, cause error) error {
	var errs []error
	errs = append(errs, cause)

	if len(s.added) > 0 {
		if err := tx.ChainDelete(s.origRow.Name); err != nil {
			errs = append(errs, fmt.Errorf("restore chain delete: %w", err))
		}
		if err := tx.ChainPut(s.origRow); err != nil {
			errs = append(errs, fmt.Errorf("restore chain put: %w", err))
		}
		if err := tx.ChainMembersPut(s.origRow.Name, s.origMembers); err != nil {
			errs = append(errs, fmt.Errorf("restore chain members: %w", err))
		}
		for _, ev := range s.origEvents {
			if _, err := tx.ChainEventPut(s.origRow.Name, ev); err != nil {
				errs = append(errs, fmt.Errorf("restore chain event: %w", err))
			}
		}
	} else {
		if err := tx.ChainPut(s.origRow); err != nil {
			errs = append(errs, fmt.Errorf("restore chain put: %w", err))
		}
		if err := tx.ChainMembersPut(s.origRow.Name, s.origMembers); err != nil {
			errs = append(errs, fmt.Errorf("restore chain members: %w", err))
		}
	}

	for _, name := range s.added {
		if err := tx.Delete(name); err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, fmt.Errorf("restore delete added member %s: %w", name, err))
		}
	}

	for _, b := range s.swapped {
		if err := tx.Save(b); err != nil {
			errs = append(errs, fmt.Errorf("restore save swapped member %s: %w", b.Name, err))
		}
	}

	return errors.Join(errs...)
}
