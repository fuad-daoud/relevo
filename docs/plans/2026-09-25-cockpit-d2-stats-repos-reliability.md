# Cockpit D2 stats, tabs 4 and 5: repos and reliability

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `d80634f`, on origin/main 753aa4e. Build on it, and amend
it at the end (`git commit --amend --no-edit`). Do not rebase or push. Line numbers are exact in the worktree now. This is
one round, and **repos comes first**: it is the user's priority. If you run short, finish repos completely, then report
reliability as not done rather than doing both halfway.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/stats/stats.go` and `internal/stats/stats_test.go` (§3.1 only);
- `internal/ui/view_stats.go`, `internal/ui/view_stats_test.go` and `internal/ui/golden_test.go`;
- the stats goldens;
- `docs/plans/`.

## 1. System overview

`:stats` has five tabs, and the first three are done. This round redraws the last two, which still show the old C2 panels
(`tabLines`, `view_stats.go:673-679`):
- **reliability:** `v.panel("reliability", v.reliabilityLines())`;
- **repos:** `v.panel("repos", groupsLines…)`, a features panel, and an `outcomes` panel.

The approved design boards, on real data, 132 columns (`·` means no value):

**repos:**
```
   REPO                                     RNDS  BINDINGS  DONE  HALTED  COMMITS   TOKENS  SHARE
   fuad-daoud/relay                          240       135   196      25      224     1.7B  ▇▇▇▇▇▇▇▇▇▇  90%
   fuad-daoud/money                           25         8     0       0       12   106.5M  ▇            6%
   (no repo)                                 165       101    15       8       49    20.2M  ▇            1%
   dexpace/spaceapi                           24        10     0       0       20        0               0%

   FEATURE                                  RNDS  BINDINGS  DONE  HALTED  COMMITS   TOKENS  SHARE
   cockpit                                    21        11    ..      ..       ..   195.7M  ▇▇           11%


   fuad-daoud/relay   https://github.com/fuad-daoud/relay
   240 rounds on 135 bindings   1.7B tokens, 90% of the window
   196 done · 25 halted · 224 commits   1.7 rounds per landed binding
   by candidate: deepseek-v4.1-flash 203 rounds · gemini-3.8-flash-high 37
```

**reliability:**
```
   SWITCHES                        RATE LIMITS                     SPAWN FAILURES                  GATED NOW
   26                              16                              1                               3
   in 19 rounds · 4% of rounds     across 4 providers              builders that failed to start   candidates waiting on a limit


   CANDIDATE               PROVIDER     UNTIL            LEFT   REASON
   claude-sonnet-4-6       agy-extra    Sep 26 18:03       1d   Individual quota reached
   gemini-3.8-flash-high   google       19:02              1h   Individual quota reached · resets in 2h37m


   LIMITS BY HOUR    0  1  2  3  4  5  6  7  8  9 10 11 12 13 14 15 16 17 18 19 20 21 22 23
   google            ·  1  ·  ·  ·  ·  ·  ·  ·  ·  1  3  2  ·  ·  ·  1  ·  ·  1  ·  ·  1  3
```

**The user's decisions:**
- **No outcomes block** anywhere.
- **No recent-switches table.**
- **Reliability has no detail block and no cursor.** Its `↑↓` already scroll, via `tabHasCursor`.
- **The feature table has the repos table's columns**, all of them.
- **The same rules as the other tabs:**
  - no titled rules, no captions, no footnotes, no dollars;
  - header rows name the tables;
  - a 3-cell margin on each side, with nothing past `width - 3`;
  - HALTED counts **reports** that said halted, as on the candidates tab.

## 2. Working efficiently

- Read these once, in one batched step:
  - `internal/ui/view_stats.go` in full;
  - `internal/ui/view_stats_test.go` in full;
  - `internal/ui/golden_test.go:455-660`;
  - `internal/stats/stats.go:160-240` and `:500-575`;
  - `internal/ledger/ledger.go:228-250`.
- The focused command:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/stats/ ./internal/ui/ -run 'Group|Stats|Golden' -count=1`
- The goldens once, at the end: `-run TestGoldenViews -update`.
- The full check, once:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
- These are pure tests: no harness and no network.

