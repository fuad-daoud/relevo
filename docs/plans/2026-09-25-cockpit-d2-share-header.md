# Cockpit D2 round 5: right-align the `% TOKENS` header over its percentages

Date: 2026-09-25. Worktree: `ck-d2-rounds`. The branch is one commit on `9471682` (PR #504). Build on it and
AMEND it at the end (`git commit --amend --no-edit`). Then `git push --force-with-lease`: this branch is PR
#504's, and the push is part of this round.

**Stop rather than improvise.** If the code differs from what this plan quotes, halt and report.

**Scope fence.** Change only:
- `internal/ui/dash/render.go`, `internal/ui/dash/d2_test.go`, `internal/ui/dash/testdata/*.golden`
- `internal/ui/view_stats.go` (only the two `% TOKENS` header lines named below), `internal/ui/view_stats_test.go`
- `internal/ui/testdata/stats-*.golden`, `internal/ui/testdata/rounds.golden`
- `docs/plans/`

## 1. The bug (the user's screenshot, `:rounds` by binding)

```
   … COMMITS   TOKENS  % TOKENS              LAST
   …      17    81.2M               4%      today
```

- A `% TOKENS` cell is a 10-cell bar, then the percentage right-aligned at the END of the cell.
- The header is LEFT-aligned at the cell's start.
- When the bar is short or empty (most rows), the number sits 10 or more cells right of its header, and the
  column reads as misaligned.
- Every other numeric column right-aligns its header over right-aligned numbers.
- The `:stats` tables use the same cell, so they have the same bug.

## 2. The fix

Right-align the `% TOKENS` header over the full width of its cell, so its last character sits over the
percentage's `%`.

1. `internal/ui/dash/render.go:609`: the group header's `add("% TOKENS", 15, false)` becomes right-aligned
   (`true`) over the same 15 cells.
2. `internal/ui/view_stats.go:1439` and `:2497`:
   - Today both are `fmt.Sprintf("%-15s", "% TOKENS")`.
   - Right-align to the exact width the body's `% TOKENS` cell occupies in that table. `statsSharePlain`
     (line ~1480) says 17 cells.
   - Read `statsShareCell` and each table's row code, and use the width the rows actually render, so that the
     header's final `S` sits over the `%` of the percentages.
   - If the two tables use different widths, each header matches its own table.

## 3. Tests

- `internal/ui/dash/d2_test.go`, `TestGroupShareHeaderAligned`, on the `by:binding` View at 132 and at 200
  columns:
  - strip ANSI;
  - find the header line's column of the final `S` of `% TOKENS`;
  - on every group line, the `%` of the percentage is in that same column.
- `internal/ui/view_stats_test.go`, `TestStatsShareHeaderAligned`: the same assertion on the repos tab golden
  model at 132, and on the overview's repos table.
- Regenerate the goldens (`-run TestGoldenViews -update` in `./internal/ui/` and `./internal/ui/dash/`). Only
  the `% TOKENS` header text may move. Read each changed golden to confirm it.

## 4. Mutation check

Report its result, and check the mutated build compiles:

1. Put the dash header back to left-aligned. `TestGroupShareHeaderAligned` must fail.

Restore it afterwards.

## 5. Working efficiently

- Read `render.go` 595-620, and `view_stats.go` 1430-1500 and 2485-2500, in one step.
- Make each file's change in one edit.
- Focused: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/ui/dash/ -count=1`.
- Full check, once:
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/... ./internal/stats/ ./cmd/relevo/ -count=1 && go vet ./internal/ui/... && test -z "$(gofmt -l $(git ls-files '*.go'))" && sh scripts/check-name.sh`
- Do NOT run `make check`.

## 6. Steps

1. §2.
2. §3.
3. §4.
4. Full check.
5. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-share-header.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit && git push --force-with-lease`.

## 7. Report

Include:
- the tests;
- the mutation result, with the build line;
- the changed goldens, each with its new header line;
- `git diff --stat 9471682 HEAD`.
