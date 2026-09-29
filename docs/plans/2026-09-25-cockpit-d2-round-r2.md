# Cockpit D2 round detail, round 2: port the two viewport-width tests

Date: 2026-09-25. Worktree: ck-d2-round, which holds round 1's **uncommitted** work (card + tabs,
markdown, the shared card). Build on it; do not reset or commit. One small round.

**Stop rather than improvise.** If anything differs from what is quoted, halt and report.

## Why

Round 1 halted correctly. Two tests outside its ported list assert that a resize sets the
viewport to the full terminal width (`vp.Width == 100`). Round 1's plan §2.5 deliberately changed that
width to `contentWidth()` (width - 6, floor 20), so the content sits inside a 5-space indent. The
tests' intent ("a resize re-flows the viewport to the new geometry") survives. Only the expected
number changes. The planner missed them in the ported list; this is that port.

## Changes (the only two edits)

1. `internal/ui/detail_test.go`, `TestResizeReflowsViewportWithoutLosingActiveTab`, the check
   `if got.pane.detail.vp.Width != 100 {` and its message: compare against `got.pane.contentWidth()`,
   and say `expected vp.Width %d (contentWidth), got %d`.
2. `internal/ui/model_test.go`, `TestWindowSizeMsgSetsReady`, the check
   `if got.pane.detail.vp.Width != 100 {` and its message: the same change.

Do not change `got.pane.width != 100` in model_test.go: the pane width is still the full width.

## Then finish round 1's remaining step

- `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1` must be green.
- `git diff --stat internal/ui/testdata/`: only round goldens (round, round-archived, and the three new
  round-*) may differ from the base commit. No fleet or stats golden may change.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## Report

List:
- the two ports;
- the check results;
- `round-real-132.golden` verbatim.
