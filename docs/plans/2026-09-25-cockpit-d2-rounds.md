# Cockpit D2, `:rounds` round 1: the dashboard grid redesigned in D2

Date: 2026-09-25. Worktree: `ck-d2-rounds`, branch `relevo/ck-d2-rounds`, base `9471682` (origin/main).
This round makes ONE new commit. Do not rebase, push or open a PR.

**Stop rather than improvise.** If the code differs from what this plan quotes (a function missing, a
signature different, a line range holding something else), or a step is impossible as written, halt and
report what you found. A halt that surfaces a planning error is worth more than a green suite bent to fit.

**Scope fence.** Change only:
- `internal/ui/dash/render.go`, `internal/ui/dash/model.go`, `internal/ui/dash/sort.go`
- `internal/ui/dash/*_test.go` and `internal/ui/dash/testdata/*.golden`
- `internal/ui/view_rounds.go`, `internal/ui/view_stats.go` (only `shortRepo`, lines 1513-1524)
- `internal/ui/dash_host_test.go`, `internal/ui/testdata/rounds.golden`, and a stats golden only where a
  repo name changes because of `shortRepo`
- `docs/plans/`

Do NOT touch `internal/histq` (the `relevo history` CLI uses it and its output must not change) or `cmd/`.
No test in this round runs a CLI subcommand.

## 1. System overview

`:rounds` is the cockpit's round grid: `internal/ui/dash` draws it, and `internal/ui/view_rounds.go` hosts it
as the `roundsView` (always `Embedded = true`). It is still in the old style: dollars, full dates, raw
outcomes, no margins. This round redesigns it in D2, to the approved board "`:rounds` D2 · by day" (and
"· b regroup, by repo"):

```
  ◆ relevo    rounds                                                      v0.13.1-…    19:17
                                                                                              (blank)
   541 rounds   1.9B tokens   238 done   36 halted   2 blocked   1 exited   1 running   6m median          sort newest
                                                                                              (blank)
   STARTED  BINDING               RND   CANDIDATE              REPO                    OUTCOME     COMMITS  TREE    TOKENS   TOOK
   today ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈ 102 rounds · 415.2M
   19:14    oc-live               r1    gemini-3.8-flash-high  fuad-daoud/relay        running                    ·      ·     2m
   19:08    haiku                 r2    gemini-3.8-flash-high  sandbox/relevo-oc-test  done              1  clean     154k    <1m
   19:03    question              r1    gemini-3.8-flash-high  sandbox/relevo-oc-test  halted            0  dirty     138k     1m
   …
   yesterday ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈ 88 rounds · 610.0M
```

Grouped (`b`), e.g. by repo:

```
   9 repos   541 rounds                                                                               by  repo
   REPO                                               RNDS  BINDINGS   DONE  HALTED  COMMITS   TOKENS  % TOKENS              LAST
   fuad-daoud/relay                                    248       137    205      26      233     1.7B  ▇▇▇▇▇▇▇▇▇   90%      today
   fuad-daoud/money                                     25         6      0       0       12   106.5M  ▇            6%     sep 18
   …
   (no repo)                                           142        83      0       0       33    18.2M               1%     sep 18
```

The user's design rules, which every step below applies:
- No dollars anywhere. The view is tokens-first.
- A 3-cell margin on each side. Nothing is drawn past `width-3`, and the tables fill `[3, width-3)`. The name
  column (BINDING, or the group key) takes the rest, and the other columns keep fixed widths.
- No captions or titles above tables. The header row names the columns.
- A "none" bucket closes a grouped table: `(no repo)`, `(no feature)`, or `(none)`.
- Bars are the number: 10 cells = 100%.

## 2. File structure

```
internal/ui/dash/
  render.go      REWRITTEN in place: D2 columns, day rules, group table, summary line, ShortRepo, outcome words
  model.go       Styles gains D2 fields; Model gains Running; visible()/cursor skip day rules; View per mode;
                 Summary(); Problem(); space pages down
  sort.go        cost keys replaced by tokens; the none group sorts last
  render_test.go goldens regenerated; new d2 tests (§7 step 5)
  model_test.go  TestSortCyclesAndFlips ported (cost -> tokens)
  testdata/*.golden  regenerated
internal/ui/
  view_rounds.go dashStyles fills the new Styles; Context/Keys/Body redesigned; runningRounds helper
  view_stats.go  shortRepo delegates to dash.ShortRepo (lines 1513-1524)
  dash_host_test.go  host tests (§7 step 5)
  testdata/rounds.golden  regenerated
docs/plans/2026-09-25-cockpit-d2-rounds.md   this plan, copied in the last step
```

