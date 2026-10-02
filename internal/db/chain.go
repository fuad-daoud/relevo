package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Chains are durable state: one chains row per chain plus its append-only
// chain_event trace. Membership lives in the row's four member columns, so a
// closing binding finds its chain with one indexed lookup, and no binding
// record changes.

// ChainRow is one chains row: the state machine's state, the resolved
// settings, the plan copies the chain holds, and the names of its builder,
// reviewer, planner and security bindings.
type ChainRow struct {
	ID             string
	Origin         string
	Owner          string
	Name           string
	Status         string
	Reason         string
	Phase          string
	Step           string
	Plan           int
	Plans          int
	Corrections    int
	PlanPathsJSON  []byte
	SettingsJSON   []byte
	AwaitingMember string
	AwaitingRound  int
	Builder        string
	Reviewer       string
	Planner        string
	Security       string
	Base           string
	Branch         string
	Repo           string
	Worktree       string
	Feature        string
	Ticket         string
	Server         string
	MasterMindID   string
	// PlanStartCommit is the commit the chain's current plan started at; ""
	// for every chain created before the column existed.
	PlanStartCommit string
	// WorkflowJSON is the chain's stored workflow definition and StateJSON its
	// engine state; both are "" for every chain created before the columns
	// existed. Parent names a fork's parent chain.
	WorkflowJSON []byte
	StateJSON    []byte
	Parent       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ChainEventRow is one chain_event row: the transition the trace shows. Event
// and Action are the JSON the chain package encodes; Reason carries a halt
// reason when there is one.
type ChainEventRow struct {
	ChainID string
	Seq     int
	TS      time.Time
	Phase   string
	Step    string
	Member  string
	Round   int
	Plan    int
	Event   string
	Action  string
	Reason  string
}

// Valid reports whether the row carries what a chain needs: an id, a name, a
// status, a phase and a step, at least one plan, and a plan index inside the
// plan list. Every other column has a stored default.
func (c ChainRow) Valid() error {
	for _, f := range []struct{ name, value string }{
		{"id", c.ID},
		{"name", c.Name},
		{"status", c.Status},
		{"phase", c.Phase},
		{"step", c.Step},
	} {
		if f.value == "" {
			return fmt.Errorf("db: chain %s is empty: %w", f.name, ErrInvalid)
		}
	}
	if c.Plans < 1 {
		return fmt.Errorf("db: chain %q has %d plans, want at least 1: %w", c.Name, c.Plans, ErrInvalid)
	}
	if c.Plan < 1 || c.Plan > c.Plans {
		return fmt.Errorf("db: chain %q has plan %d of %d: %w", c.Name, c.Plan, c.Plans, ErrInvalid)
	}
	return nil
}

// hasChains reports whether this database carries the chains tables.
func (d *DB) hasChains() bool { return d.have >= 16 }

// hasChainCustom reports whether this database carries the custom-workflow
// columns and the member and check tables.
func (d *DB) hasChainCustom() bool { return d.have >= 20 }

// hasChainCustom is the transaction's own schema test.
func (t *Tx) hasChainCustom() bool { return t.have >= 20 }

const chainCols = `id, origin, owner, name, status, reason, phase, step, plan, plans, plan_paths, corrections,
	awaiting_member, awaiting_round, settings, builder, reviewer, planner, security,
	base, branch, repo, worktree, feature, ticket, server, mastermind_id, created_at, updated_at, plan_start_commit`

// chainCustomCols are the columns migration 021 adds, read only when the
// database carries them.
const chainCustomCols = `workflow, state, parent`

func scanChain(s rowScanner, custom bool) (ChainRow, error) {
	var c ChainRow
	var planPaths, settings, createdAt, updatedAt string
	var workflow, state, parent string
	dest := []any{&c.ID, &c.Origin, &c.Owner, &c.Name, &c.Status, &c.Reason, &c.Phase, &c.Step,
		&c.Plan, &c.Plans, &planPaths, &c.Corrections, &c.AwaitingMember, &c.AwaitingRound, &settings,
		&c.Builder, &c.Reviewer, &c.Planner, &c.Security, &c.Base, &c.Branch, &c.Repo, &c.Worktree,
		&c.Feature, &c.Ticket, &c.Server, &c.MasterMindID, &createdAt, &updatedAt, &c.PlanStartCommit}
	if custom {
		dest = append(dest, &workflow, &state, &parent)
	}
	if err := s.Scan(dest...); err != nil {
		return ChainRow{}, err
	}
	c.PlanPathsJSON = []byte(planPaths)
	c.SettingsJSON = []byte(settings)
	if custom {
		c.WorkflowJSON = []byte(workflow)
		c.StateJSON = []byte(state)
		c.Parent = parent
	}

	var err error
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return ChainRow{}, fmt.Errorf("parse created_at: %w", err)
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return ChainRow{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return c, nil
}

// chainSelectCols is the column list a chain read selects: the legacy columns,
// plus the custom-workflow ones when the schema carries them.
func (d *DB) chainSelectCols() string {
	if d.hasChainCustom() {
		return chainCols + ", " + chainCustomCols
	}
	return chainCols
}

// chainScanner is the row scanner for the column list chainSelectCols returns.
func (d *DB) chainScanner(s rowScanner) (ChainRow, error) {
	return scanChain(s, d.hasChainCustom())
}

// ChainGet returns owner's chain named name within this handle's origin, and
// whether it was found. A database that predates the chains tables reads as
// absent, never an error.
func (d *DB) ChainGet(owner, name string) (ChainRow, bool, error) {
	if !d.hasChains() {
		return ChainRow{}, false, nil
	}
	c, err := d.chainScanner(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+d.chainSelectCols()+` FROM chains WHERE `+originScope+` AND owner = ? AND name = ?`,
		d.origin, owner, name))
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return ChainRow{}, false, nil
	}
	if err != nil {
		return ChainRow{}, false, fmt.Errorf("db: chain get %s/%q: %w", owner, name, mapBusy(err))
	}
	return c, true, nil
}