## 3. Data structures

### 3.1 `stats.GroupRow` gains five fields (`internal/stats/stats.go:172-178`), filled in `groupRows` (`:504-`)

| field | type | meaning |
|---|---|---|
| `Bindings` | `int` | The number of distinct `BindingID` among the group's rows. |
| `Done` | `int` | The rows whose `ReportOutcome` is `"done"`. |
| `ReportHalted` | `int` | The rows whose `ReportOutcome` is `"halted"`. |
| `Commits` | `int` | The sum of the non-nil `RoundRow.Commits`. |
| `ByCandidate` | `map[string]int` | The round count per `BuilderCandidate`, skipping nil. |

Leave `Halted` (round outcome), `Landed` and `RoundsPerLand` alone. `render.go` and the CLI output are **unchanged**.

### 3.2 `statsView`

There are no new fields.
- The repos tab keeps `focus 1` and `cursor[1]`.
- `cursor[1]` now indexes **one combined list**: the repo rows first, then the feature rows (§4.2).

## 4. Contracts: repos (all in `internal/ui/view_stats.go`)

### 4.1 `func (v statsView) repoTabRows() (repos, features []stats.GroupRow)`

- `repos` is `v.overviewRepoRows()`: tokens desc, stable.
- `features` is a copy of `v.rep.Features`, stable-sorted by `Tokens` desc.
- The combined cursor list is `repos` followed by `features`, and `n = len(repos) + len(features)`.

### 4.2 `func (v statsView) reposTabLines(env Env, width int) (lines []string, sel int)`

**The columns.** After the name column: `RNDS` 6, `BINDINGS` 10, `DONE` 6, `HALTED` 8, `COMMITS` 9, `TOKENS` 9, then
`SHARE` (the existing 17-cell `statsShareCell`).
- `nameW = width - 6 - (the fixed widths)`, and the table spans `[3, width - 3)`.
- **Narrow screens:** while `nameW < 16`, drop `COMMITS`, then `BINDINGS`, then `DONE`, in that order. Reuse the drop-rank
  idea from the candidates tab's `statsCandCol`: a small column table with ranks.

**Layout, in order:**
1. The header row: faint bold, `REPO` clipped with `FitKey`, then the column heads right-aligned. `SHARE` is left-aligned
   in its cell, as the overview does.
2. One row per repo.
3. One blank line.
4. The features header row: `FEATURE`, then the **same** column heads.
5. One row per feature.
6. Two blank lines.
7. The detail block for the selected row (§4.3).

If there are no features, leave out lines 3-5.

**Each row:**
- `shortRepo(Key)` for a repo, which gives `(no repo)` for `(none)`, or the feature name;
- then the numbers: `Rounds`, `Bindings`, `Done`, `ReportHalted`, `Commits`, `ShortTokens(Tokens)`;
- then `statsShareCell(Tokens, maxTokens, windowTotal)`, where `maxTokens` is over the repos for a repo row, over the
  features for a feature row, and `windowTotal = Totals.TokenKinds.Total()`.

**Styling:**
- **Normal:** the name in `fgStyle`, the numbers in `mutedStyle`.
- **Selected** (combined index `== clamp(v.cursor[1])`): the band. The plain text is rebuilt and rendered once with
  `selBandStyle.Foreground(textStyle.GetForeground()).Bold(true)`, using `statsSharePlain`, as the overview does. It is
  fitted so it ends at `width - 3`.

**`sel`** is the selected row's line index, so the page follows the cursor.

### 4.3 `func (v statsView) statsGroupDetail(env Env, g stats.GroupRow, feature bool, windowTotal int64) []string`

Four lines, each starting `"   "` and each cut to `width - 6`:
1. **The name** (faint bold), `"   "`, then muted: the full `Key` for a repo, `"feature"` for a feature, or nothing for
   `(none)`.