## 3. Data structures & type definitions

### 3.1 `dash.Styles` (model.go:89-99): add fields, keep the existing ones

| field | type | host value (`internal/ui/styles.go`) | used for |
|---|---|---|---|
| `Strong` | `lipgloss.Style` | `textStyle.Bold(true)` | summary numbers; the cursor row's name cell |
| `Accent` | `lipgloss.Style` | `accentStyle` | `running`; the `% TOKENS` bar |
| `Warn` | `lipgloss.Style` | `warnStyle` | `halted`, `blocked`, `switched`; `dirty` |
| `Ok` | `lipgloss.Style` | `greenStyle` | `done` |
| `Danger` | `lipgloss.Style` | `redStyle` | `exited` |
| `Grid` | `lipgloss.Style` | `gridStyle` | the `┈` run of a day rule |
| `Chip` | `lipgloss.Style` | `chipAccentStyle` | the axis chip on the context row |

`Selected` stays and becomes the row band (host value `selBandStyle`, already `selectedBg`).

### 3.2 `dash.Model` (model.go:23-87): add one field

```
// Running reports whether binding's round n is running right now. The host
// sets it from its live status; nil means no round is running.
Running func(binding string, round int) bool
```

### 3.3 `lineKind` / `line` (render.go:47-60)

- Add `lineDay` to the `lineKind` enum.
- Add `day dayRule` to `line`.
- `type dayRule struct { label string; rounds int; tokens int64 }`. `label` is `today`, `yesterday`, or the
  date formatted `"Mon 02 Jan"` and lower-cased (`sat 19 sep`), all in `m.loc` against `m.now()`.

### 3.4 `dash.Summary` (new, exported, render.go)

```
type Summary struct {
    Rounds   int
    Tokens   int64
    ByWord   map[string]int // outcome word (§3.5) -> rounds
    MedianMS int64          // median of rows with DurationMS; 0 when none
}
```

### 3.5 Outcome words (new, render.go): `func (m Model) outcomeWord(r db.RoundRow) (string, lipgloss.Style)`

| `r.Outcome` | `r.ReportOutcome` | word | style |
|---|---|---|---|
| `open` | any | `running` when `m.Running != nil && m.Running(r.BindingName, r.Number)`, else `open` | Accent / Dim |
| `exited` | any | `exited` | Danger |
| `halted` | any | `halted` | Warn |
| `switched` | any | `switched` | Warn |
| `done_no_report` | any | `no report` | Faint |
| `reported` | `done` | `done` | Ok |
| `reported` | `halted` | `halted` | Warn |
| `reported` | `blocked` | `blocked` | Warn |
| `reported` | anything else, or nil | `no outcome` | Faint |

## 4. Interface definitions & component contracts

### 4.1 dash (all in `internal/ui/dash`)

- `func ShortRepo(key string) string` (exported). Rule, in order:
  1. Trim trailing `/`.
  2. Trim a suffix `/.git`, then a suffix `.git`.
  3. If the key contains `@` and a `:` after it (the scp form `git@host:owner/name`), keep only what follows
     that `:`.
  4. Return the last two `/`-separated segments, or the key itself when there are fewer than two.

  Examples:
  - `https://github.com/fuad-daoud/relay` → `fuad-daoud/relay`
  - `git@github.com:x/persist.git` → `x/persist`
  - `/home/fuad/sandbox/relevo-oc-test/.git` → `sandbox/relevo-oc-test`
  - `relay` → `relay`
- `func (m Model) Summary() Summary`: over `m.rows` (the whole query result, unsorted). Pure.
- `func (m Model) SummaryLine() string`: the styled summary items (§5.4), joined by 3 spaces, with no margin
  and no padding.