// ChainGetByMember returns owner's chain that names member in any of its four
// member columns, and whether it was found. The per-column indexes back the
// OR.
func (d *DB) ChainGetByMember(owner, member string) (ChainRow, bool, error) {
	if !d.hasChains() {
		return ChainRow{}, false, nil
	}
	if d.hasChainCustom() {
		c, ok, err := d.chainByMemberTable(owner, member)
		if err != nil || ok {
			return c, ok, err
		}
	}
	c, err := d.chainScanner(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+d.chainSelectCols()+` FROM chains
			WHERE `+originScope+` AND owner = ? AND (builder = ? OR reviewer = ? OR planner = ? OR security = ?)
			LIMIT 1`,
		d.origin, owner, member, member, member, member))
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return ChainRow{}, false, nil
	}
	if err != nil {
		return ChainRow{}, false, fmt.Errorf("db: chain by member %s/%q: %w", owner, member, mapBusy(err))
	}
	return c, true, nil
}

// chainByMemberTable resolves a member through chain_member. A database that
// predates the table reports nothing, so the caller falls back to the legacy
// columns.
func (d *DB) chainByMemberTable(owner, member string) (ChainRow, bool, error) {
	c, err := d.chainScanner(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+d.chainSelectCols()+` FROM chains JOIN chain_member m ON m.chain_id = chains.id
			WHERE `+originScope+` AND chains.owner = ? AND m.binding = ?
			LIMIT 1`,
		d.origin, owner, member))
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return ChainRow{}, false, nil
	}
	if err != nil {
		return ChainRow{}, false, fmt.Errorf("db: chain by member table %s/%q: %w", owner, member, mapBusy(err))
	}
	return c, true, nil
}

