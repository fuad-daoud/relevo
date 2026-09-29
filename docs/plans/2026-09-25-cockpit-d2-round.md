# Cockpit D2: round detail as card + tabs

Date: 2026-09-25. Base: branch `relevo/ck-d2-fleet` at 9c7eff44 (PR #449, the D2 theme
and grouped fleet; not yet merged). Line numbers below are exact at that commit. One round.
Design: canvas board **round detail D2 · card + tabs**
(https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA).

**Stop rather than improvise.** If a step is impossible as written, if the code contradicts
a fact stated here, or if a test outside the ported list (§7) fails, halt and report.
Do not bend a test to make it pass. Change only `internal/ui`.

## 1. System overview

The round view (`enter` on a fleet row, or `:round <name>`) still draws the pre-cockpit
pane: a five-row fact block (planner/builder/tree/usage/spend), a tab bar with an
underline rule, and raw text. On the user's screen the name and round appear three times,
the candidate prints as a raw token in literal backticks, and markdown plans show their
`#`, `**` and backticks. This round rebuilds the pane's furniture in the D2 language
the fleet now uses:

- A **context row** says what the binding is: actor on candidate, planner and tree.
- A **card** (the fleet's card, shared) says what the round is doing now: a state pill,
  age, tokens and the keys that apply.
- **Pill tabs** are followed by a source line and the content, indented, with plan and report
  rendered as light markdown.

Everything behind the pane stays as it is: fetching, caching, invalidation, `[`/`]` stepping,
follow mode, the hint line and pull.

## 2. Design

### 2.1 Reference render (132×34; spool-db, round 1, working, plan tab)

```
  ◆ relevo    fleet › spool-db ›  r1                                                       ● 1 needs you      v0.13.0-28     23:31
   builder on gemini-3.8-flash-high  ·  planner architect-2  ·  relevo/spool-db                                  round 1 of 1 · live
 ╭─ spool-db  round 1 ───────────────────────────────────────────────────────────────────────────────────────────────────── live ─╮
 │  working    5m  ·  quiet 17s  ·  pid 1401366 since 23:26                                                                      │
 │  tokens  live · gemini-3.8-flash-high · 5m · in 718k · cache 4.2M (85%) · out 64k · unknown: no price                         │
 │                                                                                                                                │
 │    x  stop       g  gate       o  shell       r  retry on…                                                                    │
 ╰────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯

    plan     report     transcript     diff     log                                                     round   [   r1 of 1   ]

     plan r1 · 23:26

     Round diff and consult findings go straight into round_file

     Date: 2026-09-24. Base: origin/main b66c6fcc (#441).
     There is one round in this plan. builder.log, NNN-<id>-ask.md and NNN-plan.md
     are out of scope. Do not touch them.
     …
 tab  next tab      [ ]  round      ↑↓  scroll      s  send      x  stop      :  command      ?  all keys      esc  back
```

Spacing may differ where the rules below say otherwise. The rules win.

### 2.2 Context row (`roundView.Context`)

- **Left, for a live row** (`b := row(report, name)`, not nil): three spaces, then:
  - `actorCell(b)` in text, faint ` on `, `candidateText(b)` in text;
  - faint `  ·  `, then `planner <plannerCell(b)>` in muted. When `b.OwnerLabel != ""` (serve ui), it is `client <OwnerLabel>` instead;
  - faint `  ·  `, then the branch, or `repoCell(b)` when there is no branch, in muted;
  - when `b.Dirty`: faint `  ·  `, then `dirty` in red.
- **Left, for a hist (archived) row, or when b is nil:** three spaces and `detailHeader()` in muted.
- **Right:** `round N of M` faint, plus ` · live` or ` · archived <date>` by the same rules as
  `detailHeader()` (round_pane.go:292-304), then one space.

### 2.3 The card (top of the body; shared renderer)

New `internal/ui/card.go`:
```
// renderCard draws a rounded card one column in from the left edge, cardW = width-2 wide:
// top "╭─ " + title + " " + dashes + (right != "" ? " " + right + " ─" : "") + "╮",
// then one "│" + row + pad + "│" line per row, then "╰─…─╯". Every line fit() to width.
// title and right are pre-styled; each row is pre-styled content, padded to cardW-2.
func renderCard(width int, title, right string, rows []string) []string   // len(rows)+2 lines
func cardKeysRow(keys []KeyHelp) string   // "   " + chip(kbd,key)+" "+muted(label) joined by 6 spaces
```
The border is `borderStyle`. **The fleet's `cardLines` (view_fleet.go:682-805) is refactored to build
its rows and call `renderCard(width, title, "", rows)`. Its output must stay byte-identical:
no fleet golden may change.** If one does, halt.

**The round card** is `roundPane.cardLines(b *relevo.BindingStatus, env Env) []string`, and always
returns exactly `roundCardRows = 6` lines:
- **Title:** `b.Key()` (accent, bold) + two spaces + `round N` (faint, the round on screen).
  Right: `live` when `detail.live` and the round on screen is the open round (the same rule as
  detailHeader), `archived YYYY-MM-DD` when archivedAt is set, else `closed`. All faint.
- **Row 1, state:** two spaces, then the state pill `chip(style, word)`:

  | groupOf(b) | style | word |
  |---|---|---|
  | needs you | chipWarn, bold | `needs you` |
  | working | chipGreen, bold | `working` |
  | idle | kbd | `idle` |
  | on hold | kbd | `on hold` |
  | other | kbd | lowercased `b.Display` |
  | done | kbd | `done` |

  Then 3 spaces and the age in text: `rowNow(b, now)` with its `rN · ` prefix removed. Then muted parts,
  each preceded by faint `  ·  `:
  - `pid N since HH:MM` when `Headless != nil && PID != 0`;
  - `switched Nx` when `Switches > 0`;
  - `N consults` when `Consults > 0`;
  - `b.Stall` in red, and `b.Exploring` in muted, when non-empty.
- **Row 2, tokens:** two spaces, faint `tokens  `, then:
  - `usage.LiveParts(*b.LiveUsage)` when LiveUsage != nil: the first part in accent, the rest muted, joined by faint ` · `;
  - else `usage.Parts(*b.LastUsage)` all muted;
  - else faint `no usage yet`.

  Then, when `b.LastClose != nil && Commits > 0`: faint `  ·  `, then muted `+1 commit` / `+N commits`.
  Then, when `spendCell(b) != ""`: faint `  ·  `, then muted `spend <spendCell>`.
- **Row 3:** blank.
- **Row 4, keys:** `cardKeysRow` of the group's keys when `env.Actions != nil`:

  | group | keys |
  |---|---|
  | needs you | `s` send the next plan, `o` shell, `x` stop |
  | working | `x` stop, `g` gate, `o` shell, `r` retry on… |
  | idle | `s` send the next plan, `D` done, `o` shell |
  | on hold, other | `s` send, `D` done |
  | done | `u` unbind |

  With `Actions == nil`: two spaces and faint `read-only`.
- **For a hist row or `b == nil`:** title as above, with right `archived <date>` or `closed`.
  Row 1 is `chip(kbd,"archived")` (or `chip(kbd,"closed")`) + faint `   round N of M`. Row 2 is faint
  `no live facts for a released binding`. Row 3 is blank. Row 4 is `cardKeysRow` of `[ ] step rounds`.

### 2.4 Tabs row

Two spaces, then each tab label in `tabTitles` order, separated by three spaces:
- the active tab is `chip(chipAccentStyle, label)` (new style: background accent `#6ea8fe`, foreground
  `#0f1115`, bold, in styles.go);
- the others are `chip(normalStyle, label)` in muted (same width, so the row doesn't shift).

Right side, via `spread`: faint `round  `, then `chip(kbd,"[")`, then text bold `  rN of M  `, then `chip(kbd,"]")`, then two spaces.
There is **no rule line** under the tabs.

`tabTitles` becomes `{"plan", "report", "transcript", "diff", "log"}`. The label "terminal" becomes
"transcript". The enum and order are unchanged, so `1`-`5` keep their meaning.

### 2.5 Body order and heights (`roundPane.view`, `viewportHeight`)

1. The card: 6 lines, **only when rows >= 18**; otherwise nothing.
2. A blank line, only with the card.
3. The tabs row.
4. A blank line.
5. The source line, indented 5 spaces (`sourceLine()` unchanged).
6. A blank line.
7. The viewport, each line prefixed with 5 spaces, then the hint line when it applies (unchanged rule).

New method `(p roundPane) headRows() int`: (card ? 7 : 0) + 4. `viewportHeight()` returns
`rows - headRows()`, floored at 0, and `view()` uses the same method, so they cannot drift.

New `(p roundPane) contentWidth() int`: `max(width-6, 20)`. It is used for `vp.Width` everywhere
the pane sets it (view_round.go:196, round_pane.go `fillViewport`'s wrap, and `view`), so wrapped
lines fit inside the 5-space indent.

### 2.6 Markdown (plan and report tabs only)

New `internal/ui/markdown.go`: `func renderMarkdown(body string) string`, pure and line-based.
`bodyOf` (detail.go:60-83) calls it for `tabPlan` and `tabReport` when the content is loaded and has
no err or empty. Every other tab is unchanged.

| input line | output |
|---|---|
| a line starting with ` ``` ` | toggles fence mode; the fence line itself renders faint |
| any line inside a fence | muted, verbatim (no inline rules) |
| `# x` or `## x` | `x` in accent, bold |
| `### x` and deeper | `x` in text, bold |
| a line whose trimmed form starts with `- ` or `* ` | the same indent, faint `•`, a space, the rest with inline rules |
| a line starting with `\|` (a table) | muted, verbatim |
| anything else | text, with inline rules |

Inline rules, outside fences: `` `x` `` becomes `x` in accent (backticks removed), and `**x**` becomes `x` in text, bold. An
unmatched backtick or `**` is left as it is. Wrapping stays `wrapBody`'s job after rendering, and
is ANSI-aware.

### 2.7 Keys

- `roundView.Keys()`: `tab next tab`, `[ ] round`, `↑↓ scroll`, and with actions also `s send`, `x stop`.
- New `roundView.HelpKeys()`: `tab next tab`, `1-5 tab`, `[ ] round`, `↑↓ scroll`, and with actions also
  `s send`, `E edit+send`, `x stop`, `D done`, `u unbind`, `g gate`, `o shell`, `r retry on…`.

Key handling (`updateKey`) is unchanged.

## 3. File structure

```
internal/ui/card.go          NEW renderCard, cardKeysRow
internal/ui/markdown.go      NEW renderMarkdown
internal/ui/styles.go        + chipAccentStyle
internal/ui/view_fleet.go    cardLines uses renderCard (output identical)
internal/ui/round_pane.go    cardLines, headRows, contentWidth, tabBar -> tabsRow, view; paneHead/histPaneHead deleted
internal/ui/view_round.go    Context, Keys, HelpKeys, vp.Width via contentWidth
internal/ui/detail.go        bodyOf calls renderMarkdown for plan/report
internal/ui/fetch.go         tabTitles label "transcript"
internal/ui/*_test.go        ports (§7), new tests (§8); goldens regenerated + new
```

## 4. Contracts (signatures)

```
func renderCard(width int, title, right string, rows []string) []string
func cardKeysRow(keys []KeyHelp) string
func renderMarkdown(body string) string
func (p roundPane) cardLines(b *relevo.BindingStatus, actions bool) []string   // exactly 6
func (p roundPane) tabsRow() string
func (p roundPane) headRows() int
func (p roundPane) contentWidth() int
const roundCardRows = 6
```

`roundPane` does not hold `Env`. Pass `actions bool` from `roundView` (its `actions` field)
through a new `roundPane.actions bool` set in `newRoundView`/`newHistRoundView`, next to how
`roundView.actions` is set today.

## 5. Pseudocode: view(width)

```
b := row(report, name)
out := []
if rows >= 18: out += cardLines(b, actions) ; out += ""
out += tabsRow() ; out += ""
out += "     " + sourceLine() ; out += ""
vpRows := rows - len(out) ; hint rule as today (steals the last row)
vp.Width = contentWidth(); vp.Height = vpRows
out += "     " + line for each vp line ; + hint
pad to rows, cut to rows, fit each to width
```
`len(out)` before the viewport must equal `headRows()`. Add an assertion-style test (§8).

## 6. Errors

No new error paths. The markdown renderer never fails: malformed input passes through.
A nil row renders the hist card.

## 7. Deletions (closed list) and ports

- **R1** `paneHead` and `histPaneHead` (round_pane.go:307-414), and the consts `paneHeadRows`, `tabRows`,
  `sourceRows` (round_pane.go:19-23). Their facts move to the context row and the card. **Facts
  dropped from the view:** the planner's kind and route (`claude route pull`), the last-event line
  (`plan r1 · 5m ago`, which the source line covers), the builder kind word (`agy`), and the last close's tree hash.
  `relevo status` still shows them all.
- **R2** `tabBar`'s underline rule (round_pane.go:416-437). It is replaced by `tabsRow`.
- **R3** The raw candidate token in backticks, and the literal `lipgloss.Color("255")` (round_pane.go:312,
  the `fgStyle.Render("`"+b.BuilderCandidate+"`")` part).
- **R4** The tab label `terminal`. It becomes `transcript`.

Everything else survives. **Ports** (change the assertion; cite R-number in the report):
- `pane_test.go:37-39` (paneHead row count, R1): assert `len(cardLines) == roundCardRows`.
- `pane_test.go:66` (`last close r1: 1 commit, dirty`, R1): assert the card has `+1 commit` and the context row has `dirty`.
- `pane_test.go:78-90` (paneHead/tabBar, R1/R2): assert the equivalent facts on the card and the tabs row.
- `split_test.go:308-377` (the client line in paneHead, R1): assert `client <label>` in `roundView.Context` for a row with OwnerLabel.
- `fetch_test.go:33` (tabTitles order, R4): expect `transcript` in position 3.
- `hist_test.go:191` keeps `detailHeader()` (it survives). No change is expected.

Regenerate the goldens and read every diff:
- `round.golden` and `round-archived.golden` change (the new layout).
- Every other golden except those two must be byte-identical. A fleet or stats golden diff is a halt.

## 8. New tests

- `TestRenderMarkdown`: a table-driven test over §2.6: headings, bullets, a fence (inline rules are not applied inside), code
  spans, bold, an unmatched backtick. Compare after `stripANSI`, and check one styled case contains the accent's escape.
- `TestRoundCardByGroup`: for a row per group, assert the pill word and key labels. With actions false, assert `read-only`.
- `TestRoundCardHist`: a hist pane gives `archived`, `round N of M` and `[ ] step rounds`.
- `TestRoundHeadRowsMatchView`: at heights 17, 18 and 40, the number of lines before the first viewport line
  equals `headRows()`, and `viewportHeight() == rows - headRows()`.
- `TestRoundNoRawToken`: the whole rendered view contains neither a backtick nor
  `b.BuilderCandidate`, when BuilderName is set.
- `TestRenderCardFleetUnchanged`: the fleet goldens are unchanged. This is covered by the golden run, so name it in the report rather than adding code.
- Goldens `round-real-132` (132×34) and `round-real-100` (100×30):
  - Use a new `realRoundModel` in golden_test.go: `realFleetReport()` plus a spool-db row. Set Display ACTIVE,
    BuilderStatus working, Round 1, BuilderName gemini-3.8-flash-high, PlannerName architect-2,
    Branch relevo/spool-db, Headless{PID 1401366, StartedAt now-5m}, RoundStart now-5m, QuietFor 17s,
    and LiveUsage with Tokens In 718_000, CacheRead 4_200_000, Out 64_000.
  - Push its round view and deliver a plan tabMsg whose body contains a `#` heading, a `##` heading, a
    paragraph with `` `code` `` and `**bold**`, a bullet list and a fenced block.
  - Add `round-needs-you` (132×34) for fix-433.

CI: all tests are in `internal/ui` with fakes. No harness, no network, no cmd/relevo subcommand.

## 9. Working efficiently

- Read round_pane.go (whole), view_round.go (whole), detail.go, fetch.go:18-30, the
  view_fleet.go `cardLines` block, styles.go, and the test files named in §7, in one batched step.
- Make each file's edits in one call. round_pane.go's head/tab section (lines 286-437) can be one replacement.
- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`. For goldens, add
  `-run Golden -update`, then run `git diff --stat internal/ui/testdata/` and read every diff.
- Final: `gofmt -l $(git ls-files '*.go') internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`.
  Do **not** run `make check`: its release guard fails for an unrelated reason.

## 10. Steps

1. `card.go`, then refactor fleet `cardLines` onto it. Verify: the golden test passes with **no** `-update`,
   which proves the fleet is identical.
2. `markdown.go`, `TestRenderMarkdown`, and the `bodyOf` hook. Verify: the test passes.
3. styles.go `chipAccentStyle`, fetch.go `tabTitles`, and round_pane.go: `cardLines`, `tabsRow`, `headRows`,
   `contentWidth`, `view`, deleting R1-R3. Then view_round.go: `Context`, `Keys`, `HelpKeys`, the `actions` plumbing,
   and `vp.Width`. Verify: `go build ./internal/ui/`.
4. Ports (§7) and new tests (§8). Regenerate the goldens and read the diffs. Verify: the loop is green.
5. Run the final checks (§9).

## 11. Report

List:
- the files changed;
- each ported test with its R-number;
- every golden that changed, with one line each, confirming that no fleet or stats golden changed;
- `round-real-132.golden` verbatim;
- the check results.
