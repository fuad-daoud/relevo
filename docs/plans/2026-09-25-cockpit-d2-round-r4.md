# Cockpit D2 round detail, round 4: no unread tint on done bindings

Date: 2026-09-25. Worktree: ck-d2-round, which holds rounds 1-3 **uncommitted**. Build on it; do not reset or commit.
One tiny round.

**Stop rather than improvise.** If the code differs from what is quoted, halt and report.

## Change

`internal/ui/view_fleet.go:614`: the NOW-cell style case
`case b.Unread && (g == groupIdle || g == groupDone):` becomes
`case b.Unread && g == groupIdle:`. Done bindings' reports went to their planners long ago, so on a done
row the accent tint says nothing a human can act on. Idle rows keep the tint.

## Tests

- New `TestFleetUnreadTintOnlyOnIdle` (internal/ui/fleet_test.go):
  - Set `lipgloss.SetColorProfile(termenv.TrueColor)` for the test and restore it after; see how
    `TestDimLinesStripsStyle` does it on this branch, if it exists, or set the renderer profile directly.
  - An Unread idle row's line contains the accent colour's escape.
  - An Unread done row's line does not.
- Regenerate the goldens. They are ANSI-free, so none should change; any diff is a halt.

## Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## Report

List the files changed and the check results.
