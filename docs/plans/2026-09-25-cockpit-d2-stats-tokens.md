# Cockpit D2 stats, tab 3: the tokens tab

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `f65cfa5`, on origin/main 753aa4e. Build on it, and amend
it at the end (`git commit --amend --no-edit`). Do not rebase or push. Line numbers are exact in the worktree now. This is
one round.

**Stop rather than improvise.** If the code differs from what is quoted here, or a step cannot be done as written, halt and
report. Change only:
- `internal/stats/stats.go` and `internal/stats/stats_test.go` (§3.1 only);
- `internal/ui/view_stats.go`, `internal/ui/view_stats_test.go` and `internal/ui/golden_test.go`;
- the stats goldens;
- `docs/plans/`.

## 1. System overview

`:stats` has five tabs. The overview and candidates tabs are done and approved. This round rebuilds tab 3, **tokens**.
Today it draws the old C2 spend panel: `spendLines`/`providerLines` behind a titled rule, and a `p` "by provider" toggle.

The new tab answers two questions: how tokens accrue over time, and who or what uses them. It is one chip row plus the
charts for the selected **split**:

```
   total   [candidate]   provider   kind

               671.2M │    ┊          ┊          ┊          ┊        ██ ┊
                      │    ┊          ┊          ┊          ┊     ▇▇ ██ ┊
   deepseek-v4.1-flash│    ┊          ┊          ┊          ┊  ▄▄ ██ ██ ▄▄   1.33B  76%

                      │    ┊          ┊          ┊          ▂▂       ▅▅ ▆▆  369.0M  21%
   gemini-3.8-flash-hi│ …
   glm-5.3-flash      │    ┊          ┊          ┊          ┊           ┊       0   0%     (faint: no tokens)
                      └────┴──────────┴──────────┴──────────┴───────────┴──
                         08-28      09-04      09-11      09-18      09-25

   this week 1.63B   last week 124.7M   busiest day 09-24 814.6M
```

The user's decisions:
- **`s` cycles the split:** total → candidate → provider → kind → total. A new view starts on **candidate**. There is **no**
  `split` label: the row is only the four chips.
- **total** is the overview's chart (`statsTimelineLines`), taller.
- **candidate / provider / kind** draw one **strip** per series, stacked, sharing one x axis. They are drawn with the same
  quality as the overview chart, which the user likes:
  - the same bar geometry, bucketing and x ticks;
  - the same faint `┊` grid at the ticks;
  - the same date labels.
- **Scales:**
  - candidate and provider strips share **one** scale, so heights compare between models;
  - **kind** strips each use **their own** scale, because cache reads are about 100× the rest. Each kind strip shows its own
    max.
- **Candidates with no tokens in the window are not hidden.** Every configured candidate gets a strip, and the ones with no
  tokens are faint and empty. Provider strips likewise include the providers of configured candidates.
- **The captions and footnotes are gone.** No "rounds recorded no usage", no known cost and no dollars.
- **The chart height depends on the screen, never on the data or the window.** The user caught the overview jumping when
  `w` changed the window. The strips have a fixed height of 3 rows each.

## 2. Working efficiently

- Read these once, in one batched step:
  - `internal/ui/view_stats.go` in full;
  - `internal/ui/view_stats_test.go` in full;
  - `internal/ui/golden_test.go:455-640`;
  - `internal/stats/stats.go:120-150` and `:365-430`;
  - `internal/stats/stats_test.go` (skim it).
