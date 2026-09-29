package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

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
