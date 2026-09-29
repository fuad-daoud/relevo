# Cockpit D2 stats, tab 1 round 4: every configured candidate, no captions, uncut tiles, tables stack below 120, a chart that grows

Date: 2026-09-25. Worktree: ck-d2-stats. Its branch is one commit, `74c23893`, on origin/main 7db0ec7d. Build on it. At the
end, commit by **amending** that commit (`git commit --amend --no-edit`) so the branch stays one commit. Do not rebase or
push. Line numbers below are exact in the worktree now. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, or a step cannot be done as written, halt and
report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- `internal/ui/golden_test.go` (only if a fixture must change)
- the stats goldens under `internal/ui/testdata/`
- `docs/plans/` (step 7)

## 1. System overview

`:stats` has five tabs, and this round changes only the **overview** tab. The user reviewed round 3 on real data. Their
database has seven configured candidates, but only three ever ran a recorded builder round, so the CANDIDATES table showed
three rows and the user thought candidates were missing. They asked for:

1. **List every configured candidate.** The candidates with rounds come first, as today. Every other configured candidate
   follows as a **faint** row that shows `0` rounds and `·` for everything else.
2. **Drop the table captions entirely.** `by rounds, under 5 dimmed` and `by tokens` go. Each section line keeps only its
   title, plus the `a–b of n` scroll count when the table overflows.

The planner found three more defects in the captures:

3. **The ROUNDS tile's note is cut at 132 columns:** `25 halted · 7 open · 6m medi…`. The tile pitch lost a cell to the
   right padding. Fix the pitch so the note fits and the right margin still holds (§4.4).
4. **The tables are cramped between 96 and 119 columns.** Names cut to `deepsee…`, and the header's `CACHE` is clipped to
   `CACH`: the header's `CANDIDATE` label is wider than the name column and is not clipped. Stack the tables below 120
   columns, and clip the header labels to the name column.
5. **The chart is always 8 plot rows** and leaves empty rows on a tall screen. Let it take up to four rows per y interval
   when the tables do not need the space.

The other four tabs, and every helper they use, stay **unchanged**. That includes `candidatesLines`, `moveCursor`,
`enter`, `statsSteps`, `statsBuckets`, `statsBarRow`, `statsDayCols`, `statsPlotW` and `spendLines`.

## 2. Working efficiently

Every model step costs one round trip, whatever it does. So:

- Read these once, in one batched step:
  - `internal/ui/view_stats.go` in full;
  - `internal/ui/view_stats_test.go` in full;
  - `internal/ui/golden_test.go:455-600`;
  - `internal/candidate/candidate.go:96-215`.

  Every location you need is named here, so do not search for anything this plan locates.