- Make the edits to `view_stats.go` in as few calls as you can.
- The focused command:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/stats/ ./internal/ui/ -run 'Spend|Stats|Golden' -count=1`
- Goldens: `-run TestGoldenViews -update`, once, at the end.
- The full check, once:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
- These are pure tests: no harness and no network.

## 3. Data structures

### 3.1 `stats.DayCost` gains three fields (`internal/stats/stats.go:140-145`), filled in `buildSpend` (`:372-428`)

| field | type | meaning |
|---|---|---|
| `Kinds` | `TokenCounts` | The day's tokens by kind: the `kinds[i]` that `buildSpend` already accumulates. Keep `Tokens = Kinds.Total()`. |
| `ByCandidate` | `map[string]int64` | The day's total tokens per `BuilderCandidate`, with `"(none)"` when nil. Only rows with a non-zero total add a key. |
| `TokensByProvider` | `map[string]int64` | The same per `BuilderProvider`, with `"(none)"` when nil. |

- Initialise both maps for every day, as `ByProvider` is initialised today.
- `render.go` and the CLI text output are **unchanged**.

### 3.2 `statsView` (`view_stats.go:48-`)

- **Add** `split int`: 0 total, 1 candidate, 2 provider, 3 kind. `newStatsView` sets it to **1**.
- **Remove** `byProvider` (line 61), per the deletion list in §5.

### 3.3 `statsSeries`, unexported: one strip

| field | type | meaning |
|---|---|---|
| `Name` | `string` | The label: a candidate's name, a provider, or a kind name. |
| `Vals` | `[]float64` | One value per day, aligned with `rep.Spend.Days`. |
| `Total` | `int64` | The sum over the window. |
| `Idle` | `bool` | A configured candidate or provider with no tokens: the strip is faint and empty. |

### 3.4 `statsTimeGeom`, unexported: the x geometry shared by the chart and the strips

It is extracted from `statsTimelineLines` (lines 884-1060), with no behaviour change:

| field | meaning |
|---|---|
| `Cols []int` | Column `i` covers days `[Cols[i], Cols[i+1])`: the bucketing. |
| `Chunk int` | The days per column. |
| `Start []int` | `Start[i]` is column `i`'s first cell. It has length `len(cols)+1`, and the last entry is `avail`. |
| `BarW int` | The bar width, the same for every column. |
| `Center []int` | The tick cell of each column. |
| `Tick []bool` | Whether column `i` carries an x tick. |
| `Owner []int` | The column that owns each cell, length `avail`. |

- **The builder:** `func statsGeom(nDays, avail int) statsTimeGeom`. It does exactly what `statsTimelineLines` computes
  today: `maxCols = max(1, avail/2)`, the pitch, `cw`, the gap, `barW`, `stepCols` and the tick rule.
- **The summing helper:** `func (g statsTimeGeom) colVals(vals []float64) []float64` sums `vals` into the columns, as
  `statsBuckets` does. Keep `statsBuckets` if `colVals` uses it.
- **The rewrite:** `statsTimelineLines` is rewritten to use `statsGeom` and `colVals`.
  - Its output must stay **byte-identical**: every existing timeline test and the overview goldens must pass **unchanged**.
  - If they do not, halt.

## 4. Contracts (all in `internal/ui/view_stats.go`)

### 4.1 `func statsSplitChips(split int) string`

- `"   "` plus the four chips `total`, `candidate`, `provider` and `kind`, with three spaces between them.
- The selected chip is `chip(kbdStyle.Bold(true), name)`: a subtle filled chip, **not** the accent used by the tab bar. The
  others are `mutedStyle.Render(chip(normalStyle, name))`.
- There is **no label**.

### 4.2 `func (v statsView) statsSeriesFor(env Env, split int) (series []statsSeries, shared bool)`

- **candidate (1):**
  - A series per `ByCandidate` key across `rep.Spend.Days`. Skip `"(none)"`: rounds with no candidate carry no tokens now.
  - `Name` is `env.Src.Base().Candidates.NameOf(token)`.
  - Then every token in `v.configured` that has no series gets an `Idle` series with zero values.
  - Order: non-idle by `Total` desc, then idle by name.
  - `shared = true`.
- **provider (2):**
  - The same from `TokensByProvider`, skipping `"(none)"`.
  - The idle providers are the providers of the configured candidates, from `candidate.ParseRef(token).Provider`, that have
    no series.
  - `shared = true`.
- **kind (3):** four series, always in this order, never idle:
  - `cache read` (`Kinds.Cache`);
  - `fresh input` (`Kinds.In`);
  - `output` (`Kinds.Out`);
  - `cache write` (`Kinds.Write`).

  `shared = false`.
- **total (0):** not used here, since total draws the timeline.

### 4.3 `func statsStripLines(series []statsSeries, days []stats.DayCost, shared bool, width int, windowTotal int64) []string`

**Layout**, with every line starting `"   "`:
- `NW = 22`, the label column;
- then `" │"` (`ruleStyle`);
- then the plot, `avail = width - 3 - NW - 2 - 12 - 3` cells, at least 1;
- then 12 cells of totals;
- then the right margin.

Every line's trimmed width is at most `width - 3`.

**The geometry:** `g := statsGeom(len(days), avail)`, shared by every strip, so their columns and ticks line up exactly.

**Each strip:** 3 rows, then one blank line between strips (not after the last).
- **Its scale:** `scaleMax` is the largest column value over all strips when `shared`, else over this strip only.
- **A bar cell:** in the column's bar cells, the level is `lvl = round(24 * colVal / scaleMax)`. The rune for row `r`
  (0 = top) is `statsBarRunes[clamp(lvl - (2-r)*8, 0, 8)]`, in `accentStyle`.
- **Every other plot cell:** `┊` (`gridStyle`) at a tick column's centre, else a space. There are no horizontal gridlines
  in strips.
- **The label column:**
  - row 0: the scale max, `stats.ShortTokens(scaleMax)`, right-aligned in `NW - 1` then a space, in `faintStyle`. It is shown
    on **every** strip when `!shared`, and **only on the first** strip when `shared`;
  - row 2: the name, `stats.FitKey(Name, NW, false)`, in `fgStyle`, or `faintStyle` when `Idle`;
  - the other rows are blank.
- **The totals**, on row 2 only: `" "` + `ShortTokens(Total)` right-aligned in 6 (`textStyle.Bold(true)`, or faint when
  idle), then the percent of `windowTotal`, formatted `%.0f%%` and right-aligned in 5 (`mutedStyle`).
  - An idle strip shows `0` and `0%`, faint.
  - The percent is `·` when `windowTotal` is 0.
- **An idle strip's plot** has only its `┊` grid cells.

**After the last strip:**
- **The x axis:** `"   " + NW spaces + " └"`, then `avail` cells of `─`, with `┴` at the tick centres (`ruleStyle`).
- **The date labels:** reuse `statsTimelineLabels`'s rules (MM-DD centred on each tick centre, placed right to left, and
  skipped when crowded), offset to this plot's start column.
  - If `statsTimelineLabels` cannot take the offset as it is, add an offset parameter to it. Keep the overview's output
    byte-identical.

### 4.4 `func statsWeekLine(days []stats.DayCost) string`

This **replaces** the old `statsWeekLine(sp stats.Spend)` (line 1943):

`"   "` + muted `this week ` + bold `ShortTokens(sum of the last 7 days)` + muted `   last week ` + bold `ShortTokens(sum of the 7 days before)` + muted `   busiest day ` + bold `MonthDay(day)` + muted `" " + ShortTokens(max)`.

- The busiest-day clause is omitted when every day is 0.
- `last week` reads `0` when the window has fewer than 8 days.

### 4.5 `func (v statsView) tokensTabLines(env Env, width, height int) []string`

The tab body, in order:
1. `statsSplitChips(v.split)`;
2. one blank line;
3. **total:** the heading `"   " + faint bold "TOKENS"`, then `statsTimelineLines(days, width, rows)` with
   `rows = clamp(height - 8, 8, 20)`. That depends on the screen only;
4. **candidate / provider / kind:** `statsStripLines(...)`;
5. one blank line;
6. `statsWeekLine(days)`.

With no tokens in the window, lines 3 and 4 are replaced by one line: `"   " + faint "no token usage recorded in this window"`.

`tabLines`' `"tokens"` case returns `v.tokensTabLines(env, width, height), -1`, with no `panel` rule. The page scrolls when
the strips are taller than the screen; that is `Body`'s existing scrolling.

### 4.6 Keys

**`updateKey`:**
- `s` on the tokens tab: `v.split = (v.split + 1) % 4`, then `v.top = 0`;
- **delete** the `p` case (lines 356-359).

**`Keys()`, the tokens case (line 228):** `{"s","split"}, {"tab","next tab"}, {"w","window"}`.

## 5. Deletions (closed list; everything not listed survives)

1. `statsView.byProvider` (line 61).
2. The `p` key case in `updateKey` (lines 356-359), and `{"p","by provider"}` in `Keys()` (line 228).
3. `spendLines` (lines 1744-1795) and `providerLines` (lines 1797-1841).
4. `statsPlotW`, `statsSteps`, `statsDayCols`, `statsBarRow` and `statsRange`, **only if** nothing else references them after
   deletion 3. Check with `grep -n` before deleting each one. `statsBuckets` and `statsBarRunes` survive if still used.
5. The old `statsWeekLine(sp stats.Spend)`, replaced by §4.4.
6. The test `TestStatsDayBarsWidenWhenDaysAreFew` (`view_stats_test.go:517-`). It pins `spendLines`, deleted in deletion 3.
   It is the **only** test this round may delete. Cite "deletion 6" in the report.

## 6. Tests

**`internal/stats/stats_test.go`:**
1. `TestSpendDayBreakdowns`:
   - **Setup:** two days, three rows:
     - day 1: candidate `A`, provider `p1`, tokens in 10, cache 100, out 5;
     - day 1: candidate `B`, provider `p2`, out 7;
     - day 2: candidate `A`, in 1.
   - **Expect:**
     - `Days[0].Kinds = {In 10, Cache 100, Out 12}`;
     - `Days[0].ByCandidate = {A: 115, B: 7}`;
     - `Days[0].TokensByProvider = {p1: 115, p2: 7}`;
     - `Days[1].ByCandidate = {A: 1}`;
     - `Tokens == Kinds.Total()` on both days.

**`internal/ui/view_stats_test.go`:**

2. **The geometry refactor:** every existing timeline test and the overview goldens pass unchanged. This is the proof
   that `statsGeom` is byte-identical.
3. `TestStatsTokensSplitCycle`:
   - A new view's split is 1.
   - `s` ×4 cycles 1→2→3→0→1 on the tokens tab.
   - `s` on the overview tab does nothing.
   - `Keys()` on the tokens tab contains `s` and no `p`.
4. `TestStatsTokensChips`:
   - The stripped first body line is exactly `total   candidate   provider   kind`, after trimming, with no `split`.
5. `TestStatsTokensCandidateStrips`:
   - **Setup:** a report whose days carry `ByCandidate` for two tokens (one larger), with `configured` = those two plus one
     idle token, at 132×60.
   - **Assert:**
     - the strips appear in the order large, small, idle;
     - the idle strip's name line equals `faintStyle.Render(...)` of its label, or check its row 2 is rendered faint;
     - the scale label appears **once**;
     - the `│` sits at the same column on every strip row;
     - there is exactly one `└` line;
     - every trimmed line is at most 129 cells;
     - the large strip's totals read its `ShortTokens` and its percent.
6. `TestStatsTokensKindOwnScale`:
   - The kind split shows `cache read`, `fresh input`, `output` and `cache write` in that order.
   - There are **four** scale labels.
   - The `output` strip's bottom row has at least one non-blank bar cell, even though output is about 1% of cache reads.
     That proves the scale is per strip.
7. `TestStatsTokensTotalIsTimeline`:
   - Split 0 shows `TOKENS`, `┤` and `└`, and no strip names.
   - Its plot row count is the same for two reports whose maxima differ by 10×. The fixed height, as on the overview.
8. `TestStatsWeekLine`:
   - A 14-day series with known values gives the exact `this week`, `last week` and `busiest day` text.
   - An all-zero series omits the busiest-day clause.
9. `TestStatsTokensEmpty`: a report with no tokens shows `no token usage recorded in this window` under the chips.

**Goldens** (`internal/ui/golden_test.go`):
- **Add** `stats-tokens-132`, 132×34: `statsOverviewReport()`, key `3`.
  - Give `overviewRows`' report `configured` tokens if the golden model supports it. If not, the golden shows no idle strip,
    which is acceptable; say so.
- Regenerate once. `stats-overview-132` and `stats-candidates-132` must **not** change: that proves the geometry refactor
  is byte-identical. If they change, halt.

## 7. Ordered steps

1. **The `stats.DayCost` fields** (§3.1).
   - **Done when:** test 1 passes and the CLI golden is unchanged.
2. **The geometry refactor** (§3.4).
   - **Depends on:** none.
   - **Done when:** test 2 passes, with every existing test and golden green, unchanged.
3. **The chips, series, strips, week line, tab body and keys** (§4).
   - **Depends on:** 1 and 2.
   - **Done when:** tests 3-9 pass.
4. **The deletions** (§5).
   - **Depends on:** 3.
   - **Done when:** it builds, and `grep -n 'spendLines\|providerLines\|byProvider' internal/ui/` is empty.
5. **Goldens and the full check** (§2).
   - **Done when:** everything is green, `gofmt -l` is empty, and the overview and candidates goldens are unchanged.
6. **Plans and commit.**
   - Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-tokens.md` into `docs/plans/`.
   - Then `git add -A && git commit --amend --no-edit`.

## 8. Report

Include:
- the tests added, and the one deleted (citing deletion 6);
- the stripped `stats-tokens-132.golden` in full;
- `git diff --stat f65cfa5 HEAD`;
- anything you halted on.