2. **Totals:** bold `<Rounds>`, muted ` rounds on `, bold `<Bindings>`, muted ` bindings   `, bold
   `ShortTokens(Tokens)`, muted ` tokens, <p>% of the window`.
   - Use singulars for a count of 1.
   - Leave out the percent clause when `windowTotal` is 0.
3. **Outcomes**, all muted: `<Done> done · <ReportHalted> halted · <Commits> commits`. When `Landed > 0`, add
   `   <RoundsPerLand %.1f> rounds per landed binding`.
4. **Candidates**, muted: `by candidate: ` plus the top 3 of `ByCandidate` by count desc (ties by name), each
   `<NameOf(token)> <n> rounds` (`round` for 1), joined with ` · `. Leave the line out when `ByCandidate` is empty.

### 4.4 Keys, cursor and enter on the repos tab

- **`panelRows`** (lines 434-439), focus 1: `n` from §4.1.
- **`enter`** (lines 506-527), focus 1:
  - index the combined list;
  - a repo row behaves as today: `roundsForRepo`, or the `(none)` notice;
  - a feature row opens `v.openRounds(env, "feature:\"" + key + "\"" + v.sinceTerm())`. The rounds query already
    supports `feature:` (`internal/histq/histq.go:263`).
- **`follow`** already works on any tab that returns `sel >= 0`.

## 5. Contracts: reliability

### 5.1 `func (v statsView) reliabilityTabLines(env Env, width int) []string`

**1. Tiles:** reuse `statsTilesLines` with these tiles:
- `{"SWITCHES", <Switches>, "in <RoundsSwitched> rounds · <SwitchPct %.0f>% of rounds"}`;
- `{"RATE LIMITS", <RateLimits>, "across <len(ByHour)> providers"}`;
- `{"SPAWN FAILURES", <SpawnFailures>, "builders that failed to start"}`;
- `{"GATED NOW", <n>, "candidates waiting on a limit"}`, where `n` is the `Active` gates whose `Role` is `""` or
  `"builder"`.

Then two blank lines.

**2. The gates table.** Header, faint bold: `CANDIDATE` (24), `PROVIDER` (13), `UNTIL` (16), `LEFT` (right-aligned in 5),
three spaces, `REASON`. Then one row per counted gate, in `Active` order:
- the name: `NameOf(g.Token)` in `fgStyle`;
- the provider: `candidate.ParseRef(g.Token).Provider`, or `·` on a parse error, in muted;
- **UNTIL**, in muted:
  - `Until.Local().Format("15:04")` when it is today;
  - else `Format("Jan 2 15:04")`;
  - `until cleared` when `Until` is zero;
- **LEFT** in `redStyle`: `strings.TrimPrefix(statsGateLeft(g, now), "gated ")`, which is empty for a zero `Until`;
- **REASON**: `g.Note` in faint, cut so the row ends at `width - 3`, with `·` when the note is empty.

With no gates, one faint line replaces the whole table: `no candidate is gated`. Then two blank lines.

**3. The limits-by-hour table.** Header, faint bold: `LIMITS BY HOUR` padded to 16, then the hours `0`…`23`, each
right-aligned in 3. Then one row per `ByHour` provider: the provider muted and padded to 16, then 24 cells, each right-aligned
in 3:
- `·` in `gridStyle` for 0;
- the count in `textStyle` for 1-2;
- the count in `redStyle.Bold(true)` for 3 or more.

With no rows, one faint line: `no rate limits in this window`.

The tab has no cursor and no detail block.

### 5.2 `tabLines`

- `"reliability"` → `v.reliabilityTabLines(env, width), -1`.
- `"repos"` → `v.reposTabLines(env, width)`.

Neither uses `panel`.

## 6. Deletions (closed list; everything not listed survives)

