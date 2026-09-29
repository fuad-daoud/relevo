# Cockpit D2 stats, tab 1 round 3: side padding, a timeline chart on a grid, scrollable tables, a share column with room

Date: 2026-09-25. Worktree: ck-d2-stats. Its branch is one commit, `1de6249b feat(cockpit): stats tokens in the engine, a tab
bar, and the overview tab`, rebased onto origin/main 7db0ec7d. Build on it. At the end, commit by **amending** that commit
(`git commit --amend --no-edit`) so the branch stays one commit. Do not rebase or push. Line numbers below are exact in the
worktree now. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, or a step cannot be done as written, halt and
report. A halt that surfaces a design error is worth more than a green suite that bent a test to fit. Change only
`internal/ui/view_stats.go`, `internal/ui/view_stats_test.go`, `internal/ui/golden_test.go`, `internal/ui/styles.go` (one
token), the stats goldens under `internal/ui/testdata/`, and `docs/plans/` (step 8).

## 1. System overview

`:stats` has five tabs, and this round touches only the **overview** tab (`statsView.overviewLines`,
`internal/ui/view_stats.go:442-482`). The user reviewed round 2 and asked for five things:

1. **Side padding.** Nothing on the overview runs past column `width - 3`. That covers the tiles, the chart and the tables:
   leave three cells on the right, as on the left.
2. **A timeline with numbers.** The chart spans the whole window. It gets date ticks about weekly on the x axis and "nice"
   value ticks on the y axis (0 plus three or four more).
3. **A grid, in the style of relevo-site.** The site draws its diagram on a faint 1px blueprint grid that runs both ways and
   is painted only on the drawing's frame (`~/projects/relevo-site/style.css:261-270`, DESIGN.md "the grid belongs to the
   diagram"). The terminal version:
   - faint dotted gridlines at every y tick (`┈`) and every x tick (`┊`), inside the plot area only, behind the bars;
   - a bar cell always wins over a grid cell.
4. **Every candidate, scrollable.** The CANDIDATES table lists every scorecard row, with fewer-than-5-round rows dimmed.
   BUSIEST REPOS lists every repo. Each table is a fixed-height viewport with a cursor. `↑↓` move it, `←→` pick the table,
   and `enter` opens the row's rounds.
5. **The SHARE column has room.** Put two cells between TOKENS and the bar, left-align the bar, and right-align the percent.

The other four tabs (`candidates`, `tokens`, `reliability`, `repos`) and their shared helpers (`statsSteps`, `statsBuckets`,
`statsBarRow`, `statsDayCols`, `statsPlotW`, `spendLines`) stay **unchanged**. The new chart gets its own helpers.

## 2. Working efficiently

Every model step costs one round trip, whatever it does. So:

- Read `internal/ui/view_stats.go` once, in full, and `internal/ui/view_stats_test.go:1-135` and `:276-620`. Read
  `internal/ui/golden_test.go:455-560` in the same step. Every location you need is named here. Do not search for anything
  this plan already locates.
- Make all the changes to `view_stats.go` in as few edit calls as you can: the overview section, lines 428-736, can be one
  replacement.
- Iterate with the focused command below. Fix every error it reports before you run it again.
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Stats|Golden' -count=1`
- Regenerate the goldens once, when the code is done:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`
- Run the full check once, at the end:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
  - `gofmt -l` must print nothing. (`make check` is hook-blocked on this machine; the planner runs it.)
- No test you add may spawn a harness or reach the network. These are pure view tests, like the existing ones.

## 3. Data structures

### 3.1 `statsView` gains overview-only table state (`view_stats.go:48-60`)

Add two fields after `top`:

| field | type | purpose |
|---|---|---|
| `ovFocus` | `int` | Which overview table has the cursor: `0` CANDIDATES, `1` BUSIEST REPOS. Default 0. |
| `ovCursor` | `[2]int` | The selected row per overview table, an index into that table's row order (§3.3). It is clamped to `[0, rows-1]` and 0 when a table is empty. |

These are separate from `focus`/`cursor`, which belong to the candidates and repos tabs. The overview's repo order (tokens
desc) is not `rep.Repos`' order, so sharing one cursor would point at different rows on different tabs.

### 3.2 A new style token (`internal/ui/styles.go`, the var block at lines 8-17)

- `gridStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#2f3542"))`
- It is the chart's gridline tone: between `shadeStyle` (#2a2f39) and `borderStyle` (#3a4150).
- Its comment says it is only for the drawing surface.

### 3.3 Row orders

