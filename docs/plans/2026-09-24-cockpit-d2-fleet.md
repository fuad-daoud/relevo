# Cockpit D2: the new theme and the grouped :fleet

Date: 2026-09-24. Base: origin/main dbb08c1e. One round. Line numbers below are exact at that commit.
Design: the "relevo cockpit" canvas, board **theme D2 · A+B, blue**
(https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA). Review:
`docs/plans/2026-09-24-cockpit-review.md` R3, R4, R7, R8, R12, R17.

**Stop rather than improvise.** If a step is impossible as written, if the code
contradicts a fact stated here, or if a test outside the ported list (§7) fails, halt
and report what you found. Do not bend a test to make it pass.

This round touches only `internal/ui` and the two `ui.Options{…}` literals in
`cmd/relevo`. Plain text only in anything a CLI prints; this round prints nothing
new to a CLI.

## 1. System overview

The cockpit (`relevo` on a terminal) renders `:fleet` as a nine-column spreadsheet
in a 256-colour palette. On a real machine (34 bindings, 27 of them DONE) the screen is
flooded with DONE rows, gated candidate tokens fill the header in amber, the REPO column
shows worktree paths, and an unexplained amber dot sits in the gutter. This round does
three things:

1. **Theme D2.** It replaces the palette in `internal/ui/styles.go` with the D2 tokens (§2.1). The new tokens are pills,
   key chips, a selected-row band and one border colour. Every view picks up the colours;
   only the frame and `:fleet` change layout.
2. **Frame.** The header shows the version instead of gates and loses its bar background.
   The rule above the footer becomes a blank row. Footer keys are chips, and `?` is "all keys".
3. **`:fleet` grouped by state.** Sections: needs you, working, idle, on hold, done.
   Done folds to one line and `.` toggles it. Each row has four facts plus spend. A card at
   the bottom shows the selected binding and the keys that apply to it. One faint line
   under it lists gated providers.

Out of scope: the command box, confirm/prompt overlays, round detail, `:rounds`,
`:stats` layout, `serve ui` owner labels. They get the new colours only.

## 2. Design (the spec for this slice)

### 2.1 Tokens (`internal/ui/styles.go`, truecolor hex; lipgloss degrades on 256-colour terminals)

| token | hex | used for |
|---|---|---|
| text | `#e6e8ec` | names, values |
| muted | `#9097a3` | secondary values, labels |
| faint | `#596070` | hints, counts, planner, spend |
| border | `#22262e` | the card border, any rule |
| accent | `#6ea8fe` | the ◆ mark, selection bar, unread report, links |
| warn | `#f2b84b` | needs-you only |
| green | `#5fd08f` | working |
| red | `#ff6b81` | errors, gates, an exited/stalled builder |
| selBg | `#1a1f28` | the selected row's full-width band |
| chipWarn | bg `#3a2e14`, fg warn | the needs-you pill and header count |
| chipGreen | bg `#15301f`, fg green | the working pill |
| kbd | bg `#1d2129`, fg text | key chips, neutral pills (idle, on hold, done), the view crumb |

Rules: amber (warn) appears only for needs-you. There is no bar background on the header or footer. Boxes
are allowed only for the fleet card and modals. `styles.go` stays the only file that names a colour.

Symbols: `●` needs you (warn), `●` working (green), `○` idle (muted), `◐` on hold (muted),
`✓` done (faint), `◆` the relevo mark (accent), `◌` gated (red), `▍` selection bar (accent),
`╰` question quote (faint).

### 2.2 Frame (every view)

- **Row 1, header.** Left: `  ◆ relevo` (accent, bold). Then three spaces and the crumbs:
  one crumb renders as a kbd chip (` fleet `); several render muted, joined by a faint
  ` › `, with the last one as a kbd chip. Right: the needs-you chip ` ● N need(s) you `
  (chipWarn, bold) when N > 0, then four spaces, `shortVersion(opts.Version)` in faint
  (omitted when empty), four spaces, the clock `15:04` in muted, then two spaces. **No gates, no
  background.**
- **Row 2, context.** As today, from `View.Context`.
- **Body.** As today.
- **Row H-1:** blank (was the rule).
- **Row H, keys.** Each key is a kbd chip ` k ` followed by ` label` in muted. Keys are separated by
  five spaces. The view's `Keys()` come first, then the global tail `: command`, `? all keys`,
  `q quit` (root) / `esc back` (deeper). Drop and notice rules are unchanged.
