package db

import (
	"context"
	"fmt"
)

// chain_member names the binding that fills each actor's part in a chain, in
// the order the chain created them. It is the lookup a member's close uses to
// find its chain; the four legacy member columns stay readable for a chain
// written before the table existed.

// ChainMemberRow is one chain_member row.
type ChainMemberRow struct {
	ChainID string
	Binding string
	Actor   string
	Seq     int
}

const chainMemberCols = `chain_id, binding, actor, seq`

// ChainMembersPut writes chainID's member rows, replacing any row that already
// names the same binding. The caller owns the transaction, so a create and its
// members commit together.
func (t *Tx) ChainMembersPut(chainID string, members []ChainMemberRow) error {
	for _, m := range members {
		if _, err := t.exec(`INSERT OR REPLACE INTO chain_member (chain_id, binding, actor, seq) VALUES (?, ?, ?, ?)`,
			chainID, m.Binding, m.Actor, m.Seq); err != nil {
			return fmt.Errorf("db: chain member put %s/%q: %w", chainID, m.Binding, mapBusy(err))
		}
	}
	return nil
}

// ChainMembers returns chain id's member rows in the order the chain created
// them. A database that predates the table reads as empty.
func (d *DB) ChainMembers(id string) ([]ChainMemberRow, error) {
	if !d.hasChainCustom() {
		return nil, nil
	}
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+chainMemberCols+` FROM chain_member WHERE chain_id = ? ORDER BY seq ASC`, id)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: chain members %s: %w", id, mapBusy(err))
	}
	out, err := collectRows(rows, scanChainMember)
	if err != nil {
		return nil, fmt.Errorf("db: chain members %s: %w", id, mapBusy(err))
	}
	return out, nil
}

func scanChainMember(s rowScanner) (ChainMemberRow, error) {
	var m ChainMemberRow
	if err := s.Scan(&m.ChainID, &m.Binding, &m.Actor, &m.Seq); err != nil {
		return ChainMemberRow{}, err
	}
	return m, nil
}