- **Candidate rows:** `v.rep.Scorecard` in report order, every row (`Few` included).
- **Repo rows:** a copy of `v.rep.Repos`, stable-sorted by `Tokens` desc. The helper `v.overviewRepoRows() []stats.GroupRow`
  returns it. Both rendering and `enter` use it.

### 3.4 Chart geometry (all in cells, internal to the chart helpers)

| name | meaning |
|---|---|
| `axisW` | The widest y-tick label (`ShortTokens` of each tick value, and `"0"`). |
| `avail` | The width the plot may use: `width - 3 - axisW - 2 - 3`. That is the left gutter, the labels, `" ┤"`/`" │"`, and the right margin. Minimum 1. |
| `cols` | The per-column token sums: the days, bucketed with the existing `statsBuckets(toks, avail)` only when `len(days) > avail`. `chunk` is the days per column (1 unless bucketed). |
| `span` | `avail / len(cols)`, minimum 1: the cells one column takes. |
| `plotW` | `span * len(cols)`: the plot's real width. The axis line and the gridlines end there. |
| `gap` | `0` if `span == 1`, else `max(1, span/4)`. |
| `barW` | `span - gap`: column `i`'s bar fills cells `[i*span, i*span+barW)`. |
| `center(i)` | `i*span + (barW-1)/2`, the column's tick cell. |
| `step` | The y tick step (§4.2). |
| `m` | The number of tick intervals, `ceil(colMax/step)`, in `1..4`. |
| `axisMax` | `m*step`. |
| `H` | `2*m` plot rows. Two rows per interval, so every tick sits on a row. |
| `level(c)` | `round(8*H*c/axisMax)`, the bar height in eighths. |
| tick row `j` | For `j = 1..m`, row `H - 2j` (row 0 is the top). Row 0 carries `axisMax`. |
| `stepCols` | The x tick step in columns (§4.3). |

## 4. Contracts

All of these are unexported functions in `internal/ui/view_stats.go`.

### 4.1 `func statsNiceStep(max float64) float64`

- **Responsibility:** the smallest "nice" step at or above `max/4`.
- **Nice values:** `{1, 2, 2.5, 5} × 10^k` for `k ≥ 1`, and `1, 2, 5` for `k = 0`. The step is never below 1.
- **Precondition:** `max > 0`.
- **Postcondition:** `ceil(max/step)` is in `1..4`.
- **Examples** (these become the test table):

| max | step | m | axisMax |
|---|---|---|---|
| 6.8M | 2M | 4 | 8M |
| 9.1M | 2.5M | 4 | 10M |
| 5.5M | 2M | 3 | 6M |
| 1000 | 250 | 4 | 1000 |
| 3 | 1 | 3 | 3 |

### 4.2 `func statsTimelineLines(days []stats.DayCost, width int) []string`

- **Responsibility:** the chart body under the heading. That is `H` plot rows, the axis row, and the date-label row, each
  starting with the three-cell gutter. Nothing extends past `width - 3`.
- **Precondition:** at least one day has `Tokens > 0` (the caller handles the empty case).
- **Rows, top to bottom:**
  - **Plot row `r`:**
    - Label cell: the tick value right-aligned in `axisW` (`mutedStyle`) on a tick row, else blank.
    - Then `" ┤"` on a tick row, or `" │"` otherwise (`ruleStyle`).
    - Then `plotW` cells. For each cell `x`:
      - the bar rune when column `x/span`'s bar covers `x` and its rune for row `r` is not blank (`accentStyle`);
      - else `┈` on a tick row (`gridStyle`);
      - else `┊` when `x` is a tick column's `center` (`gridStyle`);
      - else a space.
    - The bar rune for row `r` is `statsBarRunes[clamp(level - (H-1-r)*8, 0, 8)]`.
  - **Axis row:** `"0"` right-aligned in `axisW` (`mutedStyle`), `" └"`, then `plotW` cells of `─`, with `┴` at each tick
    column's `center` (`ruleStyle`).
  - **Label row:** the tick dates (§4.3) in `mutedStyle`.
- **Styling:** render each maximal run of same-styled cells as one styled string, not one ANSI span per cell.

### 4.3 X ticks, inside `statsTimelineLines`

- **The step.** `stepCols` is the smallest `s` in `[1, 7, 14, 28, 56, 91, 182, 364]` with `s*span ≥ 10`, or 364 if none
  qualifies. When `chunk == 1` a column is a day, so a 30-day window at 132 columns (span 4) ticks weekly, a 7-day window
  (span 17) ticks daily, and a 90-day window (span 1) ticks every 14 days.
