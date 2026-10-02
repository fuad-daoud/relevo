package store

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Chains are the store's view of the chains tables: reads resolve a chain by
// name or by any of its member bindings, and writes run under the state lock.
// The owner is the store's own, and every row is scoped to the store's origin.

// Chain returns the chain named name.
func (s *Store) Chain(name string) (db.ChainRow, error) {
	var c db.ChainRow
	err := s.read(name, func(tx *Tx) error {
		var err error
		c, err = tx.Chain(name)
		return err
	})
	return c, err
}

// ChainByMember returns the chain that names member as its builder, reviewer,
// planner or security binding.
func (s *Store) ChainByMember(member string) (db.ChainRow, error) {
	var c db.ChainRow
	err := s.read(member, func(tx *Tx) error {
		var err error
		c, err = tx.ChainByMember(member)
		return err
	})
	return c, err
}

// SameChain reports whether two bindings are members of one chain. A binding
// that is in no chain shares none, so a lone writer beside a chain member is
// not exempt from the working-tree clash.
func (s *Store) SameChain(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, err := s.chainByMember(a)
	if err != nil {
		return false
	}
	cb, err := s.chainByMember(b)
	if err != nil {
		return false
	}
	return ca.ID == cb.ID
}

// Chains returns every chain, by name.
func (s *Store) Chains() ([]db.ChainRow, error) {
	var chains []db.ChainRow
	err := s.readAll(func(tx *Tx) error {
		var err error
		chains, err = tx.Chains()
		return err
	})
	return chains, err
}

// ChainEvents returns the named chain's trace, in seq order.
func (s *Store) ChainEvents(name string) ([]db.ChainEventRow, error) {
	var events []db.ChainEventRow
	err := s.read(name, func(tx *Tx) error {
		var err error
		events, err = tx.ChainEvents(name)
		return err
	})
	return events, err
}

func (t *Tx) Chain(name string) (db.ChainRow, error) {
	return t.s.chain(name)
}

func (t *Tx) ChainByMember(member string) (db.ChainRow, error) {
	return t.s.chainByMember(member)
}

func (t *Tx) Chains() ([]db.ChainRow, error) {
	return t.s.chains()
}

func (t *Tx) ChainEvents(name string) ([]db.ChainEventRow, error) {
	return t.s.chainEvents(name)
}

func (t *Tx) ChainPut(c db.ChainRow) error {
	return t.withDB(func(dtx *db.Tx) error { return dtx.ChainPut(c) })
}

// ChainEventAppend appends one trace row to the named chain, with the seq the
// database allocates.
func (t *Tx) ChainEventAppend(name string, e db.ChainEventRow) error {
	c, err := t.s.chain(name)
	if err != nil {
		return err
	}
	return t.withDB(func(dtx *db.Tx) error { return dtx.ChainEventAppend(c.ID, e) })
}

// CreateChain writes a chain, every member binding and the chain's member rows
// in one database transaction: a member that cannot be prepared, or any write
// that fails, leaves neither a chain row, a member binding nor a member row
// behind.
func (t *Tx) CreateChain(c db.ChainRow, members []Binding) error {
	recs := make([]db.Record, 0, len(members))
	rows := make([]db.ChainMemberRow, 0, len(members))
	siblings := make([]string, 0, len(members))
	for _, m := range members {
		siblings = append(siblings, m.Name)
	}
	for _, m := range members {
		b, rec, err := t.s.prepareSave(m, siblings)
		if err != nil {
			return err
		}
		recs = append(recs, rec)
		rows = append(rows, db.ChainMemberRow{ChainID: c.ID, Binding: b.Name, Actor: b.Role, Seq: len(rows)})
	}

	return t.withDB(func(dtx *db.Tx) error {
		for _, rec := range recs {
			if _, err := dtx.RecordPut(rec); err != nil {
				return err
			}
		}
		if err := dtx.ChainPut(c); err != nil {
			return err
		}
		return dtx.ChainMembersPut(c.ID, rows)
	})
}

// ChainMembers returns the named chain's member rows, in creation order.
func (s *Store) ChainMembers(name string) ([]db.ChainMemberRow, error) {
	var members []db.ChainMemberRow
	err := s.read(name, func(tx *Tx) error {
		var err error
		members, err = tx.ChainMembers(name)
		return err
	})
	return members, err
}

func (t *Tx) ChainMembers(name string) ([]db.ChainMemberRow, error) {
	c, err := t.s.chain(name)
	if err != nil {
		return nil, err
	}
	d, err := t.s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	return d.ChainMembers(c.ID)
}

