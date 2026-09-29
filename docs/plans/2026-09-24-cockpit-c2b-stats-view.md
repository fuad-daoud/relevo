# Cockpit C2b: the `:stats` view

Spec: `docs/specs/2026-09-24-cockpit-design.md` §5 and §4.3 (the `:stats` row). Mockup:
the `:stats` board of https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA. This builds on
`cockpit/wave-1`: B1's shell (`internal/ui/view.go`, `shell.go`, `frame.go`,
`cmdline.go`) and C2a's `internal/stats`. Line numbers are as on that branch.

**One round. If a step is impossible as written or contradicts the code, stop and
report. Do not improvise.** CI has no harness and no network. The view is tested by
feeding it messages. No test opens a real terminal or runs `relevo ui`. A parallel
plan (B2) edits `internal/ui/cmdline.go`'s command table too, so keep your edit there
to the one table row plus its dispatch case.

## 1. System overview

`relevo history --stats` prints C2a's report as text. This round adds the same
report as a cockpit view, `:stats`, laid out as the mockup's panels:

- candidates scorecard;
- spend per day;
- reliability;
- repos and features;
- outcomes.

It also adds keys to change the window, move between panels, and open `:rounds`
filtered to a selected candidate or repo. The inputs are assembled in one place
shared by the CLI and the view, so the two cannot disagree.

## 2. File structure

```
internal/relevo/statsinputs.go      NEW  StatsInputs, LoadHistory, LegacyGatesPath (moved from cmd/relevo)
internal/relevo/statsinputs_test.go NEW
internal/stats/render.go            the text helpers become exported (mechanical rename, §4.2)
cmd/relevo/history.go               historyStats calls relevo.StatsInputs (output byte-identical)
cmd/relevo/main.go                  loadHistory / legacyGatesPath become thin calls to the relevo versions
internal/ui/view_stats.go           NEW  statsView
internal/ui/view_stats_test.go      NEW
internal/ui/cmdline.go              + the `stats [window]` command
internal/ui/golden_test.go          + 3 cases; testdata/stats-*.golden NEW
```

## 3. Data structures

```
// internal/relevo/statsinputs.go
// StatsInputs assembles stats.Inputs for the window starting at since (zero = all)
// ending at rt.Now(). rt.DB must be open. Read failures of latency and history are
// returned as warnings, never as errors.
func StatsInputs(rt Runtime, since time.Time) (in stats.Inputs, warnings []string, err error)
func LoadHistory(rt Runtime) (history.History, error)     // moved body of cmd loadHistory, error returned
func LegacyGatesPath(dir, name string) string            // moved body of cmd legacyGatesPath

// internal/ui/view_stats.go
type statsView struct {
    window     string         // "7d" | "30d" | "90d" | "all"
    rep        stats.Report
    loaded     bool
    err        error
    fetching   bool
    fetchedAt  time.Time
    focus      int            // 0 = candidates panel, 1 = repos panel
    cursor     [2]int         // selected row per focusable panel
    byProvider bool           // spend split
    top        int            // first body line shown in stacked layout
}
type statsMsg struct{ window string; rep stats.Report; err error; warnings []string }
```

## 4. Contracts

### 4.1 `StatsInputs`

- It is exactly the input assembly in `historyStats` (`cmd/relevo/history.go:410-458`),
  moved, with `rt.Now()` as `Until` and `time.Local` as `Loc`.
- `historyStats` becomes: parse the window as today, open the DB, `StatsInputs`, print
  each warning to stderr as `relevo: <warning>`, `stats.Build`, render.
- **Its output must be byte-identical.** `TestHistoryStatsNewReport` must pass
  unchanged.
- `loadHistory` and `legacyGatesPath` in `cmd/relevo/main.go` become one-line calls to
  the moved functions, so their other callers are untouched.

### 4.2 `internal/stats` exported helpers

Rename these, and nothing else changes: `money` → `Money`, `shortTokens` →
`ShortTokens`, `duration` → `Duration`, `sparkline` → `Sparkline`, `weekDelta` →
`WeekDelta`, `pctText` → `PctText`, `medianText` → `MedianText`, `ttftText` →
`TTFTText`, `costPerRoundText` → `CostPerRoundText`, `commitsText` → `CommitsText`,
`roundsPerLandText` → `RoundsPerLandText`, `fitKey` → `FitKey`, `stripScheme` →
`StripScheme`, `monthDay` → `MonthDay`, `untilText` → `UntilText`.

