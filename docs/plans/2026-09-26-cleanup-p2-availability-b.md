# Cleanup P2-2b -- move relevo's gate, probe and limit-matching code into `internal/availability`

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 "availability/"). The previous round merged `ledger`, `history` and `latency`
into `internal/availability`; this round moves the relevo code that belongs
with them. Base: `main` at `25734a04`.

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process; when you end your turn the process exits and the round is
  lost. Do not end your turn until the report and done marker exist.
- **No behaviour change.** Declarations move and are requalified; names change
  only where this plan says. Stored formats (KV keys, JSON, DB rows) and every
  golden / `*contract_test.go` stay byte-identical.
- **golangci-lint v2.14.0 must actually run** (install with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0` if
  `golangci-lint version` differs); use `--allow-parallel-runners`.
- **Touch only the files the plan names.** CI configuration, `scripts/`
  (other than the lint allow-lists), `Makefile` and `.github/` are out of scope
  even if a move seems to call for them; if one does, stop and report.
- **Keep every `t.Parallel()` call.**
- **Do not rebase or fetch.** This tree may have no `origin`; the planner rebases.
- Script mechanical requalification (`gofmt -w -r 'old.X -> new.Y' <files>`, then
  `~/go/bin/goimports -w`), then fix what the build reports.
- New packages are born clean: no lint exclusion, no allow-list entry; their
  comments follow CLAUDE.md "### Code style" (1-3 line package comment; *why*
  only; no issue numbers, `§` or history; no restating names); functions at most
  70 lines. Rewriting moved comments to that standard is allowed.
- Coverage: code moves between packages, so after `make check` run
  `sh scripts/check-coverage.sh --write`, then restore every line for a package
  the round did not touch. Report each changed line.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report. Never add a lint exclusion, `//nolint` or allow-list entry.

## 1. What moves from `internal/relevo` into `internal/availability`

- **`ledger.go`** (the gate API) whole, as `availability/gates.go`:
  `ErrNoGates`, the legacy-path helpers, `SpawnFailedCooldown`,
  `Unavailable`, `Available`, `Gates`, `GateKindText`, `GateTimeText`,
  `GateUntilText`, `SetGateClock` (and its package clock), `GatedNote`,
  `BindingsOnProvider`, `rolesMissingNote` and the rest. Private names other
  relevo code still calls become exported: `providerOf` -> `ProviderOf`,
  `appendEntryLocked` -> `AppendEntryLocked`, `ledgerGates` -> `LedgerGates`,
  `recordSpawnFailure` / `recordSpawnFailureLocked` -> `RecordSpawnFailure` /
  `RecordSpawnFailureLocked`, `gatedNote` -> keep private if only moved tests use it.
- **`available.go`** whole (pure; uses candidate and the ledger types).
- **`probe.go`** whole (`ProbeResult`, `LineExec`, `ProbeCandidate`, `Probe`,
  `ProbeNameWidth`, `FormatProbe`, private helpers). It calls relevo's
  `headlessLaunch` (`headless.go:~175`, pure, harness only): move that function
  to `internal/spawn` as `spawn.HeadlessLaunch` (it is also used by
  `headless.go` and `send.go` in relevo) -- if `spawn` importing `harness`
  creates a cycle, stop and report.
- **`limit.go` -- the pure half only:** `LimitMatch`, the regexes and month
  table, `matchLimit` -> `MatchLimit`, `parseReset`, `inLimitWindow`, the
  window helpers, `limitPatterns` -> `LimitPatterns`, `limitScanLines` ->
  `LimitScanLines`. **Stays in relevo:** `gateOnLimit` and `limitText` (they
  call `switchBuilder` and `currentBuilderTail`): move them to the end of
  `internal/relevo/switch.go`, calling the exported availability functions.
  `limit_test.go` splits the same way: `TestGateOnLimit` and `gateHeadless`
  stay in relevo (scan_scope_test uses `gateHeadless`).
- **From `statsinputs.go`:** `LoadHistory` and `LegacyGatesPath` (duplicate of
  the ledger legacy-path helpers -- keep one). `StatsInputs` stays in relevo
  (`internal/stats` imports availability, so availability must not import stats).

**Does not move:** `tier.go` (tier resolution, not availability),
`candidates_list.go` (rendering; it goes with the view round), `switch.go`.

## 2. Dependencies

Define `availability.Deps{Store *store.Store; Candidates *candidate.Set;
Gates db.KV; GatesDir string; Latency db.KV; Now func() time.Time;
Roles harness.RoleChecker; RoleRegistry func() *roles.Registry}` (the
registry stays lazily built: pass `rt.RoleRegistry` as the func). Functions that
took `rt Runtime` take `d Deps`. In relevo add an **exported** builder
`func AvailabilityDeps(rt Runtime) availability.Deps` (callers outside relevo
have a Runtime: cmd/relevo, serve, ui) and use it everywhere; follow
`IngestDeps` in runtime.go.

Callers to update (origin/main at planning time; `go build ./...` finds the rest):
- cmd/relevo: gate.go (Unavailable, Available, Gates, GateUntilText,
  BindingsOnProvider, ClearedByPlanner, ErrUnknownProvider), candidates.go
  (Gates, Probe, ProbeResult, ProbeNameWidth, FormatProbe, LoadHistory,
  LegacyGatesPath), doctor.go / doctor_checks.go (Gates, GateKindText,
  GateTimeText, GateUntilText), ask.go / bind.go (GatedNote), serve_admin.go.
- internal/serve: admin.go (`ledgerRuntime`, Unavailable, Available, Gates,
  GateKindText, GateUntilText), bindings.go, candidates.go.
- internal/ui: actions.go (Unavailable, Available, GatedNote, BindingsOnProvider,
  GateUntilText, LineExec, Probe helpers), ui.go (LineExec), golden_test.go and
  split_test.go (SetGateClock).
- internal/relevo: add, ask, bind, builder_change, candidate, candidates_list,
  headless (RecordSpawnFailureLocked, LimitScanLines), policy_view, reconcile
  (limitText), send, served, stale, statsinputs, status, switch (LedgerGates),
  verify, and their tests (pick_test, switch_test, stale_test, scan_scope_test,
  headless_test, thinking_scan_test, roles_gate_test, status_test, ...).

Moved tests need their own small helpers (`testGateKV`, `candidateSet` and the
test candidate JSON, `rolesFileRegistry`, `baseTime`); tests that stay in relevo
use the exported names.

## 3. Steps

1. `spawn.HeadlessLaunch` first; build.
2. §1 moves; `go build ./internal/availability/...`.
3. §2 in relevo, cmd/relevo, serve, ui; `go build ./... && go vet ./...`
   (check `availability` imports neither `internal/relevo` nor `internal/stats`).
4. `go test ./internal/availability/... ./internal/spawn/... ./internal/relevo/... ./internal/serve/... ./internal/ui/... ./cmd/relevo/... -count=1`.
5. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok (if a moved
   file pushes `availability` over the 600-line file limit, split it by topic).
6. Coverage (§0); `make check` (foreground) passes.
7. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-availability-b.md`; commit once.

Report: every moved and renamed identifier (old -> new), what stayed and why,
callers changed per package, coverage lines changed.
