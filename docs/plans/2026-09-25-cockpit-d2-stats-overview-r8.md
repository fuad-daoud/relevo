# Cockpit D2 stats, overview round 8: the chart's height is fixed per screen, not per window

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `abf7f5a`. Build on it, and amend it at the end
(`git commit --amend --no-edit`). Do not rebase or push. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- the stats goldens
- `docs/plans/`

## 1. The bug

The user cycled the window (`w`) on a tall screen. The chart's height changed with each window, which moved the tables up
and down:
- 7d and 30d: 16 plot rows;
- 90d: 12;
- all: 16.

**Two data-dependent inputs set the height today:**
- `per = budget / m` clamped to 2..4, so `H = per*m` depends on `m`, the number of y intervals, which depends on the data's
  maximum;
- `budget = height - … - tableNeed`, which depends on how many candidate and repo rows the window has.

**The user's rule:** the layout above the tables must not move when the window changes. The chart's height depends on the
screen only.

## 2. Changes (all in `internal/ui/view_stats.go`)

### 2.1 `statsTimelineLines(days []stats.DayCost, width, rows int) []string`

The third parameter is renamed and changes meaning: it becomes **the plot's row count**.

- **The plot height:** `H = max(rows, m)`. Every y interval gets at least one row, so a tiny `rows` still draws.
- **The y ticks:**
  - tick `j` (`j = 1..m`) sits on row `H - round(j*H/m)`, with row 0 at the top;
  - so tick `m` is always row 0, and the ticks are as even as the division allows;
  - `tickRow` becomes a lookup over those rows.
- **Unchanged:** the bar level formula (`round(8*H*c/axisMax)`), the grid, the x axis, the labels and the width logic.

### 2.2 `overviewLines`

- **Delete the budget computation:** `tileLines`, `nCand`, `nRepo`, `tableNeed` and `budget`.
- **The chart rows:** `rows = clamp(height - 20, 8, 16)`, where `height` is the overview's height parameter.
  - The 20 is the other lines of the tiles-and-chart block (tiles 3, or 6 below 96 columns, two blanks, heading, axis,
    labels, blank) plus a floor of about 8 lines for the tables.
  - This depends on the screen size only.
- **Pass** `rows` to `statsTokenChartLines`, and then to `statsTimelineLines`.
- **The table viewport** is still computed from the lines actually above the tables (`fixed`), as today.

## 3. Tests (`internal/ui/view_stats_test.go`)

1. **New:** `TestStatsOverviewChartHeightIsFixedAcrossWindows`.
   - At 132×50, render the overview for four reports with different maxima and row counts. Use the fixture as is, the
     fixture with every day's tokens ×10, the fixture with every day's tokens ÷10, and the 20-candidate fixture from
     `TestStatsOverviewScrolls`.
   - In each, count the lines strictly between the `TOKENS` heading line and the axis line (`└`).
   - **Assert:** all four counts are equal, and they equal `clamp(avail - 20, 8, 16)`. Compute `avail` as `Body` does.
2. **Port** `TestStatsTimelineGrows` to the new meaning of the third argument:
   - `rows 16`, m=4: 16 plot rows, with ticks on 0, 4, 8 and 12;
   - `rows 12`, m=4: 12 rows, with ticks on 0, 3, 6 and 9;
   - `rows 2`, m=4: 4 rows (`H = max(rows, m)`), with ticks on 0, 1, 2 and 3.
3. **Port** `TestStatsOverviewChartUsesSpareRows`:
   - The chart still has more plot rows at 132×60 than at 132×22, or than at the smallest height that gives 8.
   - Assert the exact counts the formula gives, and name them in the report.
4. **Leave** `TestStatsTimelineTicks`, `TestStatsTimelineDailyTicksOnAWeek` and `TestStatsTimelineConstantWidth` unchanged.
   They call the renderer with `0`, which becomes `H = m`. If they assert 8 plot rows for m=4 at `0`, change only that
   argument to `8`, and say so in the report.

**Goldens:** regenerate once. `stats-overview-132` (34 rows) will change its chart height. `stats-candidates-132` must not
change; if it does, halt.

## 4. Steps

1. §2.1 and §2.2, with the tests in §3.
2. Goldens, then the full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
   - Everything green, `gofmt -l` empty.
3. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview-r8.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 5. Report

Include:
- the tests added and ported, with the exact counts;
- the stripped `stats-overview-132.golden`;
- `git diff --stat abf7f5a HEAD`.