// ChainMembersPut writes the named chain's member rows, for the workflow
// members a resume adds.
func (t *Tx) ChainMembersPut(name string, members []db.ChainMemberRow) error {
	c, err := t.s.chain(name)
	if err != nil {
		return err
	}
	return t.withDB(func(dtx *db.Tx) error { return dtx.ChainMembersPut(c.ID, members) })
}

// ChainCheck returns the named chain's check run.
func (s *Store) ChainCheck(name string, run int) (db.ChainCheckRow, error) {
	var row db.ChainCheckRow
	err := s.read(name, func(tx *Tx) error {
		var err error
		row, err = tx.ChainCheck(name, run)
		return err
	})
	return row, err
}

func (t *Tx) ChainCheck(name string, run int) (db.ChainCheckRow, error) {
	c, err := t.s.chain(name)
	if err != nil {
		return db.ChainCheckRow{}, err
	}
	d, err := t.s.dbForRead()
	if err != nil {
		return db.ChainCheckRow{}, err
	}
	row, ok, err := d.ChainCheckGet(c.ID, run)
	if err != nil {
		return db.ChainCheckRow{}, err
	}
	if !ok {
		return db.ChainCheckRow{}, fmt.Errorf("%s check %d: %w", name, run, ErrNotFound)
	}
	return row, nil
}

// ChainCheckPut writes one check run for the named chain.
func (t *Tx) ChainCheckPut(name string, row db.ChainCheckRow) error {
	c, err := t.s.chain(name)
	if err != nil {
		return err
	}
	row.ChainID = c.ID
	return t.withDB(func(dtx *db.Tx) error { return dtx.ChainCheckPut(row) })
}

// ChainCheckNextRun returns the named chain's next check run number.
func (s *Store) ChainCheckNextRun(name string) (int, error) {
	var run int
	err := s.read(name, func(tx *Tx) error {
		var err error
		run, err = tx.ChainCheckNextRun(name)
		return err
	})
	return run, err
}

func (t *Tx) ChainCheckNextRun(name string) (int, error) {
	c, err := t.s.chain(name)
	if err != nil {
		return 0, err
	}
	var run int
	err = t.withDB(func(dtx *db.Tx) error {
		var err error
		run, err = dtx.ChainCheckNextRun(c.ID)
		return err
	})
	return run, err
}

// ChainSaveWithEvent writes the chain's new state and one trace row in one
// database transaction, so a failure leaves neither.
func (t *Tx) ChainSaveWithEvent(c db.ChainRow, e db.ChainEventRow) error {
	return t.withDB(func(dtx *db.Tx) error {
		if err := dtx.ChainPut(c); err != nil {
			return err
		}
		return dtx.ChainEventAppend(c.ID, e)
	})
}

// withDB runs fn in one database transaction on the store's write handle.
func (t *Tx) withDB(fn func(*db.Tx) error) error {
	d, err := t.s.dbForWrite()
	if err != nil {
		return err
	}
	return d.Tx(fn)
}

func (s *Store) chain(name string) (db.ChainRow, error) {
	d, err := s.dbForRead()
	if err != nil {
		return db.ChainRow{}, err
	}
	if d == nil {
		return db.ChainRow{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	c, ok, err := d.ChainGet(s.owner, name)
	if err != nil {
		return db.ChainRow{}, fmt.Errorf("read chain %q: %w", name, err)
	}
	if !ok {
		return db.ChainRow{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return c, nil
}

func (s *Store) chainByMember(member string) (db.ChainRow, error) {
	d, err := s.dbForRead()
	if err != nil {
		return db.ChainRow{}, err
	}
	if d == nil {
		return db.ChainRow{}, fmt.Errorf("%s: %w", member, ErrNotFound)
	}
	c, ok, err := d.ChainGetByMember(s.owner, member)
	if err != nil {
		return db.ChainRow{}, fmt.Errorf("read chain by member %q: %w", member, err)
	}
	if !ok {
		return db.ChainRow{}, fmt.Errorf("%s: %w", member, ErrNotFound)
	}
	return c, nil
}

func (s *Store) chains() ([]db.ChainRow, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	chains, err := d.ChainList(s.owner)
	if err != nil {
		return nil, fmt.Errorf("read chains: %w", err)
	}
	return chains, nil
}

func (s *Store) chainEvents(name string) ([]db.ChainEventRow, error) {
	c, err := s.chain(name)
	if err != nil {
		return nil, err
	}
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	events, err := d.ChainEvents(c.ID)
	if err != nil {
		return nil, fmt.Errorf("read chain events %q: %w", name, err)
	}
	return events, nil
}