// ChainList returns owner's chains by name. A database that predates the
// chains tables reads as empty.
func (d *DB) ChainList(owner string) ([]ChainRow, error) {
	if !d.hasChains() {
		return nil, nil
	}
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+d.chainSelectCols()+` FROM chains WHERE `+originScope+` AND owner = ? ORDER BY name ASC`,
		d.origin, owner)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: chain list: %w", mapBusy(err))
	}
	out, err := collectRows(rows, d.chainScanner)
	if err != nil {
		return nil, fmt.Errorf("db: chain list: %w", mapBusy(err))
	}
	return out, nil
}

const chainEventCols = `chain_id, seq, ts, phase, step, member, round, event, action, reason, plan`

// ChainEvents returns chain id's trace rows in seq order. A database that
// predates the chains tables reads as empty.
func (d *DB) ChainEvents(id string) ([]ChainEventRow, error) {
	if !d.hasChains() {
		return nil, nil
	}
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+chainEventCols+` FROM chain_event WHERE chain_id = ? ORDER BY seq ASC`, id)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: chain events %s: %w", id, mapBusy(err))
	}
	out, err := collectRows(rows, scanChainEvent)
	if err != nil {
		return nil, fmt.Errorf("db: chain events %s: %w", id, mapBusy(err))
	}
	return out, nil
}

func scanChainEvent(s rowScanner) (ChainEventRow, error) {
	var e ChainEventRow
	var ts string
	if err := s.Scan(&e.ChainID, &e.Seq, &ts, &e.Phase, &e.Step, &e.Member, &e.Round,
		&e.Event, &e.Action, &e.Reason, &e.Plan); err != nil {
		return ChainEventRow{}, err
	}

	var err error
	if e.TS, err = parseTime(ts); err != nil {
		return ChainEventRow{}, fmt.Errorf("parse chain event ts: %w", err)
	}
	return e, nil
}

// ChainPut inserts or updates the live row for c.Owner and c.Name. A hit keeps
// the row's id and created_at, and takes c's other columns; the name is the
// lookup key and never moves.
func (t *Tx) ChainPut(c ChainRow) error {
	if err := c.Valid(); err != nil {
		return err
	}
	updatedAt := c.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = updatedAt
	}

	var id string
	err := t.queryRow(`SELECT id FROM chains WHERE `+originScope+` AND owner = ? AND name = ?`,
		t.origin, c.Owner, c.Name).Scan(&id)
	switch {
	case err == nil:
		return t.updateChain(id, c, updatedAt)
	case errors.Is(err, sql.ErrNoRows):
		return t.insertChain(c, createdAt, updatedAt)
	default:
		return fmt.Errorf("db: chain put %q: select: %w", c.Name, mapBusy(err))
	}
}

func (t *Tx) updateChain(id string, c ChainRow, updatedAt time.Time) error {
	if _, err := t.exec(`UPDATE chains SET origin = ?, owner = ?, status = ?, reason = ?, phase = ?, step = ?,
			plan = ?, plans = ?, plan_paths = ?, corrections = ?, awaiting_member = ?, awaiting_round = ?, settings = ?,
			builder = ?, reviewer = ?, planner = ?, security = ?, base = ?, branch = ?, repo = ?, worktree = ?,
			feature = ?, ticket = ?, server = ?, mastermind_id = ?, plan_start_commit = ?, updated_at = ? WHERE id = ?`,
		t.origin, c.Owner, c.Status, c.Reason, c.Phase, c.Step,
		c.Plan, c.Plans, string(c.PlanPathsJSON), c.Corrections, c.AwaitingMember, c.AwaitingRound, string(c.SettingsJSON),
		c.Builder, c.Reviewer, c.Planner, c.Security, c.Base, c.Branch, c.Repo, c.Worktree,
		c.Feature, c.Ticket, c.Server, c.MasterMindID, c.PlanStartCommit, formatTime(updatedAt), id); err != nil {
		return fmt.Errorf("db: chain put %q: update: %w", c.Name, mapBusy(err))
	}
	return t.chainCustomPut(id, c)
}