- Make all the changes to `view_stats.go` in as few edit calls as you can.
- Iterate with the focused command below. Fix every error it reports before you run it again.
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Stats|Golden' -count=1`
- Regenerate the goldens once, when the code is done:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`
- Run the full check once, at the end:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
  - `gofmt -l` must print nothing.
- Every test here is a pure view test. None may spawn a harness or reach the network.

## 3. Data structures

### 3.1 `statsView` gains `configured []string` (the struct at `view_stats.go:48-60` and following)

- **What it holds:** every configured candidate's canonical token, from `env.Src.Base().Candidates.Refs()`, which is sorted.
  It is nil when the set is nil.
- **The nil guard:** `(*candidate.Set).Refs` dereferences its receiver and does **not** tolerate a nil set, so check
  `Candidates != nil` before calling it.
- **When it is set:**
  - in `newStatsView` (currently lines 89-95);
  - on every successful `statsMsg` in `Update` (the `v.rep = msg.rep` branch), so a config edit shows up on the next
    refresh.
- **Tests** that build `statsView{...}` directly leave it nil, and their output is unchanged.

### 3.2 `statsOverviewCand`: one overview candidate row, unexported, in `view_stats.go`

| field | type | meaning |
|---|---|---|
| `Token` | `string` | The candidate's canonical token. |
| `Row` | `stats.ScoreRow` | Its scorecard row. The zero value when `Idle`. |
| `Idle` | `bool` | True for a configured candidate with no scorecard row in this window. |

### 3.3 `func (v statsView) overviewCandRows(names func(string) string) []statsOverviewCand`

- First, every `v.rep.Scorecard` row, in report order, with `Idle: false`.
- Then every token in `v.configured` that no scorecard row carries, sorted by `names(token)` ascending, each with
  `Idle: true`.
- A scorecard token that is no longer configured stays: its row is data, not config.
- The table and the scroll count use this order, and so do the cursor and `enter`.

`overviewRows(focus)` (line 365) returns `len(v.overviewCandRows(identity))` for focus 0. The count does not depend on
names, so any `names` function will do.

## 4. Contracts (all in `internal/ui/view_stats.go`)

### 4.1 `statsOverviewCandidates(env Env, cellW, rows int) []string` (lines 983-1011)

**Section line:** `faintStyle.Bold(true).Render("CANDIDATES")`, plus the existing faint `   a–b of n` suffix when `n > rows`.
Delete the caption `   by rounds, under 5 dimmed`.

**Header:** clip the name label to the name column, `stats.FitKey("CANDIDATE", nameW, false)`, then the same numeric
headers as today. The header's width is therefore always `nameW + 37`.

**Rows:** iterate `overviewCandRows(names)[first:last]`.
- **A non-idle row:** exactly as today, dimmed when `Row.Few`.
- **An idle row:** `faintStyle.Render(stats.FitKey(name, nameW, false) + fmt.Sprintf("%6d%6s%9s%9s%7s", 0, "·", "·", "·", "·"))`.
- **The selected row** (`v.ovFocus == 0 && i == cur`) is the band, for either kind. The plain text of an idle row is the
  same text without the style.

### 4.2 `statsOverviewRepos(cellW, rows int) []string` (lines 1052-1090)

- **Section line:** faint bold `BUSIEST REPOS`, plus the scroll suffix. Delete `   by tokens`.
- **Header:** clip the `REPO` label with `stats.FitKey("REPO", nameW, false)`.

### 4.3 `enterOverview(env Env) (View, tea.Cmd)` (lines 402-420)

- **Focus 0:** index into `overviewCandRows(names)` with `names = env.Src.Base().Candidates.NameOf`.
  - An idle row returns `v, notice(names(token) + " has no rounds in this window")`.
  - Otherwise it returns `roundsForCandidate(env, token)`, as today.
- **Focus 1:** unchanged.

### 4.4 Tile pitch: `statsTilesLines` (lines 590-611) and `statsTileCell` (614-625)

The pitch becomes `pitch = (width - 4) / 4`, or `(width - 4) / 2` below `statsOverviewWide`.
- Tile `i` starts at `3 + i*pitch`, and each tile's text is cut to `pitch - 2`.
- The last tile's text therefore ends at or before `3 + 4*pitch - 2 ≤ width - 3`.
- At 132 columns the pitch is 32, so a 30-cell note (`25 halted · 7 open · 6m median`) fits whole.
- Rename the parameter from `tileW` to `pitch` and update both comments.

### 4.5 The tables stack below 120: `overviewLines` (lines 536-587)

- Add `const statsOverviewTablesWide = 120`, with a comment: side-by-side tables at or above it, stacked below it.
- The tables' branch tests `width >= statsOverviewTablesWide` in place of `statsOverviewWide`.
- The tiles keep `statsOverviewWide` (96).
- **Delete** the unused `statsOverviewLeftW` (lines 526-528) and `statsTileW` (lines 530-531), with their comments.

### 4.6 The chart grows

**`statsTimelineLines(days []stats.DayCost, width, budget int) []string`** (line 679):
- `budget` is the most plot rows the chart may use.
- `per := budget / m`, clamped to `2..4`. This is the number of plot rows per y interval, and it is at least 2 even when the
  budget is smaller.
- `H = per * m`.
- The tick rows are `r = H - per*j` for `j = 1..m`. `tickRow` (lines 772-781) replaces its `2` with `per`.
- The bar level formula is unchanged: `round(8*H*c/axisMax)`.
- Everything else is unchanged.

**`statsTokenChartLines(width, budget int)`** (line 642) passes `budget` through.

**`overviewLines`** computes the budget before it builds the chart, then passes it:

```
tileLines   = len(statsTilesLines(tiles, width))           # 3, or 6 below 96
nCand       = len(v.overviewCandRows(names))
nRepo       = len(v.overviewRepoRows())
if width >= statsOverviewTablesWide:
    tableNeed = 2 + max(nCand, nRepo)
else:
    tableNeed = 2 + nCand + 1 + 2 + nRepo