- **Help overlay (`?`).** It lists the view's `HelpKeys()` when the view implements them, else
  `Keys()`.

### 2.3 `:fleet` (reference render, 132×34, fixture of §5.3)

```
  ◆ relevo    fleet                                                                          ● 1 needs you     v0.13.0-28    23:27
   7 live   1 needs you   2 working   4 idle   27 done                                          sort attention  ·  refreshed 0s ago

    needs you   1    a builder is waiting on an answer
 ▍ ●  fix-433             r2 · question · 3m      deepseek-v4.1-flash     architect-3    $0.03
        ╰ “Should the dedupe also cover archived bindings, or only live ones?”

    working   2    a round is running
   ●  spool-db            r1 · quiet 17s · 5m     gemini-3.8-flash-high   architect-2     plan
   ●  tok-seg             r1 · quiet 16s · 6m     gemini-3.8-flash-high   architect-5     plan

    idle   4    reported, waiting for the next plan
   ○  oc-tui-a            r6 · reported 14m       deepseek-v4.1-flash     architect-3    $0.20
   ○  rl-tail             r2 · reported 31m       gemini-3.8-flash-high   architect-5     plan
   ○  oc-tui-probe        r4 · reported 48m       gemini-3.8-flash-high   architect-3     plan
   ○  serve-status-json   r2 · reported 1h        deepseek-v4.1-flash     architect-13   $0.03

   ✓  27 done  ·  3 today  ·  last ck-w2 38m ago       .  show

 ╭─ fix-433  round 2 · question · 3m ─────────────────────────────────────────────────────────────────────────────────────────────╮
 │  planner architect-3  ·  deepseek-v4.1-flash  ·  relevo/fix-433  ·  +2 commits  ·  $0.03                                       │
 │                                                                                                                                │
 │   enter  open round      s  send the next plan      o  shell      x  stop                                                      │
 ╰────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯
   ◌ gated  antigravity 1d  ·  openai 25d  ·  the pick skips them

   enter  open      s  send      x  stop      D  done      g  gate      /  filter      :  command      ?  all keys      q  quit
```

Exact spacing may differ where a rule below says otherwise. The rules win over the picture.

**Groups**, in this order, each omitted when empty:

| group | membership | pill | dot | hint |
|---|---|---|---|---|
| needs you | `Display == "NEEDS YOU"` or `reportReady(b)` | chipWarn | ● warn | `a builder is waiting on an answer` |
| working | `Display == "ACTIVE"` and `BuilderStatus != "idle"` | chipGreen | ● green | `a round is running` |
| idle | `Display == "ACTIVE"` and `BuilderStatus == "idle"` | kbd | ○ muted | `reported, waiting for the next plan` |
| on hold | `Display` is `HELD` or `PAUSED` | kbd | ◐ muted | `paused or held` |
| other | any other `Display` except DONE | kbd | ○ muted | `` (none) |
| done | `Display == "DONE"` | kbd (expanded only) | ✓ faint | `` |

Within a group, rows keep `relevo.SortRows(bindings, f.attention)` order, and `a` still flips it.

**Section line:** 4 spaces (3 gutter + 1), the pill ` <group> ` (lowercase), two spaces, the count in faint,
four spaces, the hint in faint italic. A blank line follows every group's rows.

**Row:** gutter (3 cells: ` ▍ ` accent when selected, else 3 spaces), dot, 2 spaces, then
the columns. Each column's width includes its trailing gap:

| column | width | content | style |
|---|---|---|---|
| name | 20 | `b.Key()`, clipped with `…` | text; bold when selected |
| now | 24 | `rowNow(b, now)` (§4) | muted; text when selected; accent when `b.Unread` and the group is idle or done; red when `BuilderStatus` starts with `exited` or `stalled` |
| candidate | 24 | `candidateText(b)` | muted |
| planner | 14 | `plannerCell(b)` | faint |
| spend | 6, right-aligned | `spendCell(b)` | faint |

When a non-builder actor (`b.Role != ""`) is present, append two spaces and the role in faint after spend.
The selected row is rendered full width with the selBg band. A NEEDS YOU row whose
`Waiting.Line != ""` gets a quote line: 8 spaces, `╰ ` faint, then the line in muted italic.
It is part of the row (same selection band).

