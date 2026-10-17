package db

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// AdoptionBlockers is what stops a machine taking over another's identity: any
// change waiting in an outbox, and any shared row that is not this machine's own
// installation row. Both are history this machine wrote under the identity it is
// about to give up, and neither would belong to the identity it takes.
type AdoptionBlockers struct {
	// Outbox counts sync_outbox rows other than the installation row's own
	// upkeep: every open touches that row, so an empty outbox is not a
	// reachable state on a machine that has run.
	Outbox int
	// SharedOutbox counts the document signals waiting to be exported.
	SharedOutbox int
	// Rows counts the shared rows by table, the own installation row excluded.
	Rows map[string]int
}

// Empty reports whether nothing blocks the adoption.
func (b AdoptionBlockers) Empty() bool {
	return b.Outbox == 0 && b.SharedOutbox == 0 && len(b.Rows) == 0
}

// String names what blocks, in a stable order.
func (b AdoptionBlockers) String() string {
	var parts []string
	if b.Outbox > 0 {
		parts = append(parts, fmt.Sprintf("%d changes waiting to export", b.Outbox))
	}
	if b.SharedOutbox > 0 {
		parts = append(parts, fmt.Sprintf("%d shared documents waiting to export", b.SharedOutbox))
	}
	tables := make([]string, 0, len(b.Rows))
	for table := range b.Rows {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		parts = append(parts, fmt.Sprintf("%d %s rows", b.Rows[table], table))
	}
	return strings.Join(parts, ", ")
}

// AdoptionBlockers reads what stands between this machine and an adoption.
// ignoreDocs names document signals that are not changes of the machine's own:
// every open signals the config document, and the enable's own store step
// signals the secrets one.
func (d *DB) AdoptionBlockers(ignoreDocs ...string) (AdoptionBlockers, error) {
	var out AdoptionBlockers
	err := d.Tx(func(t *Tx) error {
		var err error
		out, err = t.adoptionBlockers()
		return err
	})
	if err != nil {
		return AdoptionBlockers{}, err
	}
	// The signals live in the machine-local file, where SignalDoc writes them.
	query := `SELECT COUNT(*) FROM shared_outbox`
	args := make([]any, 0, len(ignoreDocs))
	if len(ignoreDocs) > 0 {
		query += ` WHERE doc NOT IN (?` + strings.Repeat(`,?`, len(ignoreDocs)-1) + `)`
		for _, doc := range ignoreDocs {
			args = append(args, doc)
		}
	}
	if err := d.LocalOrSelf().sqlDB.QueryRow(query, args...).Scan(&out.SharedOutbox); err != nil {
		return AdoptionBlockers{}, fmt.Errorf("db: adoption blockers: documents: %w", mapBusy(err))
	}
	return out, nil
}

func (t *Tx) adoptionBlockers() (AdoptionBlockers, error) {
	out := AdoptionBlockers{Rows: map[string]int{}}
	if err := t.queryRow(`SELECT COUNT(*) FROM sync_outbox
		WHERE NOT (tbl = 'installation' AND pk = json_array(?))`, t.origin).Scan(&out.Outbox); err != nil {
		return AdoptionBlockers{}, fmt.Errorf("db: adoption blockers: outbox: %w", mapBusy(err))
	}
	for _, table := range SharedTables {
		query := `SELECT COUNT(*) FROM ` + table.Name
		args := []any{}
		if table.Name == "installation" {
			query += ` WHERE id <> ?`
			args = append(args, t.origin)
		}
		var n int
		if err := t.queryRow(query, args...).Scan(&n); err != nil {
			return AdoptionBlockers{}, fmt.Errorf("db: adoption blockers: %s: %w", table.Name, mapBusy(err))
		}
		if n > 0 {
			out.Rows[table.Name] = n
		}
	}
	if len(out.Rows) == 0 {
		out.Rows = nil
	}
	return out, nil
}

// ErrAdoptionDirty is an adoption refused because this machine holds history of
// its own.
var ErrAdoptionDirty = errors.New("db: this machine holds changes of its own")

// AdoptIdentity is the database half of taking over identity id, run once the
// log of id has been imported in full: it drops this machine's own installation
// row, the outbox rows the import queued for id's rows -- they came from id's own
// log, and exported again they would write every row twice -- and id's import
// mark, which becomes this machine's own origin and so has no mark.
//
// Whatever else the outbox holds is a change this machine made after the
// adoption began, under an identity it is leaving; that refuses, and nothing
// changes.
func (d *DB) AdoptIdentity(id string) error {
	if id == "" || id == d.origin {
		return fmt.Errorf("db: adopt %q: %w", id, ErrInvalid)
	}
	return d.Tx(func(t *Tx) error {
		if _, err := t.exec(`DELETE FROM sync_outbox WHERE origin = ?`, id); err != nil {
			return fmt.Errorf("db: adopt %s: outbox: %w", id, mapBusy(err))
		}
		blockers, err := t.adoptionBlockers()
		if err != nil {
			return err
		}
		if blockers.Outbox > 0 {
			return fmt.Errorf("%w: %d changes were made since the adoption began: %w",
				ErrAdoptionDirty, blockers.Outbox, ErrInvalid)
		}
		if _, err := t.exec(`DELETE FROM installation WHERE id = ?`, t.origin); err != nil {
			return fmt.Errorf("db: adopt %s: installation: %w", id, mapBusy(err))
		}
		if _, err := t.exec(`DELETE FROM sync_outbox`); err != nil {
			return fmt.Errorf("db: adopt %s: outbox: %w", id, mapBusy(err))
		}
		if _, err := t.exec(`DELETE FROM sync_import_mark WHERE origin = ?`, id); err != nil {
			return fmt.Errorf("db: adopt %s: mark: %w", id, mapBusy(err))
		}
		return nil
	})
}
