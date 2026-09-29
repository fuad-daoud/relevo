# Cleanup P2-3 -- extract `internal/delivery` (getting reports into planner sessions)

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 "delivery/"). Base: `main` at `ad1869eb`.

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

## 1. What moves (from `internal/relevo` to `internal/delivery`)

`deliver.go`, `deliverer.go`, `deliver_agy.go`, `deliver_opencode.go`,
`agy_creds.go`, `opencode_session.go`, `channel.go`, `channel_unix.go`,
`channel_other.go` (build tags move with them), `drain.go`, `giveuplog.go`,
and -- because delivery uses them and they are pure -- `push.go` and `origin.go`.
Their tests move with them (deliver_test, deliver_agy_test,
deliver_opencode_test, agy_creds_test, opencode_session_test, channel_test,
drain_test, giveuplog_test, push_test, origin_test, and `contract_test.go`'s
PushText goldens with their `testdata/contract/push-*.golden` files, unchanged
byte for byte). Names are unchanged except: `logRef` becomes exported `LogRef`.

**Stays in relevo:** `showCommand` (push.go's private helper is used by escape,
gate, headless, reconcile, remote, repair, stop): keep a private copy in relevo
(e.g. in `text.go`), and delivery keeps its own. `plannerRoute` (status.go) stays.

## 2. Dependencies instead of `Runtime`

The moved functions read `rt.Store`, `rt.Now`, `rt.Channels`, `rt.Deliverers`,
`rt.Planners`. Define `delivery.Deps{Store *store.Store; Now func() time.Time;
Channels ClaimStore; Deliverers map[string]PlannerDeliverer; Planners
planner.Registry}`; `Queue`, `DeliverPending`, `Drain` take `d Deps` instead of
`rt Runtime`. The Runtime fields keep their names; their types become
`delivery.ClaimStore` and `map[string]delivery.PlannerDeliverer`. In relevo add
one unexported `func deliveryDeps(rt Runtime) delivery.Deps` (follow
`captureDeps` / `IngestDeps` in runtime.go, including the nil-interface care).

Callers to requalify:
- relevo: `Queue` (consult.go:221, reconcile.go:495), `DeliverPending` and
  `Delivery` (reconcile.go:571), `PushText` and `MaxPushBytes` (pull.go:93,138,145,149),
  `OriginLine` (ask.go, send.go:747), `FindingsCommand` users.
- cmd/relevo: wire.go (deliverers, `AgyEnvPresent`, `ImportAgyCreds`,
  `CaptureAgyCreds`, `OpencodeSessionFinder`, `KVClaims`, `ClaimStore`),
  mcp.go (`Claim`, `ErrClaimHeld`, `Pusher`, `DrainState`, `Drain`),
  ask.go and consult.go (`FindingsCommand`), exec.go (types implementing
  `EnvExec`).
- internal/e2e tests (Claim, KVClaims, Drain, PushText).
Test helpers that other relevo tests still use (`routeRuntime`, `seedPending`,
`fakeClaimStore` from deliver_test.go -- used by pull_test, retry_test,
status_route_test, wait_test) keep a copy in relevo in a test-helper file; the
moved tests get their own copies (`testSecrets`, `testSecretDB`, `testClaims`,
`alwaysAlive`, `baseTime`), plus a `main_test.go` with `dbtest.Main` if the
moved tests open a store.

## 2b. Delete the unused scratch worktree code (owner's decision)

`internal/relevo/scratch.go` (`ErrScratch`, `Scratch`, `CreateScratch`,
`RemoveScratch`, `SweepScratch` and its private helpers) has no production
callers; only `scratch_test.go` uses it. Delete both files. Then delete what
existed only for it -- check each with `git grep` first and delete it only if
nothing else (production or test) uses it:
- `store.Store.ScratchWorktreePath`, `store.Store.ScratchWorktreeDir`;
- `Git` interface methods `AddDetachedWorktree`, `MaterializeTree` in
  `internal/relevo/runtime.go` and their implementations in `internal/git` and
  the relevo test fake (`fake_test.go`), with their tests.
`runGit` (defined in `served_test.go`) stays: remote_test and stop_test use it.
List every declaration deleted, and every one you kept because something else
still uses it.

## 3. Steps

1. Create `internal/delivery`; move §1; `go build ./internal/delivery/...`.
   Then §2b; `go build ./... && go vet ./...`.
2. §2 in relevo, cmd/relevo, e2e; `go build ./... && go vet ./...`
   (check `delivery` does not import `internal/relevo`).
3. `go test ./internal/delivery/... ./internal/relevo/... ./cmd/relevo/... ./internal/mcp/... -count=1`
   and `go test ./internal/e2e/... -count=1`.
4. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. Coverage (§0); `make check` (foreground) passes.
6. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-delivery.md`; commit once.

Report: the final exported API of `internal/delivery`, callers changed, tests
moved and helpers copied, coverage lines changed.