**Narrow widths:** drop planner first, then spend, then candidate, until the row fits.
Name and now always stay. Every row and the card use the same plan.

**Done fold:** while folded (`showDone == false`) and no filter is applied, DONE rows are
not in the cursor's rows. One line replaces them: 3 spaces, `✓` faint, 2 spaces,
`N done` muted, then faint `  ·  K today  ·  last <key> <ago> ago`, then
6 spaces, a kbd chip ` . `, and ` show` faint. "today" means `Last.TS` falls on the local date of `env.Now`.
"last" is the DONE row with the newest `Last.TS` (omit both parts when no Last). `.`
toggles `showDone`. While shown, done rows form a normal `done` section whose hint is
`. hide`. While a filter is applied, done rows that match are shown as a section whatever
`showDone` is.

**Card** (only when a row is selected and the body is at least 16 lines tall): five lines
at the bottom of the body, width = body width - 2, starting at column 1.
- Top: `╭─ ` + key (accent, bold) + two spaces + `round N · <rowNow without its "rN · " prefix>` (faint) + space + `─…─` + `╮`. The border is in the border colour.
- Meta: `│  ` + parts joined by `  ·  ` (muted):
  - `planner <name>`, with the name in warn when the group is needs you;
  - the candidate;
  - `b.Branch`, or `repoCell(b)` when there is no branch;
  - `+N commits` when `LastClose != nil && LastClose.Commits > 0`;
  - spend when non-empty.
- A blank bordered line.
- Keys: `│   ` + chips for the group, each a kbd chip followed by a muted label. With `env.Actions == nil`, only `enter open round`.
  - needs you: `enter` open round, `s` send the next plan, `o` shell, `x` stop
  - working: `enter` open round, `x` stop, `g` gate, `o` shell
  - idle: `s` send the next plan, `enter` open round, `D` done, `o` shell
  - on hold / other: `enter` open round, `s` send, `D` done
  - done: `enter` open round, `u` unbind
- Bottom: `╰─…─╯`.

**Gated line** (only when `len(env.Report.Gated) > 0`): directly under the card (or at the
bottom of the body with no card): 3 spaces, `◌` red, ` gated  ` faint, then one
`<provider> <left>` per provider, joined by faint `  ·  `, then faint `  ·  the pick skips them`.
- The provider is the second `/`-segment of `Gate.Token`, or the whole token when it has no `/`.
- A provider appears once, with its latest `Until`.
- `<left>` is `ago(env.Now, g.Until)` for a non-zero Until, and `until cleared` for a zero one.

**Body order:** a blank line, the filter input (while open), the groups, the done fold line,
blank filler, the card, the gated line. The list region (everything above the filler)
scrolls to keep the cursor's lines visible, as `windowTopLines` does today. The card and gated
line never scroll.

**Context row (fleet):**
- Left: 3 spaces, then items separated by 3 spaces, each a count (text, bold) and a label (muted):
  `N live` (all non-DONE rows), `N need(s) you`, `N working`, `N idle`, `N on hold`, `N done`. A zero count is omitted, except live.
- Right: unchanged (filter, `sort …`, refreshed).

**Fleet footer keys:**
- `Keys()` with actions: `enter open`, `s send`, `x stop`, `D done`, `g gate`, `/ filter`.
- `Keys()` without actions: `enter open`, `/ filter`, `a sort`.
- `HelpKeys()` (every key): `↑↓ move`, `home/end first/last`, `enter open`, `a sort`, `. done rows`, `/ filter`, and with actions also `s send`, `E edit+send`, `b bind`, `x stop`, `D done`, `u unbind`, `g gate`, `r retry on…`, `o shell`.

## 3. File structure

```
internal/ui/styles.go        tokens (§2.1): restyled vars + new chip/kbd/border styles, chip() helper
internal/ui/frame.go         headerView (§2.2), keysView chips, helpBody uses HelpKeys, ruleView removed
internal/ui/shell.go         View(): blank row instead of ruleView; Model reads opts.Version
internal/ui/ui.go            Options.Version
internal/ui/version.go       NEW: shortVersion
internal/ui/view.go          helpKeyer interface
internal/ui/view_fleet.go    grouped layout, fold, card, gated line (replaces the column table)
internal/ui/fleet_group.go   NEW: group type, groupOf, group metadata table, rowNow
internal/ui/*_test.go        ported tests (§7) + new tests (§8)
internal/ui/testdata/*.golden  regenerated; new fleet-real-132.golden, fleet-real-100.golden, fleet-real-done.golden
cmd/relevo/main.go           ui.Options{… Version: buildVersion()} in the `relevo ui` path
cmd/relevo/serve.go          ui.Options{… Version: buildVersion()} in cmdServeUI
```

