# Cleanup P2-2a -- merge `internal/ledger`, `internal/history`, `internal/latency` into `internal/availability`

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 "availability/"). This round only merges the three small packages; moving
relevo's gate/probe/limit code into it is the next round. Base: `main` at `ad1869eb`.

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

## 1. The merge

Create `internal/availability` holding the contents of
`internal/ledger` (ledger.go 293 lines), `internal/history` (history.go 164) and
`internal/latency` (latency.go 125), as files `ledger.go`, `history.go`,
`latency.go` with their tests (`ledger_test.go`, `history_test.go`,
`latency_test.go`); then delete the three packages.

Names collide once merged. Rename **only** the colliding identifiers, by
prefixing the source package's noun:
- `LoadKV` / `SaveKV` (all three) -> `LoadLedger`/`SaveLedger`,
  `LoadHistory`/`SaveHistory`, `LoadLatency`/`SaveLatency`;
- `Prune` / `Append` (all three) -> `PruneLedger`/`AppendLedger`,
  `PruneHistory`/`AppendHistory`, `PruneLatency`/`AppendLatency`
  (methods keep their names -- only package-level functions collide);
- `History` type (history and latency) -> history's stays `History`, latency's
  becomes `LatencyHistory`;
- `RetainWindow` (history and latency) -> `HistoryRetainWindow`,
  `LatencyRetainWindow`;
- any other collision the build reports: the same prefix rule; list it.
Non-colliding names keep their names (`Kind`, `Entry`, `Ledger`, `Gate`, `Gated`,
`Clear`, `ErrBadEntry`, `Event`, `Cleared`, `FromEntry`, `HourCounts`,
`BlockedDurations`, `Sample`, `Summary`, ...). Test-helper collisions
(`testKV` x3, `TestLoadKVMissingIsEmpty` and `TestSaveKVLoadKVRoundTrip` in two
files, history_test's package `var now`) are renamed the same way.
`history` imported `ledger`; after the merge that is an in-package reference.

## 2. Requalify every importer

About 68 files import these packages: cmd/relevo (candidates.go, doctor_checks.go,
doctor_test.go), internal/relevo (available, candidate, candidates_list, ledger,
limit, policy_view, send, status, switch, unused_gates, statsinputs, probe and
their tests), internal/serve (admin.go, admin_test, serve_test), internal/stats
(reliability.go, stats.go, stats_test), internal/ui (candidate_form, confirm,
view_actors, view_candidates, view_candidates_unused, view_log, view_stats and
their tests). `go build ./... && go vet ./...` finds any you miss. Where a file
imported two of the three, it now imports `availability` once.
Check no cycle: `availability` must not import `internal/stats` or `internal/relevo`.

## 3. Steps

1. §1; `go build ./internal/availability/... && go test ./internal/availability/... -count=1`.
2. §2; `go build ./... && go vet ./...`.
3. `go test ./internal/relevo/... ./internal/stats/... ./internal/serve/... ./internal/ui/... ./cmd/relevo/... -count=1`.
4. Remove the three packages' rules from `.golangci.yml` and their lines from the
   allow-lists; `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. Coverage (§0); `make check` (foreground) passes.
6. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-availability-a.md`; commit once.

Report: every rename (old -> new), importers changed (count per package),
coverage lines changed.
