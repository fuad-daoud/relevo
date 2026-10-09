package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Installation is one row of the installation directory: which installation
// wrote into this shared database, the label its own file carries, and the
// window in which it has been seen here.
type Installation struct {
	ID        string
	Label     string
	FirstSeen time.Time
	LastSeen  time.Time
}

// InstallationList returns every installation that has written into this
// shared database, label-ordered and id-ordered within one label. It reads the
// directory and no row, and it is deliberately unscoped by origin: the point
// of the table is to name the *other* installations too, so scoping it to the
// reader's own origin would hide every row that makes it worth reading.
func (d *DB) InstallationList() ([]Installation, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT id, label, first_seen, last_seen FROM installation ORDER BY label ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("db: installation list: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanInstallation)
	if err != nil {
		return nil, fmt.Errorf("db: installation list: %w", err)
	}
	return out, nil
}

// scanInstallation reads one directory row. A stamp that will not parse is an
// error rather than a zero time, so a corrupt directory is never rendered as a
// machine that was first seen at the epoch.
func scanInstallation(s rowScanner) (Installation, error) {
	var in Installation
	var firstSeen, lastSeen string
	if err := s.Scan(&in.ID, &in.Label, &firstSeen, &lastSeen); err != nil {
		return Installation{}, err
	}
	var err error
	if in.FirstSeen, err = parseTime(firstSeen); err != nil {
		return Installation{}, fmt.Errorf("parse first_seen: %w", err)
	}
	if in.LastSeen, err = parseTime(lastSeen); err != nil {
		return Installation{}, fmt.Errorf("parse last_seen: %w", err)
	}
	return in, nil
}

// InstallationTouch upserts the installation directory row: this
// installation's id, its label and when it was last seen. The row is a
// projection of the installation file beside the database, so a reader on
// another machine can show a label for rows that crossed a machine boundary;
// the file stays authoritative, and first_seen is kept from the first touch.
func (t *Tx) InstallationTouch(id, label string, now time.Time) error {
	if id == "" {
		return fmt.Errorf("db: installation touch: id is empty: %w", ErrInvalid)
	}

	var firstSeen string
	err := t.queryRow(`SELECT first_seen FROM installation WHERE id = ?`, id).Scan(&firstSeen)
	switch {
	case err == nil:
		if _, uerr := t.exec(`UPDATE installation SET label = ?, last_seen = ? WHERE id = ?`,
			label, formatTime(now), id); uerr != nil {
			return fmt.Errorf("db: installation touch %s: update: %w", id, mapBusy(uerr))
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		if _, ierr := t.exec(`INSERT INTO installation (id, label, first_seen, last_seen) VALUES (?,?,?,?)`,
			id, label, formatTime(now), formatTime(now)); ierr != nil {
			return fmt.Errorf("db: installation touch %s: insert: %w", id, mapBusy(ierr))
		}
		return nil
	default:
		return fmt.Errorf("db: installation touch %s: select: %w", id, mapBusy(err))
	}
}