Use `gofmt -r 'money -> Money' -w internal/stats` one rename at a time, or `gopls
rename` if available. The golden `report.golden` must not change.

### 4.3 `statsView`

**Construction.** `newStatsView(env Env, window string) (View, tea.Cmd, error)`
- The error is `relevo.ErrNoDatabase` when `env.Src.Base().DB == nil`, as
  `newRoundsView` refuses. `serve ui` hits that and gets the notice.
- The `tea.Cmd` is the first fetch: an async `relevo.StatsInputs(env.Src.Base(),
  cutFor(window, env.Now))`, then `stats.Build`, returning a `statsMsg`.

**Identity.** `Crumbs()` is `{"stats"}`. `Capturing()` is false.

**Context.**
- Left: `N rounds · $X + N on plan · T tok · median M`, from `rep.Totals`, using the
  exported helpers.
- Right: `window 30d`, then ` · refreshing` while fetching, or the fetch error in
  `errorStyle`.

**Keys.**

| key | effect |
|---|---|
| `w` | Cycle the window 7d → 30d → 90d → all → 7d, and refetch. |
| `tab` | Toggle the focus between the candidates and repos panels. |
| `↑↓` / `j k` | Move the cursor in the focused panel. |
| `enter` | Open `:rounds` filtered to the selected row (below). |
| `p` | Toggle the spend split by provider. |
| `r` | Refetch. |
| `pgup` / `pgdn` | Scroll the stacked layout. |

**Enter.**
- **Candidates row:** push `newRoundsView(env, q, "")`, where `q` is
  `candidate:<token>`, plus ` since:<window>` unless the window is `all`. The token
  is `ScoreRow.Token`.
- **Repos row:** the query is `repo:"<key>"`, with the same since rule. The
  `(none)` row does nothing and shows a notice: `rounds with no repo cannot be
  filtered`.
- If `newRoundsView` errors, show a notice with the error.

**Refresh.** On `tickMsg`, refetch when `!fetching` and more than 30s have passed
since `fetchedAt`. On `statsMsg` for the current window, store the report, clear
`fetching`, and set `fetchedAt`. A `statsMsg` for another window is dropped.