## 4. Types and contracts

```
// ui.go
Options.Version string   // "" hides the version in the header

// version.go
func shortVersion(v string) string
  // "v0.13.0-28-gb66c6fc" -> "v0.13.0-28"; "v0.13.0-28-gb66c6fc-dirty" -> "v0.13.0-28";
  // "v0.13.0" -> "v0.13.0"; "" -> ""; anything else unchanged.
  // Rule: drop a trailing "-dirty", then a trailing "-g<7+ hex>".

// view.go
type helpKeyer interface{ HelpKeys() []KeyHelp }   // optional; helpBody prefers it

// fleet_group.go
type fleetGroup int
const ( groupNeedsYou fleetGroup = iota; groupWorking; groupIdle; groupHeld; groupOther; groupDone )
func groupOf(b relevo.BindingStatus) fleetGroup          // table in §2.3
type groupMeta struct{ label, hint string; pill, dot lipgloss.Style; glyph string }
var groupMetas map[fleetGroup]groupMeta                  // §2.3 table (done's hint set at render time)
func rowNow(b relevo.BindingStatus, now time.Time) string
  // needs you / held / other: "r<Round> · " + nowCell(b, now)   (existing whatAge rules)
  // working: "r<Round> · " + (BuilderStatus when != "working", else "quiet <QuietFor>" when QuietFor != "",
  //          else "working") + " · " + ago(RoundStart, now) when RoundStart non-zero
  // idle:    "r<Round> · reported <ago(LastPayload.TS)>" when LastPayload.Kind == report,
  //          else "r<Round> · idle <ago(Last.TS)>" when Last != nil, else "r<Round> · idle"
  // done:    nowCell(b, now) (today: "done · 3h")
  // "r<Round> · " is omitted when Round == 0.

// styles.go (names are contracts; values from §2.1)
textStyle, mutedStyle, faintStyle, borderStyle, accentStyle, warnStyle, greenStyle, redStyle
selBandStyle (Background selBg), chipWarnStyle, chipGreenStyle, kbdStyle
func chip(s lipgloss.Style, text string) string   // s.Render(" " + text + " ")
// Keep the existing var names other files use (fgStyle, dimStyle, ruleStyle, selectedBg,
// stateNeedsYouStyle, stateActiveStyle, stateDoneStyle, stateHeldStyle, errorStyle, emptyStyle,
// activeTabStyle, inactiveTabStyle, diff*Style, archivedStyle, pillStyle, stateStyle) as aliases
// or re-pointed values so no other view changes code: fgStyle=text, dimStyle=muted, faintStyle=faint,
// ruleStyle=border, selectedBg=selBand, stateNeedsYouStyle=warn bold, stateActiveStyle=green,
// stateDoneStyle=faint, stateHeldStyle=accent, errorStyle=red bold. headerBar is deleted (§7 D1).

// view_fleet.go (fleetView gains one field)
fleetView.showDone bool
func (f fleetView) rows(env Env) []relevo.BindingStatus      // visible rows in GROUP order (fold + filter applied)
func (f fleetView) HelpKeys() []KeyHelp
func fleetRowPlan(width int) (candidate, spend, planner bool) // which optional columns fit (§2.3 narrow rule)
func fleetRowLine(b relevo.BindingStatus, g fleetGroup, selected bool, now time.Time, width int) string
func (f fleetView) cardLines(env Env, b relevo.BindingStatus, width int) []string   // exactly 5
func gatedLine(env Env, width int) string                                          // "" when no gates
func doneFoldLine(rows []relevo.BindingStatus, now time.Time, width int) string
```

`fleetLine{text, row}` and `windowTopLines` stay. Section, blank and fold lines carry
`row: -1`, so they are never "the cursor's row".

## 5. Pseudocode

### 5.1 rows(env)
```
sorted := SortRows(env.Report.Bindings, f.attention)
filtered := apply activeFilter (fleetRowMatches, unchanged)
bucket filtered by groupOf, preserving order
out := needsYou ++ working ++ idle ++ held ++ other
if showDone || activeFilter() != "": out ++= done
return out
```
The cursor, sticky, moveCursor, enter and action keys all use this slice, so a folded
done row can never be selected or acted on.

