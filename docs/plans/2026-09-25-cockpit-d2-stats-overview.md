# Cockpit D2 stats, tab 1: token totals in the engine, a tab bar, and the overview tab

Date: 2026-09-25. Base: origin/main c7c5cbe0 (D2 fleet #449 and round detail #460 merged). One round.
Design: canvas row **Stats in D2**, board `:stats · overview (tokens-first)`
(https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA). Stats is being redone **one tab at a time**, with the user
reviewing each: this round delivers the tab bar and **overview** only. The other four tabs temporarily show today's
panels, full width.

**Stop rather than improvise.** If a step is impossible as written or the code differs from what is quoted, halt and
report. Do not bend a test to make it pass. Change only `internal/stats` and `internal/ui` (no CLI text changes in this
round, so `history --stats` text output must stay byte-identical; its `--json` may gain fields).

## 1. Why tokens

Most users are on subscription plans, so dollars are mostly `$0` or unknown. Tokens (fresh input, cache read,
cache write, output) are what they can watch. On the user's machine, over 30 days:
- 1.61B tokens, of which 1.54B were cache reads (96% of input), 58M fresh input and 13.4M output;
- only 217 of 459 rounds recorded usage.

The engine already reads the four token fields per round (`rowTokens`, internal/stats/stats.go:520), but only reports
their sum.

## 2. Engine (internal/stats/stats.go): additive only

```
// TokenCounts is a sum of token usage over rounds that recorded any.
type TokenCounts struct {
	In, Cache, Write, Out int64
	Measured              int // rounds with at least one non-nil token field
}
func (c TokenCounts) Total() int64                    // In+Cache+Write+Out
func (c TokenCounts) CachePct() (float64, bool)       // Cache/(In+Cache)*100; false when In+Cache == 0
func (c TokenCounts) PerRound(n int64) (int64, bool)  // n / Measured; false when Measured == 0
```

- `Totals` gains `TokenKinds TokenCounts`, summed over **every** windowed row (unrecorded included, like `Tokens`).
  `Tokens` stays and must equal `TokenKinds.Total()`.
- `ScoreRow` gains `TokenKinds TokenCounts`, summed over that candidate's rows.
- `DayCost` gains `Tokens int64`, bucketed by the same local day as `USD`.
- `GroupRow` gains `Tokens int64` (repos and features).
- Add a helper `addTokens(*TokenCounts, db.RoundRow)` and use it in `buildTotals`, `buildScorecard`, `buildSpend` and
  `groupRows`.
- `render.go` (the CLI text) is **unchanged**.
- The JSON gains the new fields. Existing JSON keys must not change.

## 3. View (internal/ui/view_stats.go)

### 3.1 Tabs

- `statsView` gains `tab int`, indexing `statsTabs = []string{"overview", "candidates", "tokens", "reliability", "repos"}`.
  A new view starts on overview.
- **Keys:**
  - `tab` / `shift+tab` go to the next / previous tab (wrapping).
  - `1`-`5` jump to a tab.
  - `w` cycles the window (unchanged).
  - `↑↓` and `enter` work only on **candidates** (focus 0) and **repos** (focus 1): set `focus` from the tab,
    and reuse `cursor`, `moveCursor` and `enter` unchanged.
  - `p` works only on **tokens**.
  - `pgup`/`pgdn` and `r` are unchanged.
- **The tabs row** is the first body line, then a blank line. It uses the round view's tab style: the active tab is
  `chip(chipAccentStyle, name)`, the others `chip(normalStyle, name)` in muted, with three spaces between tabs. Reuse the round view's
  helper if it is factored; otherwise write `statsTabsRow`.
- **Temporary bodies** for the not-yet-redone tabs, each the existing panel full width via `v.panel(...)`:
  - candidates: `candidatesLines`;
  - tokens: `spendLines` + `providerLines`, as today's spend panel;
  - reliability: `reliabilityLines`;
  - repos: `groupsLines(Repos)`, then features, then `outcomesLines`.

### 3.2 Context row (`Context`, view_stats.go:118)

- **Left:** three spaces, then `<n> <label>` pairs separated by three spaces, each number in text bold and each label in muted:
  - `Rounds rounds`;
  - `ShortTokens(TokenKinds.Total()) tokens`;
  - `<CachePct>% cached` (omitted when unknown);
  - `ShortTokens(Out) out`;
  - `Duration(MedianMS) median`;
  - `<halted> halted`, where halted = `Outcomes.ByReport["halted"]`.
- **Right:** faint `window  `, then the four windows as chips: the active one `chip(chipAccentStyle, w)`, the others muted, one space
  apart. Then ` · refreshing` or the error, as today, and two spaces.
- **No dollars** anywhere in the context row.

### 3.3 The overview tab (new `overviewLines(env Env, width int) []string`)

The reference render, at 132 wide, below the tabs row:
```
   ROUNDS                        TOKENS                        CACHE                         OUTPUT
   459                           1.61B                         96%                           13.4M
   22 halted · 7 open            217 rounds measured           of input served from cache    62k per round


   TOKENS   per day, 30 days
   821M │                                                          ██
        │                                                        ▃▃██
      0 └────────────────────────────────────────────────────────────
        08-26                                                  09-24

   CANDIDATES   done · in/rnd · out/rnd · cache                   BUSIEST REPOS   tokens
   deepseek-v4.1-flash       99%    6.9M    56k    99%            fuad-daoud/relay         1.42B
```

**Tiles:** four columns, each 30 wide, starting at column 3. Three lines per tile:
1. the label, faint bold;
2. the value, text bold;
3. the note, faint.

| label | value | note |
|---|---|---|
| ROUNDS | `Rounds` | `<ByReport halted> halted · <ByRound open> open` |
| TOKENS | `ShortTokens(Total)` | `<Measured> rounds measured` |
| CACHE | `<CachePct>%` or `·` | `of input served from cache` |
| OUTPUT | `ShortTokens(Out)` | `<ShortTokens(Out/Measured)> per round` |

Below 100 columns, lay the tiles out 2×2.

**Chart:** two blank lines, then the heading `   TOKENS` (faint bold) + faint `   per day, <window>`, then a 5-row bar chart of
`Spend.Days[i].Tokens`:
- Reuse the existing bar machinery (`statsDayCols`, `statsBuckets` / `statsSteps`, `statsBarRow`), with values from `.Tokens` instead of `.USD`. Generalise
  those helpers to take `[]float64` if they read `USD` directly.
- The y label is `ShortTokens(max)` on the top row and `0` on the axis row. The axis is `└` + `─`.
- The x labels are the first and last day as `MM-DD`, left and right under the plot.
- With no token data in the window, one faint line: `no token usage recorded in this window`.

**Two columns** (left from column 3, right from column 66; below 100 columns, stack them):
- **CANDIDATES:** the heading, then up to 3 non-`Few` scorecard rows ordered by `Rounds` desc. Each row has:
  - `BuilderName` (fall back to the token), padded to 24;
  - done %, width 5;
  - in/rnd, width 8, meaning `(In+Cache)/Measured`;
  - out/rnd, width 7;
  - cache %, width 7.

  Numbers are in muted. With nothing measured, show `·`.
- **BUSIEST REPOS:** the heading, then the top 3 `Repos` by `Tokens`, via `shortRepo(key)`, padded to 22, plus `ShortTokens(Tokens)` in width 8.
  `shortRepo` gives the last two `/`-segments of a URL or path; `(none)` becomes `(no repo)`.

Colours come from styles.go tokens only.

### 3.4 Footer keys (`Keys`)

The keys depend on the tab:

| tab | keys |
|---|---|
| overview | `tab next tab`, `w window`, `esc back` |
| candidates, repos | `↑↓ move`, `enter its rounds`, `tab next tab`, `w window` |
| tokens | `p by provider`, `tab next tab`, `w window` |
| reliability | `tab next tab`, `w window` |

## 4. Deletions (closed list)

- **S1** The all-panels dashboard layout: `wideBody` and `narrowBody` (view_stats.go:307-371) and their call in `Body`. Every panel
  function (`candidatesLines`, `spendLines`, `providerLines`, `reliabilityLines`, `groupsLines`, `outcomesLines`, `panel`) survives,
  used by the temporary tabs.
- **S2** `tab` toggling `focus` between two panels (view_stats.go, `updateKey`'s `case "tab"`). `tab` now switches tabs.
- **S3** Dollars in the context row.

**Ports:** tests that asserted the dashboard layout, `tab`-as-focus, or `$` in the context row are ported. Cite S1-S3. Likely
`view_stats_test.go`. The golden files `stats-wide`, `stats-narrow` and `stats-empty` are regenerated.

## 5. Tests

- **Engine** (internal/stats):
  - `TestTokenKindsSumAndCache`: rows with mixed nil and non-nil token fields. `TokenKinds` equals the hand sum, `Measured` counts
    only rows with any field, `Tokens == TokenKinds.Total()`, and CachePct is correct. Zero input gives `ok == false`.
  - `TestScoreRowTokens`, `TestDayTokensBucketByLocalDay` (a fixed-offset Loc, a row near midnight), and `TestRepoTokens`.
  - The CLI text golden (`report.golden` in internal/stats, if present) must be **unchanged**.
- **View:**
  - `TestStatsTabsKeys`:
    - `tab` cycles five tabs and wraps;
    - `shift+tab` goes back;
    - `3` jumps to tokens;
    - `↑↓` does nothing on overview and moves the cursor on candidates;
    - `enter` on repos opens `:rounds` (the existing test helper for enter).
  - `TestStatsContextNoDollars`: the context row contains `tokens` and `cached`, and no `$`.
  - `TestStatsOverviewTiles`: a report with known TokenKinds. The overview contains the four labels and values, and the `N rounds measured` note.
  - `TestStatsOverviewNoTokens`: a report without token data shows `no token usage recorded`.
- **Goldens:**
  - Add `stats-overview-132` (132×34). Build a report with `stats.Build` over synthetic `db.RoundRow`s shaped like the user's data:
    3 candidates, deepseek-like with 99% cache and gemini-like with 88%, tokens on 4 of the last 7 days, 3 repos, and some rows
    with nil tokens.
  - Regenerate `stats-wide`, `stats-narrow` and `stats-empty`. Read each diff.
  - No fleet or round golden may change.

All tests are in internal/stats and internal/ui with fakes. No harness, network or cmd/relevo subcommand is involved.

## 6. Working efficiently

- Read internal/stats/stats.go (whole), view_stats.go (whole), view_stats_test.go and the stats part of golden_test.go in one step.
- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/stats/ ./internal/ui/ -count=1`.
  For goldens, add `-run Golden -update` (in internal/ui), then `git diff --stat internal/ui/testdata/ internal/stats/testdata/`.
- Final: `gofmt -l $(git ls-files '*.go')`, `go vet ./internal/stats/ ./internal/ui/ ./cmd/relevo/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/stats/ ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## 7. Steps

1. §2 engine and its tests. Verify: `go test ./internal/stats/` passes and the CLI text golden is unchanged.
2. §3.1 tabs and temporary tab bodies (S1, S2), and §3.4 keys. Verify: `TestStatsTabsKeys` passes.
3. §3.2 context row (S3).
4. §3.3 overview, its tests and the goldens. Then ports and the final checks.

## 8. Report

List:
- the files changed;
- every port with its S-number;
- the golden diffs;
- `stats-overview-132.golden` verbatim;
- the check results.