**Body layout.**
- **Wide (`width >= 150`):** three bands, each a titled rule (`── candidates ──`
  style, as the round view's section rules) followed by its lines.
  1. Candidates on the left, 55% of the width; spend on the right.
  2. Reliability, full width.
  3. Repos (then features, if any) on the left; outcomes on the right.
  - Panels are joined line by line, and the shorter side is padded.
  - If the three bands are taller than `height`, band 3 is cut at the bottom. The
    report is not scrollable in wide mode.
- **Narrow:** the same panels stacked in the order candidates, spend, reliability,
  repos, features, outcomes, windowed by `top` with `pgup`/`pgdn`.
- **Candidates panel:**
  - Header: `NAME RNDS DONE HALT MED TTFT $/RND`, in faint bold.
  - One row per `ScoreRow`. The name is `env.Src.Base().Candidates.NameOf(token)`,
    fitted with `stats.FitKey`. `Few` rows use `faintStyle`.
  - The selected row, when the panel has focus, gets the fleet's gutter and
    `selectedBg`.
  - A last line gives the unrecorded count in faint text.
- **Spend panel:**
  - Four rows of bars, one column per day. When there are more days than columns,
    bucket consecutive days by summing, as few per column as needed.
  - Levels are `" ▁▂▃▄▅▆▇█"` over 4 rows (32 steps), scaled to the max, in
    `accentStyle`.
  - A left axis shows the max as `$X ┤` on the top row and `$0 ┼───` on the bottom.
  - Below: `MM-DD … MM-DD`, then `this week $X · last week $Y · ±N%`.
  - With `p`: instead of the bars, one line per provider with
    `stats.Sparkline`-style one-row bars and its total, sorted by total.
- **Reliability:** the text of C2a's reliability section. The by-hour digits use
  `errorStyle` when they are 2 or more, `stateNeedsYouStyle`'s colour for 1, and
  `faintStyle` for `.`.
- **Repos, features, outcomes:** as C2a's text, with repo keys through
  `stats.StripScheme` and `FitKey` (clipped from the left). The repos cursor as in
  the candidates panel.
- **States:**
  - Before the first report: the body is `loading…`.
  - A fetch error before any report: `renderError` of it.
  - A report with zero rounds: C2a's `no rounds in this window` line, centred.

**Styles.** Use only the existing `styles.go` styles. No new colour.

### 4.4 Command table (`internal/ui/cmdline.go:16`)

- Add `{"stats", "[7d|30d|90d|all]", "rounds, cost and health"}`.
- In `execute`: `stats` with no argument uses `30d`. Any other argument outside the
  four is a notice: `stats: want 7d, 30d, 90d or all`.
- Success is `root(v)` plus the init command. A `newStatsView` error is a notice:
  `no database: <err>`.

## 5. Pseudocode

```
Update(statsMsg m): if m.window != v.window: drop
                    v.rep, v.err, v.loaded, v.fetching, v.fetchedAt = ...
                    warnings -> notice(first warning) when non-empty
Update(key "w"):    v.window = next(v.window); v.fetching = true; return fetch(env, v.window)
Update(key enter):  row = selected(focus); q = queryFor(row, v.window); if q == "": notice
                    rv, init, err = newRoundsView(env, q, ""); err -> notice; else push(rv, init)
```

## 6. Error handling

| case | result |
|---|---|
| No DB | `:stats` refused with a notice; the stack is unchanged. |
| Fetch error | Shown in the context line and, before the first report, as the body. The last good report stays otherwise. |
| Warnings (latency or history unreadable) | The first one becomes a footer notice. |
| Every other message a view might get | Ignored, never a panic. |

## 7. Tests

### `internal/relevo/statsinputs_test.go`

`TestStatsInputsAssemblesFromDB`: a temp DB seeded like the neighbouring history
tests (reuse `openTestHistoryDB`, `internal/relevo/history_test.go:17-25`). Rows are
windowed, `Landed` comes from DONE bindings, and a missing latency KV gives a warning,
not an error.

### `internal/ui/view_stats_test.go`

Build a `stats.Report` fixture by hand; no DB is needed. Feed it as a `statsMsg`.

- `TestStatsWindowCycle`: `w` four times cycles the windows and returns a fetch each
  time; a stale-window `statsMsg` is dropped.
- `TestStatsEnterOpensFilteredRounds`: the candidates row gives a pushed rounds view
  whose `dash.QueryText()` is `candidate:<tok> since:30d`. The repos row gives
  `repo:"<key>" since:30d`. The `(none)` row gives a notice. Under `all` there is no
  `since:`. The rounds view needs a DB; use the golden harness's `db.Open` temp DB,
  `golden_test.go:174-194`.
- `TestStatsNoDatabase`: `:stats` against a source with a nil DB gives the notice, and
  the stack is unchanged.
- `TestStatsRefreshThrottle`: ticks within 30s do not refetch, and a tick after 30s
  does.
- `TestStatsFocusAndCursor`: `tab` and `j` move within bounds.

### Goldens

Same harness, same `-update` flag:
- `stats-wide`: 160x40, the fixture.
- `stats-narrow`: 100x30.
- `stats-empty`: zero rounds.

**Read each one** against §4.3 and paste `stats-wide` into the report.

### Unchanged

`TestHistoryStatsNewReport` and `internal/stats`' `report.golden` must pass unchanged.

### Mutation check

Make `enter` on the candidates panel omit the `since:` term. `TestStatsEnterOpensFilteredRounds`
must fail. Report it, then revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch the reads: `view.go`, `shell.go`, `frame.go`, `cmdline.go`, `view_rounds.go`,
  `view_fleet.go` (for the gutter and styles), `internal/stats/render.go`,
  `cmd/relevo/history.go:383-470` and `cmd/relevo/main.go` (`loadHistory`,
  `legacyGatesPath`).
- Write each new file in one call.
- Iterate on `go test ./internal/ui/... ./internal/relevo/ -run Stats ./internal/stats/... ./cmd/relevo/ -run History`.
- Run `make check` once at the end.

## 9. Ordered steps

**1. `StatsInputs` and the moves.**
- Deliverable: §4.1, the cmd one-liners, and `statsinputs_test.go`.
- Verify: `go test ./internal/relevo/ -run Stats` and
  `go test ./cmd/relevo/ -run History`. The output must be unchanged.

**2. Exported helpers.**
- Deliverable: §4.2.
- Verify: `go test ./internal/stats/...`.

**3. `view_stats.go` and the command.**
- Deliverable: §4.3, §4.4 and `view_stats_test.go`.
- Verify: `go test ./internal/ui/ -run Stats`.
- Depends on 1 and 2.

**4. Goldens.**
- Deliverable: the three cases, generated, read and pasted.
- Depends on 3.

**5. Check and report.**
- Run the mutation check, then `make check`.
- Report the functions with their line ranges, the pasted golden, and
  `git diff --stat`, which must touch only the §2 files.