### 5.2 Body(env, width, height)
```
if !Loaded -> loading block (unchanged); if no bindings -> empty block (unchanged)
fixed := [blank]; if filtering: fixed += filter input line
list := for each non-empty group in order (done only when shown):
          section line (row -1); for each row: row line (+ quote line), row index i; blank (row -1)
        if done folded and there are done rows (and no filter): fold line (row -1)
bottom := []
if a row is selected and height >= 16: bottom += cardLines(selected)
if gates: bottom += gatedLine
listH := height - len(fixed) - len(bottom) - (1 if bottom non-empty else 0)
start := windowTopLines(list, listH); draw list[start:start+listH], pad with blanks to listH
emit fixed ++ window ++ [blank if bottom] ++ bottom, fitLines(width, height)
```
`tableHeight`, `fixedRows` and `windowTop` follow the same arithmetic, so a scroll
computed in Update matches Body. Keep one helper that returns
(fixed, bottom) heights and use it in both.

### 5.3 Keys
```
Update: case ".": f.showDone = !f.showDone; repointed(env)   (not while filtering: the input owns keys)
```

## 6. Errors

No new error paths. Rendering never fails. A zero `Until` renders `until cleared`. A
missing `LastPayload` or `Last` omits its fact. `shortVersion` of an unexpected string returns it
unchanged.

## 7. Deletions (closed list) and ports

Deleted:
- **D1** `headerBar` (styles.go) and its use in `headerView`: the header has no background.
- **D2** The loop over `env.Report.Gated` in `headerView` (frame.go:161-163). Gates move to
  the fleet's gated line.
- **D3** `ruleView` (frame.go:182-188) and its call in `View()` (shell.go:383). That row is
  now blank.
- **D4** The fleet's column table: `fleetColumns`, the `col*` consts, `fleetLineWidth`,
  `fleetColumnPlan`, `fleetCells`, `fleetHeaderCells`, `fleetHeaderLine`,
  `renderFleetCells`, and the header line in `Body`/`fixedRows` (view_fleet.go:440-661,
  707-713, 776). The ACTOR, RND, STATE and REPO columns go with it: the state becomes the
  group, the round moves into NOW, the actor becomes a tag, and the repo/branch move into the card.
- **D5** The amber unread dot in `fleetGutter` (view_fleet.go:665-675). Unread now tints the
  NOW cell accent (§2.3).
- **D6** The 256-colour index values in styles.go (replaced by §2.1 hex values).

Everything else survives: the filter, `a` sort, enter, the sticky cursor, the question
line, `reportReady`, every action key and confirm, the loading and empty blocks, notices,
the working indicator, and the error block.

**Ports.** A test that asserted surviving behaviour through a deleted mechanism is
ported: change its assertion, don't delete the test. Expected ports, each to be cited
with its D-number in the report:
- `view_fleet_test.go`: `TestFleetBodyHeader` (D4: assert section lines instead of the
  column header), `TestFleetHeaderSurvivesNarrowing` (D4: name and now survive at 80),
  `TestFleetColumnsAndNarrowDropOrder` (D4: the new drop order), `TestFleetNeedsYouSecondLine`
  (the quote glyph `└` becomes `╰`).
- `list_test.go`: the `TestFleet*` window tests, if their line arithmetic assumed the header line (D4).
- Any test asserting `gated` text in the header (D2): assert it in the fleet body instead.
- Any test asserting the rule row or `? help` in the footer (D3, §2.2): `? all keys`.

Delete a test only when every assertion in it is about a deleted item, and cite the
item. Regenerate every golden with `-update`, then read each regenerated file. A
golden whose change is not explained by D1-D6 or §2 is a halt.

## 8. New tests

- `TestShortVersion`: the §4 table.
- `TestFleetGroupsOrderAndOmission`: a fixture with one row per group. Section lines appear in
  the §2.3 order. An empty group has no section line.
- `TestFleetDoneFold`: 3 done rows, folded. `rows()` excludes them, the fold line says `3 done`,
  and `.` includes them. A filter that matches one done row shows it while folded.
- `TestFleetCardKeysByGroup`: select a row of each group and assert the card's key labels. With
  `Actions == nil`, only `open round`.
- `TestFleetRowDropOrder`: at widths 132, 90, 70 and 50, planner goes first, then spend, then candidate.
  Name and now are always present.