- `func (m Model) Problem() string`: `m.Notice` if set, else `"query failed: " + m.fetchErr` if set, else `""`.
- `func (m Model) FilterText() string`: `stripBy(m.text)`.
- `func (m Model) SortLabel() string`: the round sort key when ungrouped, the group sort key when grouped.
  `started` reads `newest` when descending and `oldest` when ascending. Any other key reads `<key> ↓`
  (descending) or `<key> ↑` (ascending).
- `outcomeWord` (§3.5), `dayLabel(t time.Time) string`, `groupKeyLabel(key string) string` (§5.3), and
  `tokensBar(pct int) string` are unexported helpers.
- `shortTokens` (render.go:419-430) becomes a call to `stats.ShortTokens` (`internal/stats/render.go:271`),
  which gives `1.9B` above 1e9 and strips a trailing `.0`. If importing `internal/stats` from dash makes an
  import cycle, halt and report; do not copy the rule.

Preconditions: none (nil maps and slices tolerated; zero rows give zero counts). Postconditions: no rendered
line is wider than `m.width`, and no rendered text contains `$`.

### 4.2 host (`internal/ui/view_rounds.go`)

- `func runningRounds(env Env) func(string, int) bool`. It builds `map[name]round` from
  `env.Report.Bindings` where `groupOf(b) == groupWorking` (`fleet_group.go:23`), and returns
  `func(name, n) bool { r, ok := m[name]; return ok && r == n }`.
- `roundsView.Context(env)` (lines 61-73), `Keys()` (75-84) and `Body(env,w,h)` (128-132) are redesigned
  (§5.5). `newRoundsView` (40-51) also sets `d.Running = runningRounds(env)`.
- `dashStyles()` (24-36) fills the §3.1 fields.

`internal/ui/view_stats.go:1515 shortRepo(key)`: keep the `"(none)"` → `"(no repo)"` branch, then
`return dash.ShortRepo(key)`. `view_stats.go` does not import dash yet: add
`"github.com/fuad-daoud/relevo/internal/ui/dash"` (no cycle: dash never imports `internal/ui`, and dash already
depends on `internal/stats` transitively).

## 5. High-level pseudocode

### 5.1 Layout: every rendered line = 3 spaces + content (`cw = width-6` cells) + 3 spaces

```
View():
  grid lines = gridContent() windowed as today (vp)
  if Embedded:  body = thirdLine() + "\n" + grid          // height-1 grid rows
  else:         body = headerLine() + "\n" + margin(SummaryLine or Problem in Error) + "\n" + thirdLine() + "\n" + grid
gridHeight(): height - (1 if Embedded else 3)              // replaces dashHeaderRows usage
thirdLine(): editing -> "   / " + input (+ parse error, as today, clipped to cw)
             grouped -> groupHeader()   else -> roundHeader()
```

Header rows: labels UPPERCASE, `Faint.Bold(true)`, right-aligned over right-aligned (numeric) columns.

### 5.2 Round columns (replace `roundColumns`, render.go:138-166)

In order, with widths and alignment:

| column | width | align | notes |
|---|---|---|---|
| STARTED | 7 | l | |
| BINDING | rest | l | minimum 12 |
| RND | 4 | l | |
| CANDIDATE | 21 | l | |
| REPO | 22 | l | |
| OUTCOME | 10 | l | |
| COMMITS | 7 | r | |
| TREE | 5 | l | |
| TOKENS | 7 | r | |
| TOOK | 5 | r | |

Two cells separate each pair of columns. When BINDING would fall under 12, drop columns in this order until
it fits: first REPO, then TREE and COMMITS together, then TOKENS. An expanded group's rounds are indented 2
cells, and BINDING gives up those 2 cells.

Cells:

| column | text | style |
|---|---|---|
| STARTED | `StartedAt.In(loc).Format("15:04")` | Dim |
| BINDING | the binding name | Fg; Strong on the cursor row |
| RND | `r<n>` | Dim |
| CANDIDATE | `nameOf(BuilderCandidate)`, `·` when nil | Dim |
| REPO | `ShortRepo(*Repo)`, `(no repo)` when nil | Dim |
| OUTCOME | the §3.5 word | the §3.5 style |
| COMMITS | `n`; `·` when nil | Fg; Faint when 0 or nil |
| TREE | `*Tree`; `·` when nil | Warn when `dirty`, else Faint |
| TOKENS | `shortTokens(sum)`; `·` when all four token columns are nil | Fg |
| TOOK | `shortDuration(DurationMS)`; see below | Dim |

