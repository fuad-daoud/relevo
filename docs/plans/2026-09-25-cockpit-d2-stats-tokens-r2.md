# Cockpit D2 stats, tokens tab round 2: stacked charts in place of strips

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `60d8461`. Build on it, and amend it at the end
(`git commit --amend --no-edit`). Do not rebase or push. Line numbers are exact in the worktree now. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- the stats goldens
- `docs/plans/`

## 1. The user's review

- **The `total` split is right.** It is the overview's chart: a token y axis (`1B ┤`, `750M ┤`, …), dotted gridlines, and
  dates.
- **The candidate, provider and kind strips are wrong by design.** Their left column holds names where the chart needs its
  **token axis**, so a strip's height carries no number.
- **The user chose stacked charts:** one full chart per series, the same renderer as `total` but shorter, stacked under
  each other, with the page scrolling (pgdn/space, as today).

## 2. Changes (all in `internal/ui/view_stats.go`)

### 2.1 `statsTimelineLines` takes options, and stays byte-identical for its current callers

Add `type statsChartOpts struct`:

| field | type | meaning |
|---|---|---|
| `Rows` | `int` | The plot rows. This is today's third argument. |
| `FixedMax` | `float64` | When > 0, the y scale is computed from this value instead of the data's column max. Used for a scale shared across charts. |
| `AxisW` | `int` | When > 0, the minimum y-label width, so stacked charts whose labels differ in width still start their plots on the same column. |

Add `func statsTimelineVals(vals []float64, days []stats.DayCost, width int, o statsChartOpts) []string`. It is today's
`statsTimelineLines` body (lines 976-1100) with these changes:
- the values come from `vals`, one per day, aligned with `days`; `days` is used for the date labels only;
- `colMax` is replaced by `o.FixedMax` when that is > 0;
- `axisW` is at least `o.AxisW`.

`statsTimelineLines(days, width, rows)` becomes a one-line wrapper:
`statsTimelineVals(tokensOf(days), days, width, statsChartOpts{Rows: rows})`.

**Every existing timeline test and the overview, candidates and tokens goldens that draw it must pass unchanged.** That is
the proof of no change. If one moves, halt.

### 2.2 `func statsStackedCharts(series []statsSeries, days []stats.DayCost, shared bool, width int, windowTotal int64) []string`

This replaces `statsStripLines` (line 1982).

- **The charts:** only the non-idle series get a chart, in the order `statsSeriesFor` already gives. The idle series are
  collected for the last line.
- **The shared scale** (when `shared`): `FixedMax` is the largest **column** value over every non-idle series, using the
  same `statsGeom` bucketing the renderer will use.
  - Compute it with the same `avail` the renderer computes: `width - axisW - 8`.
  - Take the `axisW` from a first pass, or accept a one-cell difference. Either way, the y ticks are identical on every
    chart.
  - For `kind` (`shared == false`), `FixedMax` is 0 and every chart scales itself.
- **Alignment:** `AxisW` is the widest y label any chart in the stack will carry. Compute it with `statsNiceStep` over each
  series' max, or the shared max, formatted with `stats.ShortTokens`. Then every plot starts at the same column.
- **Each chart:**
  1. A **title line**:
     - `"   "` + the name in `textStyle.Bold(true)`;
     - then, right-aligned so it ends at `width - 3`: `ShortTokens(Total)` bold, plus muted ` · <pct>%` (the share of
       `windowTotal`, `%.0f%%`; the clause is left out when `windowTotal` is 0).
  2. `statsTimelineVals(series.Vals, days, width, statsChartOpts{Rows: 8, FixedMax: …, AxisW: …})`.
  3. **One blank line**, but not after the last chart.
- **After the last chart**, if any series is idle: one blank line, then
  `"   " + faintStyle.Render("no tokens in this window: " + names joined by " · ")`.
  - The line is cut to `width - 6` with `statsCut`.
- **The idle series** are the configured candidates, or providers, with no tokens. They come from the existing
  `statsSeriesFor`. `kind` never has idle series.

`tokensTabLines` (line 2121) calls `statsStackedCharts` where it called `statsStripLines`.

### 2.3 Deletions (closed list)

1. `statsStripLines` and any helper only it used. Check each with `grep -n` before deleting.

Everything else survives: `statsSeriesFor`, `statsSeries`, `statsGeom`, the chips, the week line and the keys.

## 3. Tests (`internal/ui/view_stats_test.go`)

1. **Port** `TestStatsTokensCandidateStrips` (line 1896) to the stacked charts. Keep its setup: two series with tokens and
   one idle, at 132×60.
   - **Assert:**
     - two title lines, in the order large then small, each ending with its `ShortTokens` total and ` · N%` at column
       `width - 3`;
     - each chart has its own `┤` lines and its own `└` axis line: exactly 2 `└` lines;
     - **the shared scale:** the set of y labels on the `┤` rows is identical for both charts;
     - **the alignment:** the `┤` column is the same in both charts;
     - the idle series appears only in the final `no tokens in this window: …` line, which is faint;
     - every trimmed line is at most 129 cells.
2. **Port** `TestStatsTokensKindOwnScale` (line 1963):
   - four title lines in the order `cache read`, `fresh input`, `output` and `cache write`;
   - their charts' top y labels differ, which shows each has its own scale;
   - their `┤` column is still identical, which shows they are aligned;
   - there is no `no tokens` line.
3. **New:** `TestStatsTimelineValsMatchesLines`. For the 30-day fixture of `TestStatsTimelineTicks`,
   `statsTimelineVals(tokensOf(days), days, 132, statsChartOpts{Rows: 8})` equals `statsTimelineLines(days, 132, 8)`, line
   for line.
4. **New:** `TestStatsTimelineFixedMax`. With `FixedMax` set to 10× the data's max, the top y label is the one
   `statsNiceStep(10×max)` gives, and the bars are shorter than without it.
5. Leave `TestStatsTokensSplitCycle`, `TestStatsTokensChips`, `TestStatsTokensTotalIsTimeline`, `TestStatsWeekLine` and
   `TestStatsTokensEmpty` unchanged. They must pass.

**Goldens:** regenerate once. Only `stats-tokens-132` may change. If the overview or candidates goldens change, halt.

## 4. Steps

1. §2.1, then tests 3 and 4, with every existing test green.
2. §2.2 and §2.3, then the ported tests 1 and 2.
3. The goldens, then the full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
   - Everything green, `gofmt -l` empty.
4. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-tokens-r2.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 5. Report

Include:
- the tests ported and added;
- the stripped `stats-tokens-132.golden`;
- `git diff --stat 60d8461 HEAD`.