- `TestHeaderShowsVersionNotGates`: the header contains `v0.13.0-28` (Options.Version
  `v0.13.0-28-gb66c6fc`) and no `gated`. The fleet body contains
  `◌ gated  antigravity` for a gate on `agy/antigravity/claude-sonnet-4-6`.
- `TestGatedLineOneProviderLatestUntil`: two gates on one provider give one entry with the later Until.
- `TestHelpListsHelpKeys`: `?` on `:fleet` lists `. done rows` and `r retry on…`. The footer
  shows `? all keys`.
- Goldens `fleet-real-132` (132×34), `fleet-real-100` (100×30), `fleet-real-done` (132×34
  after `.`), built from a new `realFleetReport()` in golden_test.go:
  - fix-433: NEEDS YOU, Waiting{Cause "blocked", Line as §2.3, Since now-3m}, Round 2, BuilderName
    deepseek-v4.1-flash, PlannerName architect-3, Branch relevo/fix-433, LastClose{Commits 2},
    Spend{Measured 0.03}.
  - spool-db, tok-seg: ACTIVE, working, QuietFor 17s/16s, RoundStart now-5m/now-6m,
    gemini-3.8-flash-high, Spend{Plan 1}.
  - oc-tui-a, rl-tail, oc-tui-probe, serve-status-json: ACTIVE, BuilderStatus idle,
    LastPayload{Kind report, TS now-14m/31m/48m/1h}, oc-tui-a Unread.
  - 27 DONE rows `done-01`…`done-27` with Last.TS spread so that 3 fall on today.
  - Gated: `agy/antigravity/claude-sonnet-4-6` until now+42h, and `codex/openai/gpt-5.6-terra:high` until now+25d.

  Use the test clock `railNow`. The picture in §2.3 uses 23:27 only for illustration.

CI note (CLAUDE.md): these are `internal/ui` tests with fakes. No test spawns a harness,
reaches the network, or runs a `cmd/relevo` subcommand.

## 9. Working efficiently

- Batch independent reads (styles.go, frame.go, shell.go View(), view_fleet.go, view.go,
  ui.go, golden_test.go, view_fleet_test.go, list_test.go) in one step. The locations
  above are exact at dbb08c1e. Don't re-search for them.
- Make each file's changes in one edit, or one Write for view_fleet.go's rewritten table section.
- Focused loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`
  (create the dir; /tmp has a small quota). Fix every failure before rerunning.
  Goldens: the same command with `-run Golden -update`, then read the diffs.
- Full check once at the end: `make check`. If a hook refuses it, run
  `gofmt -l $(git ls-files '*.go')`, `go vet ./...`, `go mod tidy -diff` and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./... -count=1`, and say so in the report.

## 10. Steps

1. **Tokens.** Rewrite styles.go per §2.1 and §4: the new styles, `chip()`, re-pointed old
   names, and delete `headerBar` (D1, D6). Verify: `go build ./internal/ui/`.
2. **Version plumbing.** Add `Options.Version`, `version.go` `shortVersion`, and pass
   `Version: buildVersion()` in cmd/relevo/main.go (the `ui.Run` literal in the `relevo ui` path) and
   serve.go (`cmdServeUI`'s `ui.RunSource` literal). Verify: `TestShortVersion` passes;
   `go build ./...`.
3. **Frame.** headerView per §2.2 (D2), keysView chips and `? all keys`, helpBody prefers
   `helpKeyer`, and `View()` gets a blank row instead of the rule (D3). Verify: the package builds, and the
   frame tests run (expect golden diffs only).
4. **Fleet groups.** fleet_group.go (`groupOf`, `groupMetas`, `rowNow`). Then rewrite
   view_fleet.go's table section per §2.3/§5 (D4, D5), plus `showDone`, `.`, `HelpKeys`, card
   and gated line. Verify: §8 fleet tests pass.
5. **Ports and goldens.** Port the §7 tests, add `realFleetReport()` and the three new goldens,
   and regenerate all goldens. Read each diff against D1-D6/§2. Verify: `go test ./internal/ui/` is green.
6. **Full check** per §9.

## 11. Report

List:
- the files changed;
- every ported or deleted test with its D-number;
- every regenerated golden, with one line on why it changed;
- the 132×34 `fleet-real-132.golden` pasted verbatim;
- the full-check result.