TOOK for a `running` round is `shortDuration(now-StartedAt)`; for any other round with nil DurationMS it
is `·`.

Cursor row: the content (padded to `cw`) is wrapped in `Selected` (the band). The margins stay unstyled.
Archived rows keep `Archived`.

### 5.3 Group table (replace `groupLine`/`groupHeader`, render.go:191-218 and 337-353)

Columns, in order:

| column | width | align | notes |
|---|---|---|---|
| KEY | rest | l | header is the axis name, uppercased; `builder` reads CANDIDATE |
| RNDS | 5 | r | |
| BINDINGS | 8 | r | omitted when the axis is `binding` |
| DONE | 5 | r | |
| HALTED | 6 | r | |
| COMMITS | 7 | r | |
| TOKENS | 7 | r | |
| % TOKENS | 15 | l | |
| LAST | 9 | r | |

Below 110 columns drop `% TOKENS`. Below 90 columns also drop BINDINGS and COMMITS.

```
KEY text   = groupKeyLabel(g.Key):
             "-" -> "(no repo)" (axis repo) | "(no feature)" (axis feature) | "(none)" (other axes)
             axis repo -> ShortRepo(key); axis builder -> nameOf(key); else key
DONE       = count of g.Rows whose outcomeWord is "done"; HALTED likewise "halted"
BINDINGS   = distinct BindingName in g.Rows
% TOKENS   = pct := round(100*g.Tokens/total)  (total = sum over all groups; 0 -> pct 0)
             tokensBar(pct) = strings.Repeat("▇", round(pct/10)) padded to 10, in Accent;
             then " " + fmt("%3d%%", pct) in Fg
LAST       = "today" when g.Last is today in loc, else g.Last.Format("Jan 02") lower-cased ("sep 18")
key cell Fg (Strong on the cursor row); numbers Dim; no ▸/▾ marker
expanded group: its rounds follow it as round lines (§5.2), indented 2
```

### 5.4 Summary line (`SummaryLine`)

- Items appear in this order: `<Rounds> rounds`, `<shortTokens(Tokens)> tokens`, `<done> done`, then `halted`,
  `blocked`, `exited`, `switched`, `running` and `open` each only when their count is above 0. Last comes
  `<shortDuration(MedianMS)> median`, only when MedianMS > 0.
- The number is Strong, a space, then the label in Dim.
- Items are joined by 3 spaces.

### 5.5 Host

```
Context(env):
  r.dash.Running = runningRounds(env)
  left  = "   " + (Error(problem) if r.dash.Problem() != "" else r.dash.SummaryLine())
  right = ""
  if f := r.dash.FilterText(); f != "": right += Dim("/ " + clip(f, 40)) + "   "
  if axis := r.dash.GroupAxis(); axis != "": right += Faint("by ") + Chip(" " + axisLabel(axis) + " ")
                                              (axisLabel: "builder" -> "candidate", else axis)
  else:                                       right += Faint("sort ") + Dim(r.dash.SortLabel())
  right += "   "
Keys(): ↑↓ move · enter "open round" (ungrouped) | "expand" (grouped) · / filter · b regroup · s sort · S flip · r refresh
Body(env, w, h): r.dash.Running = runningRounds(env); SetSize(w, h); View()
```

`Keys()` needs the grouped state; it has a value receiver, so read `r.dash.GroupAxis()`.

### 5.6 Day rules and the cursor (model.go / render.go)

```
visible():
  ungrouped:
    rows := sortedRows(m.rows)
    if roundSortKey() == "started":
      for each run of consecutive rows with the same StartedAt date in loc:
        emit line{kind: lineDay, day: {label, rounds: len(run), tokens: sum}} then the run's round lines
    else: round lines only
  grouped: as today (group lines; an expanded group's rows beneath). No day rules.

day rule render (exactly cw wide, then margins):
  Dim(label) + " " + Grid("┈" * fill) + Faint(" " + plural(n, "round") + " · " + shortTokens(tokens))
  plural: "1 round", "2 rounds"

cursor:
  selectable(i) = visible()[i].kind != lineDay
  moveCursorTo(c) takes a direction dir (+1 down/pgdown/end-from-top, -1 up/pgup):
    clamp c to [0, n-1]; while !selectable(c) step c += dir;
    if it runs off an end, step the other way from the clamped c; no selectable line -> cursor 0
  home = first selectable; end = last selectable
  RowsMsg / clampCursor / b / New: snap the cursor to a selectable line (dir +1)
  "enter" on a lineDay: no-op (unreachable, but safe)
updateKeys: add case " " (space) = pgdown
```