1. `reliabilityLines` (line 2218).
2. `groupsLines` (line 2262).
3. `outcomesLines` (line 2285), `statsOutcomeKeys` (2295), `statsReportKeys` (2304) and `statsCountPairs` (2313).
4. The `statGroup` constant and its comment (lines 68-69), if nothing else uses it.
5. `panel` (line 1530) and `statsTitleRule` (line 1594), **only if** nothing else calls them after deletions 1-3. Check with
   `grep -n`.
6. Any test that asserts the deleted outcomes block, or the old `COST`/`RNDS/LAND` columns. Delete **only** those; cite
   the deletion number for each in the report. A test that pins surviving behaviour through the old panels is **ported**,
   not deleted. That includes `TestStatsFocusAndCursor`'s repos part: with the fixture's feature, the combined list has 3
   rows, so `j` twice now reaches index 2. Port its expectations.

## 7. Tests

**`internal/stats/stats_test.go`:**
1. `TestGroupRowsBreakdowns`:
   - **Setup:** one repo, 4 rows over 3 bindings, with `ReportOutcome` done, done, halted and nil, `Commits` 2, nil, 1 and
     0, and candidates A, A, B and nil.
   - **Expect:** `Bindings 3`, `Done 2`, `ReportHalted 1`, `Commits 3`, `ByCandidate {A:2, B:1}`.

**`internal/ui/view_stats_test.go`:**

2. `TestStatsReposTabTables`, at 132×40 on the fixture:
   - no `──` rule, no `outcomes`, no `RNDS/LAND`, no `$`;
   - the repos header contains `BINDINGS`, `COMMITS` and `SHARE`;
   - the features header begins `FEATURE` and has the **same** column heads, at the same columns: check with `statsColEnd`
     for `TOKENS`;
   - repos come before features;
   - every trimmed line is at most 129 cells.
3. `TestStatsReposTabDetail`:
   - With the cursor on row 0, the body contains `rounds on`, `% of the window` and `by candidate:`, when the fixture's
     `ByCandidate` is set.
   - With the cursor moved onto the feature row, line 1 of the detail ends with `feature`.
4. `TestStatsReposTabEnterFeature`: `enter` on the feature row pushes a rounds view whose query is `feature:"<name>" since:30d`.
   Use `statsShell`, as `TestStatsTabsKeys` does.
5. `TestStatsReposTabDropsColumns`:
   - at 80 columns, `COMMITS` is gone first, and the name column is at least 16;
   - no line passes `w-3`.
6. `TestStatsReliabilityTab`, at 132×40:
   - **Setup:** a report with 2 active gates (one with `Role` `"reviewer"`, which must **not** show), 2 `ByHour` rows (one
     count of 3), and `Switches 5`, `RoundsSwitched 3`.
   - **Assert:**
     - the four tile labels are present;
     - `GATED NOW` shows `1`;
     - the gate row shows its name, provider, `UNTIL` and `LEFT`;
     - the `3` in the hours row is rendered with `redStyle.Bold(true)`: compare the ANSI output against a rendered
       fragment;
     - there is no `──` rule.
7. `TestStatsReliabilityEmpty`: with no gates and no `ByHour`, the body shows `no candidate is gated` and
   `no rate limits in this window`.

**Goldens** (`internal/ui/golden_test.go`):
- Add `stats-repos-132` (key `5`) and `stats-reliability-132` (key `4`), both 132×34 on `statsOverviewReport()`, as
  `stats-candidates-132` does.
- The overview, candidates and tokens goldens must **not** change. If they do, halt.

## 8. Ordered steps

1. §3.1, with test 1.
2. The repos tab (§4), with tests 2-5 and the `TestStatsFocusAndCursor` port.
3. The reliability tab (§5), with tests 6-7.
4. The deletions (§6).
5. The goldens and the full check.
6. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-repos-reliability.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 9. Report

Include:
- the tests added, ported and deleted (with deletion numbers);
- the stripped `stats-repos-132.golden` and `stats-reliability-132.golden`;
- `git diff --stat d80634f HEAD`;
- anything you halted on.
