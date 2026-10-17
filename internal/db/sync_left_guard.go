package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrWriterMoved reports a local write to a binding another machine writes now.
// The triggers would attribute the row to the new writer and this machine's
// exporter would drop it, so the write is refused rather than silently lost.
var ErrWriterMoved = errors.New("db: another machine writes this binding now")

// guardRoot refuses a write under a root this installation handed over or lost
// to a takeover. It is one primary-key probe on a table that is empty on a
// machine that never gave a binding away.
func (t *Tx) guardRoot(rootID string) error {
	if rootID == "" {
		return nil
	}
	var kind string
	err := t.queryRow(`SELECT kind FROM sync_binding_left WHERE root_id = ?`, rootID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: guard root %s: %w", rootID, mapBusy(err))
	}
	return fmt.Errorf("%s was %s away: %w", rootID, kind, ErrWriterMoved)
}

// guardRound is guardRoot for a write keyed by a round id.
func (t *Tx) guardRound(roundID string) error {
	if roundID == "" {
		return nil
	}
	var bindingID string
	err := t.queryRow(`SELECT binding_id FROM round WHERE id = ?`, roundID).Scan(&bindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: guard round %s: %w", roundID, mapBusy(err))
	}
	return t.guardRoot(bindingID)
}

// guardArtifact is guardRoot for a write keyed by an artifact id.
func (t *Tx) guardArtifact(id string) error {
	var roundID sql.NullString
	err := t.queryRow(`SELECT round_id FROM artifact WHERE id = ?`, id).Scan(&roundID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: guard artifact %s: %w", id, mapBusy(err))
	}
	return t.guardRound(roundID.String)
}

// guardName refuses creating a live row under a name another machine writes: the
// new row would share its name with the binding this installation gave away.
func (t *Tx) guardName(name string) error {
	var kind string
	err := t.queryRow(`SELECT kind FROM sync_binding_left WHERE name = ? LIMIT 1`, name).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: guard name %q: %w", name, mapBusy(err))
	}
	return fmt.Errorf("%q was %s away: %w", name, kind, ErrWriterMoved)
}