### 5.7 Sorting (sort.go)

- `roundSortKeys = {"started", "tokens", "duration", "commits"}`.
- `groupSortKeys = {"tokens", "rounds", "halted", "last"}`; the group default becomes `tokens`.
- `groupLess` gains `tokens` (by `g.Tokens`).
- The `-` (none) group sorts last whatever the key and direction: `sortedGroups` partitions it to the end
  after sorting.
- A stored `"cost"` preference is simply not a key any more, and falls back to the default. That already
  happens through `isRoundSortKey` and `isGroupSortKey`.

## 6. Error handling strategy

No new errors. A fetch failure or a host notice replaces the summary on the context row (left, Error style)
and the last good rows stay on the grid, exactly as today's tiles line did. The editor's parse error stays
inline beside the `/` input. Nothing panics on nil pointers: every nullable column has its `·` fallback.

## 7. Working efficiently

A round costs one round trip per step, so:
- Batch independent reads, searches and edits as parallel tool calls in one step.
- Read the files this plan names once, at the line ranges given. Do not re-search for them.
- Make every change to a file in one edit call. `render.go` is mostly rewritten: write it whole in one Write,
  keeping the helpers this plan does not delete (`stripBy`, `deref`, `pad`, `clip`, `fit`, `spread`,
  `nameOf`, `shortDuration`).
- Iterate with the focused command, fixing every reported error before the next run:
  `mkdir -p $HOME/.cache/relevo-verify/tmp && env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/dash/ ./internal/ui/ -count=1`
- Regenerate goldens with the same command plus `-run TestGoldenViews -update`. Then read every changed
  golden and check it against §1's boards before you accept it.
- Run the full check once at the end (step 7).
- Do NOT run `make check`: it is refused on this machine. The planner runs it on the server.

## 8. Deletions (closed list; everything else survives)

1. `costCell`, `groupCostCell`, `colCost` and every `cost` column, header and tiles text (render.go:388-409 and
   its uses). `costValue` in sort.go:119-126 and the `cost` case of `roundLess` and `groupLess`.
2. The `gate` column: `gateCell` (render.go:377), `colGate`, `tierGate`.
3. The old tier constants and widths (render.go:18-44), `builderWidth`, `tailWidth` (220-254), replaced by §5.2.
4. The `▸`/`▾` marker in `groupLine`.
5. `tilesLine`'s text "rounds … cost … bindings … builders" (render.go:283-304), replaced by `SummaryLine`/`Problem`.
6. The old full-date `2006-01-02 15:04` started cell and `+N` commits cell (`commitsCell`, render.go:367-373).
7. `roundsView.Context`'s `"all rounds"` / `"query: …"` left text and `"by <axis>"` right text (view_rounds.go:61-73).
8. `dashHeaderRows` (render.go:16), replaced by the per-mode count in `gridHeight`.

A test that asserted surviving behaviour through a deleted mechanism is PORTED, not deleted. For example,
`TestSortCyclesAndFlips` cycles `tokens` where it cycled `cost`, and asserts the tokens order: api's 2.0M
first when descending. Any test you remove must cite one of the numbers above in the report.

## 9. Ordered implementation steps

1. **Styles and model fields.** Add the §3.1 fields to `dash.Styles`, `Running` to `Model`, and fill them in
   `dashStyles()`. *Verify:* `go build ./internal/ui/...`.