# everything but the plot rows: tiles, two blanks, heading, axis, labels, blank
budget = height - tileLines - 2 - 1 - 2 - 1 - tableNeed
```

The table viewport `rows` is still computed from the lines actually above the tables (the existing `fixed`), so a grown
chart never pushes the tables off screen by more than the budget allowed.

### 4.7 Keys and footer

There are no changes. `Keys()` for overview stays as it is.

## 5. Error handling

This is pure rendering. The degenerate cases are defined:
- **A nil candidate set:** `configured` is nil, and the table is exactly round 3's.
- **A budget below `2m`:** `per` is 2, which is round 3's height.
- **No tokens:** the existing one-liner, and `budget` is unused.

## 6. Tests (`internal/ui/view_stats_test.go`)

**Port these.** Change their assertions and keep their intent:

1. `TestStatsOverviewFillsWidth`: the tile column becomes `3 + 3*((w-4)/4)`.
2. `TestStatsOverviewNarrowStacks`: the tile pitch becomes `(w-4)/2`, at the same width of 90.
3. Any test that asserts the text `by rounds`, `under 5 dimmed` or `by tokens`: delete that assertion only.
4. Any test that calls `statsTimelineLines(days, w)`: it becomes `statsTimelineLines(days, w, 0)`, which gives `per` 2, so
   its assertions hold.
5. Any test that renders the overview side by side at a width of 96-119 (check the width each overview test uses):
   - if it asserts side by side, move it to 132;
   - otherwise leave it alone.

**Add these:**

6. `TestStatsOverviewNoCaptions`: at 132×34 the stripped body contains none of `by rounds`, `dimmed` or `by tokens`.
7. `TestStatsOverviewConfiguredCandidates`:
   - **Setup:** `statsTestView("30d")` with `configured` set to the fixture's scorecard tokens plus
     `"claude/anthropic/haiku"` and `"codex/openai/gpt-5.6-terra:high"`.
   - **Assert that:**
     - the two idle rows follow every scorecard row, in name order (no names resolve in the test env, so the names are the
       tokens themselves);
     - each idle row's stripped text ends `0     ·        ·        ·      ·`;
     - an idle row, unselected, equals `faintStyle.Render(...)` of its plain text exactly.
   - **Moving onto an idle row:** move the cursor to the first idle row with `j` presses. `enter` then returns a
     non-nil command, and no `push`: run the command and assert it produces the notice message type the file already uses
     for `notice(...)`. Check how `TestStatsEnterOpensFilteredRounds` or `notice` is tested and copy that pattern.
   - **A token no longer configured:** set `configured` to only the two extra tokens. The fixture's scorecard rows still
     all appear.
8. `TestStatsOverviewTilesUncut`:
   - At 132×34, with the fixture's ROUNDS note (`… median`), the stripped body contains the whole note and no `…` on the
     tile rows.
   - At 100, 132 and 200, every stripped overview line still has a trimmed length of at most `w-3`. If
     `TestStatsOverviewSidePadding` already covers this, extend it and add nothing new.
9. `TestStatsOverviewTablesStackBelow120`:
   - At 110×40: the CANDIDATES header line does not contain `SHARE`, and the `REPO` header comes on a later line.
   - At 120×40: one line contains both `IN/RND` and `SHARE`.
   - At 100×40: the candidates header line contains `CACHE` whole.
10. `TestStatsTimelineGrows`:
    - `statsTimelineLines(days, 132, 100)` for the §`TestStatsTimelineTicks` 30-day fixture (max 6.8M, so `m = 4`) has
      16 plot rows, and `┤` on rows 0, 4, 8 and 12.
    - `statsTimelineLines(days, 132, 12)`: per is `12/4 = 3`. Assert 12 plot rows and `┤` on rows 0, 3, 6 and 9.
    - `statsTimelineLines(days, 132, 5)` has 8 rows (per is clamped to 2).
11. `TestStatsOverviewChartUsesSpareRows`:
    - The overview at 132×60 with the fixture has more chart plot rows than at 132×34.
    - Count the lines between the `TOKENS   per day` heading and the axis line (`└`).

**Goldens:** regenerate once. The chart in `stats-overview-132` should stay 8 plot rows at height 34. If it grew, report the
budget the code computed and halt instead of accepting it.

## 7. Ordered implementation steps

1. **Candidate rows.** The `configured` field and where it is set, `statsOverviewCand`, `overviewCandRows`, `overviewRows`,
   `enterOverview`, and the candidates table's rows and header (§3, §4.1, §4.3).
   - **Depends on:** none.
   - **Done when:** test 7 passes.
2. **Captions and header clipping** (§4.1, §4.2).
   - **Depends on:** 1.
   - **Done when:** test 6, and test 9's `CACHE` case, pass.
3. **Tile pitch** (§4.4).
   - **Depends on:** none.
   - **Done when:** ported tests 1 and 2, and test 8, pass.
4. **Stacking at 120, and the dead constants deleted** (§4.5).
   - **Depends on:** 2.
   - **Done when:** test 9 passes.
5. **The chart grows** (§4.6).
   - **Depends on:** 4.
   - **Done when:** tests 10 and 11, and ported test 4, pass.
6. **Goldens and the full check** (§2, §6).
   - **Depends on:** 5.
   - **Done when:** everything is green and `gofmt -l` is empty.
7. **Plans and commit.**
   - Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview-r4.md` (this file) into the worktree's
     `docs/plans/`.
   - Then `git add -A && git commit --amend --no-edit`.
   - **Depends on:** 6.
   - **Done when:** `git log --oneline origin/main..HEAD` shows one commit.

## 8. Report

Include:
- the tests added and ported;
- the stripped `stats-overview-132.golden` in full;
- `git diff --stat 74c23893 HEAD` (this round's changes);
- anything you halted on.
