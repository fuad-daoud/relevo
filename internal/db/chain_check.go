package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// chain_check records one check step's run for a chain: the command it runs,
// where it got to, and its result. A run is identified by the chain and a run
// number the caller allocates with ChainCheckNextRun, so a re-run never
// overwrites an earlier row.

// ChainCheckRow is one chain_check row.
type ChainCheckRow struct {
	ChainID    string
	Run        int
	Step       string
	Visit      int
	Command    string
	PID        int
	StartedAt  int64
	Attempt    int
	Result     string
	ExitCode   int
	DurationMS int64
	Log        string
	Note       string
	CreatedAt  time.Time
}

const chainCheckCols = `chain_id, run, step, visit, command, pid, started_at, attempt, result, exit_code, duration_ms, log, note, created_at`

// ChainCheckPut writes one check row, replacing any row with the same chain
// and run.
func (t *Tx) ChainCheckPut(c ChainCheckRow) error {
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if _, err := t.exec(`INSERT OR REPLACE INTO chain_check
			(chain_id, run, step, visit, command, pid, started_at, attempt, result, exit_code, duration_ms, log, note, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ChainID, c.Run, c.Step, c.Visit, c.Command, c.PID, c.StartedAt, c.Attempt,
		c.Result, c.ExitCode, c.DurationMS, c.Log, c.Note, formatTime(createdAt)); err != nil {
		return fmt.Errorf("db: chain check put %s/%d: %w", c.ChainID, c.Run, mapBusy(err))
	}
	return nil
}

// ChainCheckGet returns chain id's check run, and whether it was found. A
// database that predates the table reports nothing.
func (d *DB) ChainCheckGet(id string, run int) (ChainCheckRow, bool, error) {
	if !d.hasChainCustom() {
		return ChainCheckRow{}, false, nil
	}
	c, err := scanChainCheck(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+chainCheckCols+` FROM chain_check WHERE chain_id = ? AND run = ?`, id, run))
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return ChainCheckRow{}, false, nil
	}
	if err != nil {
		return ChainCheckRow{}, false, fmt.Errorf("db: chain check get %s/%d: %w", id, run, mapBusy(err))
	}
	return c, true, nil
}

// ChainCheckNextRun returns the next run number for chain id: one past the
// highest already recorded. The count rises with every recorded run, so a
// re-run never reuses a number.
func (t *Tx) ChainCheckNextRun(id string) (int, error) {
	var next int
	if err := t.queryRow(`SELECT COALESCE(MAX(run), 0) + 1 FROM chain_check WHERE chain_id = ?`, id).Scan(&next); err != nil {
		if isMissingTable(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("db: chain check next run %s: %w", id, mapBusy(err))
	}
	return next, nil
}

func scanChainCheck(s rowScanner) (ChainCheckRow, error) {
	var c ChainCheckRow
	var createdAt string
	if err := s.Scan(&c.ChainID, &c.Run, &c.Step, &c.Visit, &c.Command, &c.PID, &c.StartedAt,
		&c.Attempt, &c.Result, &c.ExitCode, &c.DurationMS, &c.Log, &c.Note, &createdAt); err != nil {
		return ChainCheckRow{}, err
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return ChainCheckRow{}, fmt.Errorf("parse chain check created_at: %w", err)
	}
	c.CreatedAt = parsed
	return c, nil
}
