package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Query builds one SELECT over round JOIN binding LEFT JOIN repo from the
// non-zero Filter fields, every value parameterised.
func (d *DB) Query(f Filter) ([]RoundRow, error) {
	return queryRounds(context.Background(), d.sqlDB, f)
}

func (t *Tx) Query(f Filter) ([]RoundRow, error) { return queryRounds(t.ctx, t.conn, f) }

func queryRounds(ctx context.Context, q queryer, f Filter) ([]RoundRow, error) {
	if f.Here != "" {
		return nil, fmt.Errorf("db: query: Here must be resolved: %w", ErrInvalid)
	}

	where, args := roundFilter(f)
	query := `SELECT round.binding_id, binding.name, repo.origin_url, repo.common_dir, binding.feature,
			binding.ticket,
			round.number, round.started_at, round.closed_at, round.outcome,
			round.candidate, round.harness, round.provider, round.model, round.actor,
			round.commits, round.tree, round.gate_result, round.cost_usd, round.cost_basis,
			round.in_tokens, round.cache_tokens, round.write_tokens, round.out_tokens,
			round.report_outcome, round.mode, binding.server,
			binding.archived_at, round.switches
		FROM round
		JOIN binding ON binding.id = round.binding_id
		LEFT JOIN repo ON repo.id = binding.repo_id` + where
	if f.Newest {
		query += ` ORDER BY round.started_at DESC`
	} else {
		query += ` ORDER BY round.started_at ASC`
	}
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query: %w", err)
	}
	out, err := collectRows(rows, scanRoundRow)
	if err != nil {
		return nil, fmt.Errorf("db: query: %w", err)
	}
	return out, nil
}

// roundFilter turns the round-scoped Filter fields into a WHERE clause.
func roundFilter(f Filter) (string, []any) {
	var where []string
	var args []any
	add := func(clause string, vals ...any) {
		where = append(where, clause)
		args = append(args, vals...)
	}

	if f.Repo != "" {
		add(`(repo.origin_url = ? OR repo.common_dir = ?)`, f.Repo, f.Repo)
	}
	if f.Feature != "" {
		add(`binding.feature = ?`, f.Feature)
	}
	ticketFilter(f, add)
	if f.Binding != "" {
		add(`binding.name = ?`, f.Binding)
	}
	if f.MasterMind != "" {
		add(`binding.mastermind_id IN (SELECT id FROM mastermind WHERE session_id = ?)`, f.MasterMind)
	}
	if f.Harness != "" {
		add(`round.harness = ?`, f.Harness)
	}
	if f.Provider != "" {
		add(`round.provider = ?`, f.Provider)
	}
	if f.Model != "" {
		add(`round.model = ?`, f.Model)
	}
	if f.Candidate != "" {
		add(`round.candidate = ?`, f.Candidate)
	}
	if f.Outcome != "" {
		add(`round.outcome = ?`, f.Outcome)
	}
	if f.ReportOutcome != "" {
		add(`round.report_outcome = ?`, f.ReportOutcome)
	}
	if f.GateResult != "" {
		add(`round.gate_result = ?`, f.GateResult)
	}
	if f.CostBasis != "" {
		add(`round.cost_basis = ?`, f.CostBasis)
	}
	if f.State != "" {
		add(`binding.final_state = ?`, f.State)
	}
	if f.Round != 0 {
		add(`round.number = ?`, f.Round)
	}
	if !f.Since.IsZero() {
		add(`round.started_at >= ?`, formatTime(f.Since))
	}
	if !f.Until.IsZero() {
		add(`round.started_at < ?`, formatTime(f.Until))
	}
	if f.Archived != nil {
		if *f.Archived {
			where = append(where, `binding.archived_at IS NOT NULL`)
		} else {
			where = append(where, `binding.archived_at IS NULL`)
		}
	}

	if len(where) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(where, " AND "), args
}

// ticketMatch builds the WHERE clause for Filter.Ticket. A value naming a
// repository (owner/repo#N) matches the stored form exactly; a bare N or #N
// matches any stored ticket ending in #N. The number is digits only, so the
// LIKE pattern takes no wildcard from the value; an unparseable value matches
// nothing, because no stored ticket equals it.
func ticketMatch(value string) (string, any) {
	if n, ok := bareTicketNumber(value); ok {
		return `binding.ticket LIKE '%#' || ?`, n
	}
	return `binding.ticket = ?`, value
}

// bareTicketNumber returns the digits of a bare "N" or "#N" ticket value.
func bareTicketNumber(s string) (string, bool) {
	n := strings.TrimPrefix(s, "#")
	if n == "" {
		return "", false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return "", false
		}
	}
	return n, true
}