- **The tick columns.** Column `i` is a tick when `(len(cols)-1-i) % stepCols == 0`. The newest column always carries one.
- **The labels.** Each label is `stats.MonthDay` of the column's first day (`days[i*chunk].Day`), five cells centred on
  `3 + axisW + 2 + center(i)`.
  - Place them right to left.
  - Shift a label left so it ends at or before `width - 3`.
  - Skip a label whose right end would come less than two cells before the previously placed label's start, or whose start
    would be below column 0. A skipped label keeps its `┊`/`┴`.

### 4.4 `func (v statsView) statsTokenChartLines(width int) []string`

This replaces lines 535-585.

- The heading line is unchanged (line 539).
- With no tokens, the one faint line gains the three-cell gutter:
  `"   " + faintStyle.Render("no token usage recorded in this window")`.
- Otherwise it returns the heading plus `statsTimelineLines(v.rep.Spend.Days, width)`.
- **Delete** `statsOverviewPlotW` (587-595) and `statsXLabels` (597-611). Nothing else calls them.

### 4.5 Overview layout: `func (v statsView) overviewLines(env Env, width, height int) []string`

`height` is the height the overview may fill. The layout is the same as today, in order:
- tiles;
- two blank lines;
- the chart;
- one blank line;
- the tables.

**Tiles** (`statsTilesLines`, 486-505):
- `tileW = (width - 6) / 4`, or `(width - 6) / 2` below `statsOverviewWide`.
- Each tile starts at `3 + i*tileW`, as today.

**Tables:**
- `fixed` is the line count of everything above the tables.
- `rows = height - fixed - 2` is the rows each table shows below its section line and header, minimum 3.
- **Wide (`width >= statsOverviewWide`):**
  - `leftW = (width - 9) / 2`, `rightW = width - 9 - leftW`.
  - The left table starts at column 3, the right at `3 + leftW + 3`, and the right table ends exactly at `width - 3`.
  - `statsTableColumns` (763-780) takes `leftW, rightW` instead of one `cellW`.
- **Stacked (below 96):**
  - Each table is `width - 6` wide at column 3, and they are separated by one blank line.
  - Each gets `rows = max(3, (height - fixed - 5) / 2)`.
- If the result is taller than `height`, `Body`'s existing `top`/pgdn scrolling handles it. Do not change `Body`'s
  scrolling.

`Body` (363-392) and `tabLines` (410-426) pass the height through. `tabLines(env, width, height)` hands `height - len(head)`
to `overviewLines`. The other tabs ignore it.

### 4.6 `func statsTableWindow(n, cursor, rows int) (first, last int)`

- **Responsibility:** the visible slice `[first, last)` of `n` rows that keeps `cursor` visible, computed with no stored
  offset.
- `first` is 0 when `cursor < rows`, else `cursor - rows + 1`.
- `last` is `min(n, first + rows)`.

### 4.7 `func (v statsView) statsOverviewCandidates(env Env, cellW, rows int) []string`

This replaces lines 613-636.

- **Section line:** faint bold `CANDIDATES` + faint `   by rounds, under 5 dimmed`. When `len(Scorecard) > rows`, append
  faint `   <first+1>–<last> of <n>` (an en dash).
- **Header row:** as today (line 620-621).
- **Rows:** every row in `[first, last)` from `statsTableWindow(n, v.ovCursor[0], rows)`.
  - Each row is `statsOverviewCandidateRow(name, s, nameW, few=s.Few)`: when `few`, the name and the numbers are both
    `faintStyle` rather than `fgStyle`/`mutedStyle`.
  - The selected row (`v.ovFocus == 0 && i == v.ovCursor[0]`) is rebuilt as plain text and rendered once with
    `selBandStyle.Foreground(textStyle.GetForeground()).Bold(true)`, fitted to `cellW` (the `compose.go:129` idiom).
  - The unfocused table has no band.

### 4.8 `func (v statsView) statsOverviewRepos(cellW, rows int) []string`

This replaces lines 662-689.

- The section line is faint bold `BUSIEST REPOS` + faint `   by tokens`, plus the same `of n` suffix as §4.7.
- **Header:** `fmt.Sprintf("%-*s%6s%9s", nameW, "REPO", "RNDS", "TOKENS") + "  " + fmt.Sprintf("%-15s", "SHARE")` in faint
  bold. `SHARE` starts over the bar's first cell.
- **Rows:** `v.overviewRepoRows()`, windowed like the candidates on `v.ovCursor[1]`, with `maxTokens` taken over **all**
  rows, not just the visible ones. The selected row is banded as in §4.7 when `v.ovFocus == 1`.
