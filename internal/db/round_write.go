package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UpsertRound inserts or updates r by its natural key: (binding_id, number).
func (t *Tx) UpsertRound(r Round) (string, error) {
	if err := t.guardRoot(r.BindingID); err != nil {
		return "", err
	}
	if r.BindingID == "" {
		return "", fmt.Errorf("db: upsert round: BindingID: %w", ErrInvalid)
	}
	if !ValidOutcome(r.Outcome) {
		return "", fmt.Errorf("db: upsert round: Outcome: %w", ErrInvalid)
	}
	// An empty actor is the builder actor, the column's own default and the seeded actor.
	if r.Actor == "" {
		r.Actor = "builder"
	}

	var id string
	err := t.queryRow(`SELECT id FROM round WHERE binding_id = ? AND number = ?`, r.BindingID, r.Number).Scan(&id)
	if err == nil {
		if uerr := t.updateRound(id, r); uerr != nil {
			return "", fmt.Errorf("db: upsert round: update: %w", mapBusy(uerr))
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("db: upsert round: select: %w", mapBusy(err))
	}

	id = NewID()
	startedAt := r.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if ierr := t.insertRound(id, r, startedAt); ierr != nil {
		return "", fmt.Errorf("db: upsert round: insert: %w", mapBusy(ierr))
	}
	return id, nil
}

// updateRound rewrites one round row, writing nothing when every column
// already holds its value.
func (t *Tx) updateRound(id string, r Round) error {
	return t.updateIfChanged("round", id, []string{"started_at", "closed_at", "outcome", "candidate",
		"account", "harness", "provider", "model", "mode", "actor", "tier",
		"commits", "tree", "gate_result", "gate_exit", "gate_duration_ms",
		"in_tokens", "cache_tokens", "write_tokens", "out_tokens", "cost_usd", "cost_basis",
		"report_outcome", "switches",
		"queue_wait_ms", "start_kind", "rusage_cpu_ms", "rusage_peak_mem_bytes",
		"rusage_base_bytes", "rusage_burst_bytes", "rusage_burst_secs",
		"rusage_cpu_some_usec", "rusage_cpu_full_usec",
		"rusage_mem_some_usec", "rusage_mem_full_usec",
		"rusage_io_some_usec", "rusage_io_full_usec",
		"rusage_ev_high", "rusage_ev_max", "rusage_ev_oom_kill"},
		formatTime(r.StartedAt), nullableTime(r.ClosedAt), r.Outcome, nullableString(r.Candidate),
		nullableString(r.Account),
		nullableString(r.Harness), nullableString(r.Provider), nullableString(r.Model),
		nullableString(r.Mode), r.Actor, nullableString(r.Tier),
		nullableInt(r.Commits), nullableString(r.Tree), nullableString(r.GateResult), nullableInt(r.GateExit),
		nullableInt64(r.GateDurationMS),
		nullableInt64(r.InTokens), nullableInt64(r.CacheTokens), nullableInt64(r.WriteTokens), nullableInt64(r.OutTokens),
		nullableFloat64(r.CostUSD), nullableString(r.CostBasis),
		nullableString(r.ReportOutcome), r.Switches,
		nullableInt64(r.QueueWaitMS), nullableString(r.StartKind),
		nullableInt64(r.RusageCPUMS), nullableInt64(r.RusagePeakMemBytes),
		nullableInt64(r.RusageBaseBytes), nullableInt64(r.RusageBurstBytes), nullableInt64(r.RusageBurstSecs),
		nullableInt64(r.RusageCPUSomeUsec), nullableInt64(r.RusageCPUFullUsec),
		nullableInt64(r.RusageMemSomeUsec), nullableInt64(r.RusageMemFullUsec),
		nullableInt64(r.RusageIOSomeUsec), nullableInt64(r.RusageIOFullUsec),
		nullableInt64(r.RusageEvHigh), nullableInt64(r.RusageEvMax), nullableInt64(r.RusageEvOOMKill))
}

// insertRound writes one round row with its natural key.
func (t *Tx) insertRound(id string, r Round, startedAt time.Time) error {
	if _, err := t.exec(`INSERT INTO round (id, binding_id, number, started_at, closed_at, outcome,
			candidate, account, harness, provider, model, mode, actor, tier,
			commits, tree, gate_result, gate_exit, gate_duration_ms,
			in_tokens, cache_tokens, write_tokens, out_tokens, cost_usd, cost_basis,
			report_outcome, switches,
			queue_wait_ms, start_kind, rusage_cpu_ms, rusage_peak_mem_bytes,
			rusage_base_bytes, rusage_burst_bytes, rusage_burst_secs,
			rusage_cpu_some_usec, rusage_cpu_full_usec,
			rusage_mem_some_usec, rusage_mem_full_usec,
			rusage_io_some_usec, rusage_io_full_usec,
			rusage_ev_high, rusage_ev_max, rusage_ev_oom_kill)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, r.BindingID, r.Number, formatTime(startedAt), nullableTime(r.ClosedAt), r.Outcome,
		nullableString(r.Candidate), nullableString(r.Account), nullableString(r.Harness), nullableString(r.Provider),
		nullableString(r.Model), nullableString(r.Mode), r.Actor, nullableString(r.Tier),
		nullableInt(r.Commits), nullableString(r.Tree), nullableString(r.GateResult), nullableInt(r.GateExit),
		nullableInt64(r.GateDurationMS),
		nullableInt64(r.InTokens), nullableInt64(r.CacheTokens), nullableInt64(r.WriteTokens), nullableInt64(r.OutTokens),
		nullableFloat64(r.CostUSD), nullableString(r.CostBasis),
		nullableString(r.ReportOutcome), r.Switches,
		nullableInt64(r.QueueWaitMS), nullableString(r.StartKind),
		nullableInt64(r.RusageCPUMS), nullableInt64(r.RusagePeakMemBytes),
		nullableInt64(r.RusageBaseBytes), nullableInt64(r.RusageBurstBytes), nullableInt64(r.RusageBurstSecs),
		nullableInt64(r.RusageCPUSomeUsec), nullableInt64(r.RusageCPUFullUsec),
		nullableInt64(r.RusageMemSomeUsec), nullableInt64(r.RusageMemFullUsec),
		nullableInt64(r.RusageIOSomeUsec), nullableInt64(r.RusageIOFullUsec),
		nullableInt64(r.RusageEvHigh), nullableInt64(r.RusageEvMax), nullableInt64(r.RusageEvOOMKill)); err != nil {
		return err
	}
	return nil
}