2. **render.go rewrite** (§3.3-§3.5, §4.1, §5.1-§5.4, §5.6's render part): `ShortRepo`, `outcomeWord`,
   `Summary`, `SummaryLine`, `Problem`, `FilterText`, `SortLabel`, the round and group columns, the day rules,
   and the margins. Delete §8 items 1-6 and 8. *Verify:* the package builds.
3. **model.go and sort.go** (§5.6 cursor, the space key, `View` and `gridHeight` per mode; §5.7). *Verify:* the
   focused command compiles. Old goldens are expected to fail at this point.
4. **Host** (§4.2, §5.5): `runningRounds`, `Context`, `Keys`, `Body`, `newRoundsView`; `shortRepo` in
   view_stats.go. Delete §8 item 7.
5. **Tests.** In `internal/ui/dash` (put the new ones in `internal/ui/dash/d2_test.go`):
   - `TestShortRepo`: the four §4.1 examples.
   - `TestOutcomeWord`: every row of §3.5, `running` and `open` included.
   - `TestDayRules`: with `testRows()` and `testNow` (Mon 2026-09-21 UTC), the ungrouped View has the rule
     lines `yesterday`, `sat 19 sep`, `fri 18 sep`, in that order. Each ends with `1 round · <tokens>` exactly
     3 cells before the line's end.
   - `TestNoDayRulesUnlessSortedByStarted`: after one `s`, the View has no `┈`.
   - `TestCursorSkipsDayRules`: after the fetch the cursor is on a round line. `down` walks r5 → r4 → r2 and
     never rests on a `lineDay`, and `up` and `home` do likewise. `end` lands on r2.
   - `TestNoDollars`: flat, grouped and expanded Views contain no `$`.
   - `TestMarginsAndWidth`: at 132×34 and at 100×30, every View line has width ≤ width. Every non-empty line
     starts with 3 spaces, and its last 3 cells are spaces or the line ends before `width-3`.
   - `TestColumnsDropByWidth`: the round header has REPO at 132, has no REPO but has TREE at 110, and has
     neither TREE nor COMMITS at 90.
   - `TestGroupNoneLast`: add a row with nil `Repo` and the most tokens. With `by:repo`, the last group line
     starts with `(no repo)` in both sort directions.
   - `TestGroupTokensBar`: with `by:binding` on rows of one binding, that line contains
     `▇▇▇▇▇▇▇▇▇▇ 100%`.
   - `TestRunningAndSummary`: an open row with `Running` returning true reads `running`, and its TOOK is
     now-start. With `Running` nil it reads `open`. `Summary().ByWord` counts `done`/`halted` as §3.5 says, and
     `SummaryLine` (ANSI stripped) starts with `4 rounds`.
   - Port `TestSortCyclesAndFlips` (§8).

   In `internal/ui/dash_host_test.go`, driving `roundsView` through `goldenRoundsModel` as the existing tests
   do:
   - `TestRoundsContextSummary`: the Context left has `3 rounds` and no `$`, and the right has `sort newest`.
     After `b`, the right has `by` and `binding`.
   - `TestRoundsRunningFromReport`: a report binding `persist` in the working group on round 5, with that row
     open, makes the body show `running`.

   Regenerate the goldens (dash and `rounds.golden`), and read each one against §1. *Verify:* the focused
   command is green.
6. **Mutation checks** (report both results, and check each mutated build compiles):
   1. In `moveCursorTo`, remove the step that skips `lineDay`. `TestCursorSkipsDayRules` must fail.
   2. In `sortedGroups`, remove the partition that puts the none group last. `TestGroupNoneLast` must fail.

   Restore both afterwards.
7. **Full check**, once:
   `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/... ./internal/stats/ ./internal/histq/ ./cmd/relevo/ -count=1 && go vet ./internal/ui/... && test -z "$(gofmt -l $(git ls-files '*.go'))" && sh scripts/check-name.sh`
   `check-name.sh` bans the old project name `relay` in tracked files, test fixtures included. Real repo URLs
   in fixtures must not contain it. Use `x/persist`-style names.
8. **Commit.** Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-rounds.md` into
   `docs/plans/`. Then run `git add -A && git commit -m "feat(cockpit): :rounds in D2 -- day rules, outcome words, tokens-first group table"`.

## 10. Report

Include:
- each step's result;
- the list of goldens that changed, and one line on each;
- the mutation results, with the build line of each mutated build;
- any test removed, citing §8;
- `git diff --stat 9471682 HEAD`;
- the ANSI-stripped `rounds.golden` in full.