// chainCustomPut writes the custom-workflow columns, which are left out of the
// legacy insert and update statements so a database that predates them still
// round-trips.
func (t *Tx) chainCustomPut(id string, c ChainRow) error {
	if !t.hasChainCustom() {
		return nil
	}
	if _, err := t.exec(`UPDATE chains SET workflow = ?, state = ?, parent = ? WHERE id = ?`,
		string(c.WorkflowJSON), string(c.StateJSON), c.Parent, id); err != nil {
		return fmt.Errorf("db: chain put %q: custom columns: %w", c.Name, mapBusy(err))
	}
	return nil
}

func (t *Tx) insertChain(c ChainRow, createdAt, updatedAt time.Time) error {
	if _, err := t.exec(`INSERT INTO chains
			(id, origin, owner, name, status, reason, phase, step, plan, plans, plan_paths, corrections,
			 awaiting_member, awaiting_round, settings, builder, reviewer, planner, security,
			 base, branch, repo, worktree, feature, ticket, server, mastermind_id, created_at, updated_at, plan_start_commit)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, t.origin, c.Owner, c.Name, c.Status, c.Reason, c.Phase, c.Step, c.Plan, c.Plans,
		string(c.PlanPathsJSON), c.Corrections, c.AwaitingMember, c.AwaitingRound, string(c.SettingsJSON),
		c.Builder, c.Reviewer, c.Planner, c.Security, c.Base, c.Branch, c.Repo, c.Worktree,
		c.Feature, c.Ticket, c.Server, c.MasterMindID, formatTime(createdAt), formatTime(updatedAt), c.PlanStartCommit); err != nil {
		return fmt.Errorf("db: chain put %q: insert: %w", c.Name, mapBusy(err))
	}
	return t.chainCustomPut(c.ID, c)
}

// ChainDelete removes owner's chain named name; its trace goes with it through
// the foreign key's ON DELETE CASCADE. The member and check rows carry no
// foreign key, so they are removed here first. No row is a no-op.
func (t *Tx) ChainDelete(owner, name string) error {
	if t.hasChainCustom() {
		if err := t.chainDeleteChildren(owner, name); err != nil {
			return err
		}
	}
	if _, err := t.exec(`DELETE FROM chains WHERE `+originScope+` AND owner = ? AND name = ?`,
		t.origin, owner, name); err != nil {
		return fmt.Errorf("db: chain delete %q: %w", name, mapBusy(err))
	}
	return nil
}

// chainDeleteChildren removes the rows of the member and check tables for the
// chain named name, before the chain row itself goes.
func (t *Tx) chainDeleteChildren(owner, name string) error {
	scope := `SELECT id FROM chains WHERE ` + originScope + ` AND owner = ? AND name = ?`
	for _, table := range []string{"chain_member", "chain_check"} {
		if _, err := t.exec(`DELETE FROM `+table+` WHERE chain_id IN (`+scope+`)`, t.origin, owner, name); err != nil {
			return fmt.Errorf("db: chain delete %q: %s: %w", name, table, mapBusy(err))
		}
	}
	return nil
}

// ChainEventAppend appends one trace row for chain id, with the next seq after
// the chain's current maximum, so a caller never names a seq.
func (t *Tx) ChainEventAppend(id string, e ChainEventRow) error {
	if _, err := t.exec(`INSERT INTO chain_event (chain_id, seq, ts, phase, step, member, round, event, action, reason, plan)
		SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM chain_event WHERE chain_id = ?`,
		id, formatTime(e.TS), e.Phase, e.Step, e.Member, e.Round, e.Event, e.Action, e.Reason, e.Plan, id); err != nil {
		return fmt.Errorf("db: chain event append %s: %w", id, mapBusy(err))
	}
	return nil
}
