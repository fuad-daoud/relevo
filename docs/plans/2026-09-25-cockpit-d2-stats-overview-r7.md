# Cockpit D2 stats, overview round 7: no table titles, no chart subtitle, a chart of constant width

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `3f69ebe`. Build on it. Amend it at the end
(`git commit --amend --no-edit`). Do not rebase or push. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- the stats goldens
- `docs/plans/`

## 1. The user's review of the overview tab

1. **Remove the table titles.** The `CANDIDATES` and `BUSIEST REPOS` section lines above the two overview tables go. Each
   table starts with its column header row.
2. **Remove the chart subtitle.** `per day, 7 days` (or `30 days`, `90 days`, `all time`) after `TOKENS` goes. The heading
   is just `TOKENS`.
3. **Keep the chart's width constant; only the bars adapt.**
   - At 30d the plot fills its room.
   - At 90d the plot is about 90 cells wide and leaves the right part of the row empty. Each day is one column
     (`span = avail / 90 = 1`) and `plotW = span * len(cols)` is narrower than `avail`.
   - At `all`, the data starts on 09-05, so there are 21 days: `span = 5` and `plotW = 105` is again short of `avail`.
   - The user likes the chart as it is otherwise, with its grid and ticks. Keep all of that.

## 2. Changes (all in `internal/ui/view_stats.go`)

### 2.1 The tables lose their section line

In `statsOverviewCandidates` and `statsOverviewRepos`, the first returned line (the section title) is dropped. The header
row is now the first line.

**The scroll count moves into the header.** When a table overflows (`n > rows`), its name-column label becomes
`CANDIDATE   a–b of n` (or `REPO   a–b of n`). It is clipped to the name column with `stats.FitKey`, as the label is
today.

Everything that counted the section line drops it:
- **The selected-line arithmetic** in `overviewLines`: `sel = fixed + 1 + off` in place of `fixed + 2 + off`. In the stacked
  layout, the repos block's start is shifted by one as well.
- **The table viewport:** `rows = height - fixed - 1` (side by side), and the stacked equivalent.
- **The chart budget's `tableNeed`:**
  - side by side: `1 + max(nCand, nRepo)`;
  - stacked: `1 + nCand + 1 + 1 + nRepo`.

### 2.2 The chart heading

`statsTokenChartLines` gives a heading of `"   " + faintStyle.Bold(true).Render("TOKENS")`, with no subtitle. If
`statsWindowLabel` is then unused, delete it.

### 2.3 `statsTimelineLines`: the plot always fills `avail`

Keep the signature `(days, width, budget)`, and keep the y axis, the ticks, the grid, the runs builder and the labels as
they are. Change only the column geometry:

- **`avail`:** as today, `width - axisW - 8`, minimum 1. **`plotW = avail` always.**
- **Bucketing:**
  - `maxCols = max(1, avail/2)`, so every column gets at least two cells: a bar and a gap.
  - With `len(days) <= maxCols`, `cols` is the days and `chunk = 1`.
  - Otherwise `cols = statsBuckets(toks, maxCols)` and `chunk = ceil(len/maxCols)`.
  - The y-axis `colMax` and `step` are computed from `cols`, as today.
- **Each column's cells:**
  - `pitch = float64(avail) / float64(len(cols))`.
  - Column `i` starts at `start(i) = floor(i*pitch)` and ends at `start(i+1)`, where `start(len) = avail`.
  - `cw = floor(pitch)`, the same for every column.
  - `gap = 0` when `cw == 1`, else `max(1, cw/4)`. `barW = cw - gap`.
  - Column `i`'s bar covers `[start(i), start(i)+barW)`. The rest of its cells, up to `start(i+1)`, are gap cells, so the
    bars are all the same width and only the gaps differ, by at most one cell.
  - `center(i) = start(i) + (barW-1)/2`.
- **Finding the column of a cell:** a lookup table of length `avail`, filled from the starts, maps each cell `x` to its
  column `i`.
- **Ticks:**
  - `stepCols` is the smallest `s` in `[1, 7, 14, 28, 56, 91, 182, 364]` with `s * pitch >= 10` (compare as a float).
  - The tick columns are unchanged: `(len(cols)-1-i) % stepCols == 0`.
  - The labels come from `statsTimelineLabels`, which keeps its rules; pass it the new centres.
- **The axis row** is `avail` cells of `─`, with `┴` at the tick centres, as today.

## 3. Tests (`internal/ui/view_stats_test.go`)

1. `TestStatsTimelineConstantWidth`:
   - For day counts 7, 21, 30, 90, 200 and 400 (tokens on a few days, the rest zero), at width 132:
     - the stripped axis row (`└`) of `statsTimelineLines(days, 132, 0)` has a trimmed length of exactly `132 - 3`;
     - every plot row's trimmed length is at most that.
   - At 90 and 400 days, on the bottom plot row, every two bar cells that belong to different columns have at least one
     non-bar cell between them. So bars never touch.
2. **The existing timeline tests** (`TestStatsTimelineTicks`, `TestStatsTimelineDailyTicksOnAWeek`, `TestStatsTimelineGrows`)
   must pass unchanged.
   - At 30 days and 132 columns, the pitch is about 4.07, so the ticks stay weekly with five labels.
   - If one fails, halt and report its output. Do not edit it.
3. `TestStatsOverviewNoTableTitles`:
   - At 132×34 on the fixture, the stripped body contains no line equal to `CANDIDATES` or `BUSIEST REPOS` after trimming,
     no `per day`, and a header line containing both `IN/RND` and `SHARE`.
   - With a 20-candidate fixture (reuse the one from `TestStatsOverviewScrolls`), the header contains `of 20`.
4. **Port** every existing test that looked for the section titles, the subtitle, or the old `sel`/`rows` arithmetic.
   - These include `TestStatsOverviewScrolls`, which looks for `1–N of 20` on the section line: the count is on the header
     line now.
   - List each one you port in the report. Delete none.

**Goldens:** regenerate once. The overview goldens change: no title lines, and a wider chart. `stats-candidates-132` must not
change; if it does, halt.

## 4. Steps

1. §2.1 and §2.2, with tests 3 and 4.
2. §2.3, with tests 1 and 2.
3. Goldens, then the full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./internal/relevo/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
   - Everything must be green, and `gofmt -l` must print nothing.
4. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview-r7.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 5. Report

Include:
- the tests added and ported;
- the stripped `stats-overview-132.golden` in full;
- `git diff --stat 3f69ebe HEAD`.