// ticketFilter adds the ticket clause when one is set.
func ticketFilter(f Filter, add func(string, ...any)) {
	if f.Ticket == "" {
		return
	}
	clause, arg := ticketMatch(f.Ticket)
	add(clause, arg)
}

func scanRoundRow(s rowScanner) (RoundRow, error) {
	var row RoundRow
	var origin, commonDir, feature, ticket sql.Null[string]
	var startedAt string
	var closedAt sql.Null[string]
	var candidate, harness, provider, model sql.Null[string]
	var commits sql.Null[int64]
	var tree, gateResult sql.Null[string]
	var costUSD sql.Null[float64]
	var costBasis sql.Null[string]
	var inTokens, cacheTokens, writeTokens, outTokens sql.Null[int64]
	var reportOutcome, mode, server sql.Null[string]
	var archivedAt sql.Null[string]

	if err := s.Scan(&row.BindingID, &row.BindingName, &origin, &commonDir, &feature, &ticket,
		&row.Number, &startedAt, &closedAt, &row.Outcome,
		&candidate, &harness, &provider, &model, &row.Actor,
		&commits, &tree, &gateResult, &costUSD, &costBasis,
		&inTokens, &cacheTokens, &writeTokens, &outTokens,
		&reportOutcome, &mode, &server,
		&archivedAt, &row.Switches); err != nil {
		return RoundRow{}, fmt.Errorf("scan: %w", err)
	}

	st, err := parseTime(startedAt)
	if err != nil {
		return RoundRow{}, fmt.Errorf("parse started_at: %w", err)
	}
	row.StartedAt = st
	row.Repo = firstValid(origin, commonDir)
	row.Feature = ptrIfValid(feature)
	row.Ticket = ptrIfValid(ticket)
	if row.ClosedAt, err = nullTimeFrom(closedAt); err != nil {
		return RoundRow{}, fmt.Errorf("parse closed_at: %w", err)
	}
	if row.ClosedAt != nil {
		ms := row.ClosedAt.Sub(row.StartedAt).Milliseconds()
		row.DurationMS = &ms
	}
	row.Candidate = ptrIfValid(candidate)
	row.Harness = ptrIfValid(harness)
	row.Provider = ptrIfValid(provider)
	row.Model = ptrIfValid(model)
	row.Commits = intPtr(commits)
	row.Tree = ptrIfValid(tree)
	row.GateResult = ptrIfValid(gateResult)
	row.CostUSD = ptrIfValid(costUSD)
	row.CostBasis = ptrIfValid(costBasis)
	row.InTokens = ptrIfValid(inTokens)
	row.CacheTokens = ptrIfValid(cacheTokens)
	row.WriteTokens = ptrIfValid(writeTokens)
	row.OutTokens = ptrIfValid(outTokens)
	row.ReportOutcome = ptrIfValid(reportOutcome)
	row.Mode = ptrIfValid(mode)
	row.Server = ptrIfValid(server)
	if archivedAt.Valid {
		row.Archived = true
		at, err := parseTime(archivedAt.V)
		if err != nil {
			return RoundRow{}, fmt.Errorf("parse archived_at: %w", err)
		}
		row.ArchivedAt = &at
	}

	return row, nil
}

// bindingColumns is the column list both binding readers scan.
const bindingColumns = `binding.id, binding.name, binding.repo_id, binding.mastermind_id, binding.feature,
	binding.ticket,
	binding.forked_from_binding_id, binding.forked_from_round, binding.cwd, binding.worktree,
	binding.branch, binding.base_commit, binding.tier, binding.gate, binding.builder_mode,
	binding.server, binding.created_at, binding.final_state, binding.archived_at, binding.archive_path,
	binding.ingest_source, repo.origin_url, repo.common_dir,
	(SELECT COUNT(*) FROM round WHERE round.binding_id = binding.id),
	COALESCE((SELECT MAX(round.started_at) FROM round WHERE round.binding_id = binding.id), binding.created_at) AS last_activity`