- `statsRepoNameW(cellW)` becomes `statsMinWidth(cellW - 32)`: RNDS 6, TOKENS 9, SHARE 17. Update its comment (line 726-728).

### 4.9 `func statsShareCell(tokens, maxTokens, total int64) string`

This replaces lines 691-720.

- The cell is always 17 cells: two spaces, a 10-cell bar area, one space, and the percent right-aligned in four cells.
- **The bar area:** `n = round(10*tokens/maxTokens)` accent `▇` (at least 1 when `tokens > 0`), padded with spaces to 10.
- **The percent:** `fmt.Sprintf("%3.0f%%", pct)` → `" 54%"`, `"100%"`, `"  0%"`, in `fgStyle`.
- Example: `"  ▇▇▇▇▇▇▇▇▇▇  54%"` and `"  ▇▇▇▇▇▇      32%"`.

### 4.10 Keys (`updateKey` 204-256, `Keys` 115-140, `enter` 321-339)

On the **overview** tab only:
- `up`/`k`, `down`/`j` move `ovCursor[ovFocus]`, clamped to that table's row count.
- `left`/`h` sets `ovFocus = 0`; `right`/`l` sets `ovFocus = 1`.
- `enter` opens `:rounds` for the selected row, with the same queries as today:
  - `candidate:<token><since>`;
  - `repo:"<key>"<since>`, or the existing `(none)` notice.

Factor the two query builds out of `enter` into `func (v statsView) roundsForCandidate(env Env, token string)` and
`func (v statsView) roundsForRepo(env Env, key string)`, both returning `(View, tea.Cmd)`. Then `enter` (the tabs) and the
overview's enter share them.

The candidates and repos tabs' behaviour is unchanged. `Keys()` for overview returns
`{"↑↓","move"}, {"←→","table"}, {"enter","its rounds"}, {"tab","next tab"}, {"w","window"}`.

`w` (window change) resets `ovCursor` to `[0,0]`, as it already resets `top`.

## 5. Pseudocode: the chart

```
statsTimelineLines(days, width):
  toks = days' Tokens as float64
  axisW = 4                                   # a guess; corrected below
  repeat at most twice:
    avail = max(1, width - axisW - 8)
    cols, chunk = (toks, 1) if len(toks) <= avail else (statsBuckets(toks, avail), ceil(len/avail))
    colMax = max(cols); step = statsNiceStep(colMax); m = ceil(colMax/step); axisMax = m*step
    newW = max width of ShortTokens(j*step) for j in 1..m, and of "0"
    if newW == axisW: break
    axisW = newW
  span, plotW, gap, barW as in §3.4; H = 2*m
  tickCols = { i : (len(cols)-1-i) % stepCols == 0 }
  for r in 0..H-1:  emit plot row r (§4.2)
  emit axis row; emit label row (§4.3)
```

## 6. Error handling

This is pure rendering, with no new error paths. The degenerate inputs are defined:

- **No tokens:** the existing one-liner (§4.4).
- **`width` too small for any plot:** `avail = 1` and `span = 1`, and the rows are still produced.
- **Empty tables:** the section line and header only, and the cursor stays 0.

## 7. Tests (`internal/ui/view_stats_test.go`)

**Port these.** Change their assertions and keep their intent:

