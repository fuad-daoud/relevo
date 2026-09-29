# Cleanup P2-1b -- extract `internal/capture` (round diff, drift)

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 "capture/"). Base: `main` at `c8d3a51d`.

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process; when you end your turn the process exits and the round is
  lost. Do not end your turn until the report and done marker exist.
- **No behaviour change.** Declarations move, a few are renamed (§2), their
  `Runtime` parameter becomes a small dependency struct (§3); nothing else.
- **golangci-lint v2.14.0 must actually run** (install it with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0` if
  `golangci-lint version` is not 2.14.0); use `--allow-parallel-runners`.
- **Keep every `t.Parallel()` call.** Goldens and `*contract_test.go` not edited.
- Another round is moving `internal/relevo/runner.go` into `internal/spawn` at the
  same time; it touches `send.go` too. If you hit a conflict with it when you
  rebase at the end, keep both changes.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report. Never add a lint exclusion, `//nolint` or allow-list entry.

## 1. What moves

`internal/relevo/capture.go` and `internal/relevo/drift.go` -> new package
`internal/capture` (files `capture.go`, `drift.go`), with their tests
`capture_test.go` and `drift_test.go`. **`scratch.go` does not move** (it has no
production callers; the owner will decide whether it goes).

Two helpers they use stay in relevo because relevo uses them too:
- `brief(error) string` (defined in capture.go today): move its definition to
  `internal/relevo/text.go`; capture gets its own unexported copy.
- `showCommand` (`internal/relevo/push.go:22`): capture gets its own unexported
  one-line copy.

## 2. Names in the new package (drop the stutter)

| relevo today | capture |
|---|---|
| `CaptureBaseline` | `Baseline` |
| `CaptureRoundDiff` | `RoundDiff` |
| `CaptureDrift` | `Drift` |
| `DiffResult`, `CommitResult`, `CommitFacts`, `DiffSummary`, `DiffLine`, `DiffLineFromNote`, `PathsLine`, `PathsLineFromNote`, `ReadDiff`, `DriftResult`, `DriftSummary`, `DriftLine`, `ReadDrift` | same name |

Unexported helpers (`formatFiles`, `commitClause`, `formatCommits`,
`pathsClauseRe`) move unexported. `pathsClauseRe` must keep matching the format
string at `internal/relevo/reconcile.go:415` (`"paths: report %d, diff %d"`):
add a test in capture that pins the regex against that exact format.

## 3. Dependencies instead of `Runtime`

The moved functions read only `rt.Git` and `rt.Store`. Define in capture:

```
type Git interface { SnapshotTree; HeadCommit; DiffTrees; RevListCount; Dirty }  // the methods these files call, signatures copied from relevo.Git
type Deps struct { Git Git; Store *store.Store }
```

Functions that took `rt Runtime` take `d Deps` (or `s *store.Store` when they
use only the store: `ReadDiff`, `ReadDrift`). In relevo add one unexported
helper `func captureDeps(rt Runtime) capture.Deps` and use it at every call site
(`reconcile.go:406,407`, `repair.go:120`, `send.go:357,533`). A nil `rt.Git`
must become a nil `capture.Git` interface (not a non-nil interface holding a
nil) -- follow `IngestDeps` in `runtime.go:300`. Callers outside relevo:
`cmd/relevo/show.go:330,332` and `internal/ui/fetch.go:506` call
`capture.ReadDiff(rt.Store, ...)` / `capture.ReadDrift(rt.Store, ...)`.
`DiffLineFromNote` and `PathsLineFromNote` are called from `remote.go:1372,1375`.

## 4. Tests

`capture_test.go` and `drift_test.go` move. They used relevo's `fakeGit`
(`fake_test.go:106`) and `dbtest.Main`: give `internal/capture` its own
`helpers_test.go` with a small fake implementing only `capture.Git` (the fields
and counters these tests read), and a `main_test.go` calling `dbtest.Main` the way
`internal/relevo/main_test.go` does. Tests in relevo that call the moved
functions (`reconcile_test.go:903,948`, `remote_test.go:4343,4361`,
`seal_test.go:119,121`) are requalified, not deleted.

## 5. The new package is born clean

No lint exclusion or allow-list entry for `internal/capture`. Its comments
follow CLAUDE.md "### Code style" (1-3 line package comment; *why* only; no issue
numbers, `§` or history; no restating names); functions at most 70 lines. That
comment rewrite is the one non-mechanical edit allowed.

## 6. Coverage baseline

Regenerate as the spec requires for a move: `make check`, then
`sh scripts/check-coverage.sh --write`, then restore every line for a package
this round did not touch (only relevo, capture, ui, cmd/relevo may change).
Report each changed line (old -> new).

## 7. Steps

1. Create `internal/capture`; move §1 with the §2 names and §3 signatures.
   `go build ./internal/capture/...`.
2. Update relevo, cmd/relevo, ui call sites; `brief` to text.go.
   `go build ./... && go vet ./...`.
3. Tests (§4). `go test ./internal/capture/... ./internal/relevo/... ./internal/ui/... ./cmd/relevo/... -count=1`.
4. §5; `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. §6; `make check` (foreground) passes.
6. `git fetch origin && git rebase origin/main` (the spawn round may have
   landed); resolve, rebuild, re-run step 3's tests.
7. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-capture.md`; commit once.

Report: final API of `internal/capture`, call sites changed, tests moved and
added, coverage lines changed.