func scanBindingRow(s rowScanner) (BindingRow, error) {
	var br BindingRow
	var repoID, mastermindID, feature, ticket, forkedFromBindingID sql.Null[string]
	var forkedFromRound sql.Null[int64]
	var worktree, branch, baseCommit, tier, gate, server sql.Null[string]
	var createdAt string
	var finalState, archivedAt, archivePath sql.Null[string]
	var originURL, commonDir sql.Null[string]
	var lastActivity string

	if err := s.Scan(&br.ID, &br.Name, &repoID, &mastermindID, &feature, &ticket,
		&forkedFromBindingID, &forkedFromRound, &br.CWD, &worktree,
		&branch, &baseCommit, &tier, &gate, &br.BuilderMode,
		&server, &createdAt, &finalState, &archivedAt, &archivePath,
		&br.IngestSource, &originURL, &commonDir,
		&br.Rounds, &lastActivity); err != nil {
		return BindingRow{}, fmt.Errorf("scan: %w", err)
	}

	br.RepoID = ptrIfValid(repoID)
	br.MasterMindID = ptrIfValid(mastermindID)
	br.Feature = ptrIfValid(feature)
	br.Ticket = ptrIfValid(ticket)
	br.ForkedFromBindingID = ptrIfValid(forkedFromBindingID)
	br.ForkedFromRound = intPtr(forkedFromRound)
	br.Worktree = ptrIfValid(worktree)
	br.Branch = ptrIfValid(branch)
	br.BaseCommit = ptrIfValid(baseCommit)
	br.Tier = ptrIfValid(tier)
	br.Gate = ptrIfValid(gate)
	br.Server = ptrIfValid(server)

	ct, err := parseTime(createdAt)
	if err != nil {
		return BindingRow{}, fmt.Errorf("parse created_at: %w", err)
	}
	br.CreatedAt = ct
	br.FinalState = ptrIfValid(finalState)
	if br.ArchivedAt, err = nullTimeFrom(archivedAt); err != nil {
		return BindingRow{}, fmt.Errorf("parse archived_at: %w", err)
	}
	br.ArchivePath = ptrIfValid(archivePath)
	br.RepoOrigin = ptrIfValid(originURL)
	br.RepoCommonDir = ptrIfValid(commonDir)

	la, err := parseTime(lastActivity)
	if err != nil {
		return BindingRow{}, fmt.Errorf("parse last_activity: %w", err)
	}
	br.LastActivity = la

	return br, nil
}

// Bindings lists bindings matching the binding-scoped fields of f, newest
// activity first.
func (d *DB) Bindings(f Filter) ([]BindingRow, error) {
	return queryBindings(context.Background(), d.sqlDB, f)
}

func (t *Tx) Bindings(f Filter) ([]BindingRow, error) { return queryBindings(t.ctx, t.conn, f) }

func queryBindings(ctx context.Context, q queryer, f Filter) ([]BindingRow, error) {
	if f.Here != "" {
		return nil, fmt.Errorf("db: bindings: Here must be resolved: %w", ErrInvalid)
	}

	where, args := bindingFilter(f)
	query := `SELECT ` + bindingColumns + `
		FROM binding
		LEFT JOIN repo ON repo.id = binding.repo_id` + where +
		` ORDER BY last_activity DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: bindings: %w", err)
	}
	out, err := collectRows(rows, scanBindingRow)
	if err != nil {
		return nil, fmt.Errorf("db: bindings: %w", err)
	}
	return out, nil
}

// bindingFilter turns the binding-scoped Filter fields into a WHERE clause.
func bindingFilter(f Filter) (string, []any) {
	var where []string
	var args []any
	add := func(clause string, vals ...any) {
		where = append(where, clause)
		args = append(args, vals...)
	}

	if f.Repo != "" {
		add(`(repo.origin_url = ? OR repo.common_dir = ?)`, f.Repo, f.Repo)
	}
	if f.Feature != "" {
		add(`binding.feature = ?`, f.Feature)
	}
	ticketFilter(f, add)
	if f.Binding != "" {
		add(`binding.name = ?`, f.Binding)
	}
	if f.MasterMind != "" {
		add(`binding.mastermind_id IN (SELECT id FROM mastermind WHERE session_id = ?)`, f.MasterMind)
	}
	if f.State != "" {
		add(`binding.final_state = ?`, f.State)
	}
	if f.Archived != nil {
		if *f.Archived {
			where = append(where, `binding.archived_at IS NOT NULL`)
		} else {
			where = append(where, `binding.archived_at IS NULL`)
		}
	}

	if len(where) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(where, " AND "), args
}

func (d *DB) Binding(name string) (BindingRow, bool, error) {
	return getBinding(context.Background(), d.sqlDB, name)
}

func (t *Tx) Binding(name string) (BindingRow, bool, error) { return getBinding(t.ctx, t.conn, name) }

func getBinding(ctx context.Context, q queryer, name string) (BindingRow, bool, error) {
	query := `SELECT ` + bindingColumns + `
		FROM binding
		LEFT JOIN repo ON repo.id = binding.repo_id
		WHERE binding.name = ?
		ORDER BY binding.created_at DESC
		LIMIT 1`

	br, err := scanBindingRow(q.QueryRowContext(ctx, query, name))
	if errors.Is(err, sql.ErrNoRows) {
		return BindingRow{}, false, nil
	}
	if err != nil {
		return BindingRow{}, false, fmt.Errorf("db: binding: %w", err)
	}
	return br, true, nil
}