1. `TestStatsOverviewFillsWidth` (276-304):
   - tile columns become `3 + 3*((w-6)/4)`;
   - the SHARE check changes: the **first repo row under the SHARE header**, trimmed, must end exactly at `w-3` (its
     percent is right-aligned in the table's last cells).
2. `TestStatsOverviewColumnsAligned` (309-357):
   - iterate **every** `rep.Scorecard` row, not just the non-`Few` ones, since all are listed now;
   - the repo rows keep their TOKENS alignment check.
3. `TestStatsOverviewNarrowStacks` (361-399): `tileW = (w-6)/2`.
4. `TestStatsTabsKeys` (573-620), the "↑↓ do nothing on overview" block (598-606):
   - `j` on overview now moves `ovCursor[0]` to 1;
   - `cursor[0]` and `cursor[1]` stay 0;
   - rename the comment.

**Add these:**

5. `TestStatsNiceStep`: the §4.1 table.
6. `TestStatsTimelineTicks`:
   - A 30-day `[]stats.DayCost` from 2026-08-19 to 2026-09-17, tokens on a few days, max 6.8M. Render
     `statsTimelineLines(days, 132)` and strip the ANSI.
   - **Assert:**
     - the plot has 8 rows;
     - rows 0, 2, 4 and 6 contain `┤` and the labels `8M`, `6M`, `4M` and `2M`;
     - an empty cell of row 2 is `┈`;
     - the axis row has five `┴`;
     - the label row holds `09-17` as its last label and has exactly five labels, seven days apart (`08-20 08-27 09-03 09-10 09-17`);
     - no line's trimmed width exceeds 129.
7. `TestStatsTimelineDailyTicksOnAWeek`: the same with seven days, where every day is labelled.
8. `TestStatsOverviewSidePadding`:
   - At widths 100, 132 and 200 (height 34), no line of the stripped overview body has a trimmed length above `w-3`.
   - At 132, the right table's last cell is at `w-4` (0-based), so the right edge is exact.
9. `TestStatsOverviewScrolls`:
   - A fixture with 20 `ScoreRow`s (names `cand-00`…`cand-19`, the last five `Few`) at 132×34.
   - The body shows `1–N of 20` for some N < 20 and does not show `cand-19`.
   - After 19 `j`, it shows `cand-19` and `–20 of 20`, and not `cand-00`.
10. `TestStatsOverviewEnter`:
    - Using `statsShell` as `TestStatsTabsKeys` does, `l` then `enter` on the overview pushes a rounds view whose query is
      the top-tokens repo's `repo:"…" since:30d`.
    - A fresh shell with `j`, `enter` gives the second candidate's `candidate:… since:30d`.
11. `TestStatsShareCell`:
    - `statsShareCell(9_100_000, 9_100_000, 16_900_000)` stripped is `"  ▇▇▇▇▇▇▇▇▇▇  54%"`, 17 cells.
    - A 0-token row is `"  " + 10 spaces + "   0%"`, 17 cells.

**Leave unchanged:** `TestStatsDayBarsWidenWhenDaysAreFew` and every non-overview test.

**Goldens** (`internal/ui/golden_test.go`):
- `statsOverviewReport` (523-531): `Since: railNow.AddDate(0, 0, -29)`, so the golden's window matches its `30 days` heading.
- `overviewRows` (458-521): add two few-round candidates, both on `relevo`, with `gl` tokens and outcome reported, so the
  golden shows dimmed rows:
  - `kimi-k3` (2 rounds, days 12 and 16);
  - `qwen-4-coder` (1 round, day 14).
- Regenerate all goldens once (§2), and read `stats-overview-132.golden` to check the layout reads as §4 says.

## 8. Ordered implementation steps

1. **Style token.** Add `gridStyle` (§3.2).
   - **Depends on:** none.
   - **Done when:** it compiles.
2. **Chart helpers.** `statsNiceStep`, `statsTimelineLines`, the rewritten `statsTokenChartLines`, and the deleted
   `statsOverviewPlotW`/`statsXLabels` (§4.1-4.4).
   - **Depends on:** 1.
   - **Done when:** tests 5-7 pass.
3. **Padding and layout.** `overviewLines(env, width, height)`, the `tabLines`/`Body` height pass-through, `statsTilesLines`,
   and `statsTableColumns(left, right, leftW, rightW, width)` (§4.5).
   - **Depends on:** 2.
   - **Done when:** ported tests 1 and 3, and test 8, pass.
4. **Tables.** `statsTableWindow`, `overviewRepoRows`, the new `statsOverviewCandidates` and `statsOverviewRepos`,
   `statsOverviewCandidateRow(..., few bool)`, `statsShareCell`, and `statsRepoNameW` (§4.6-4.9).
   - **Depends on:** 3.
   - **Done when:** ported test 2, and tests 9 and 11, pass.
5. **Keys.** The `ovFocus`/`ovCursor` fields, overview `↑↓←→ enter`, `roundsForCandidate`/`roundsForRepo`, `Keys()` and the
   `w` reset (§3.1, §4.10).
   - **Depends on:** 4.
   - **Done when:** ported test 4 and test 10 pass.
6. **Goldens.** The fixture changes and the regeneration (§7).
   - **Depends on:** 5.
   - **Done when:** `TestGoldenViews` passes without `-update`.
7. **Full check** (§2).
   - **Depends on:** 6.
   - **Done when:** all green, and `gofmt -l` is empty.
8. **Plans.** Copy these into the worktree's `docs/plans/`, so the plans ship with the code:
   - `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview.md`
   - `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview-r2.md`
   - this file, `-r3.md`

   Then `git add -A && git commit --amend --no-edit`.
   - **Depends on:** 7.
   - **Done when:** `git log --oneline origin/main..HEAD` shows one commit.

## 9. Report

Include:
- the test names added and ported;
- the stripped `stats-overview-132.golden` pasted in full;
- `git diff --stat origin/main HEAD`;
- anything you had to halt on.
