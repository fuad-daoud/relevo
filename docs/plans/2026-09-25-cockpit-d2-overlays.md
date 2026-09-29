# Cockpit D2: every overlay is a boxed modal

Date: 2026-09-25. Worktree: ck-d2-overlay on branch `relevo/ck-d2-overlay`, rebased onto origin/main c7c5cbe0,
with one WIP commit (the approved modal spike: compose.go `dimLines`/`composeBox`/`renderModal`, the `modalOverlay`
interface, confirmBox as a modal). Line numbers are exact there. Build on it: do not reset, do not commit.
Design: canvas row **Overlays in D2**, boards `overlay · : command`, `gate form`, `send plan`, `retry on…`, `? keys`
(https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA). The user approved the stop confirm as built in a real terminal.

**Stop rather than improvise.** If a step is impossible as written or the code differs from what is quoted, halt
and report. Do not bend a test to make it pass. Change only `internal/ui`.

## 1. Goal

Every overlay draws the same way: a centred box in the upper third over the dimmed body, via `renderModal` and
`composeBox` (the `modalOverlay` path in frame.go `body`). While one is open, the footer shows **that overlay's** keys.
The behaviour behind each overlay is unchanged unless §3 says otherwise.

## 2. Shared pieces

**2.1 Footer keys for a modal.**
- Add `type overlayKeyer interface{ keys() []KeyHelp }`.
- frame.go `keysView` (line 196): when the command line is open, when `m.help` is set, or when `m.overlay` implements
  `overlayKeyer`, lay out those keys **instead of** the view's keys and the global tail. The notice and working
  indicator keep their right side.
- confirmBox's keys: `y <c.yes or "yes">`, `n cancel`.

**2.2 A selectable row.** In compose.go:
```
// modalRow renders one list row inside a modal: marker "▸ " (accent) when selected else "  ",
// name, desc, and an optional right-aligned note; the selected row wears selBandStyle across innerW.
func modalRow(selected bool, name, desc, note string, innerW int, nameStyle, descStyle lipgloss.Style) string
// modalSection renders a faint bold section label ("VIEWS").
func modalSection(label string) string
```

**2.3 Placement.** Unchanged: centred, `y = (bh - len(box)) / 3`.

## 3. The overlays

### 3.1 `:` command line (frame.go `body` lines 259-266 and `cmdBox` line 334 → a modal)

- Title `command`, want 80, accent border.
- **Rows:**
  - the input: `c.input.View()`;
  - blank;
  - then the matches grouped into sections, in this order, each section omitted when empty:
    - `VIEWS` (entries from the `commands` table);
    - `BINDINGS` (`round <key>` entries);
    - `GATES` (`ungate …` entries).
- **Match order:** `matches()` keeps its ranking. The groups keep that order inside them.
- **Binding entries:** desc is `<state word> · r<Round> · <candidateText>`, where the state word is lowercase `groupOf` (`working`, `idle`, `needs you`,
  `on hold`, `done`) from the report row. Done bindings sort after every other binding match.
- **Selection:** the selected match (`c.sel`) gets the band.
- **Over the cap:** when more than the 8 shown match, add a faint `+ N more match; keep typing` row. Add
  `func (c cmdLine) matchCount(env Env) int` for this. `matches()` keeps its cap of 8 and its signature.
- The last row is faint right-aligned `tab complete · enter go · esc close`.
- **Keys:** `↑↓ move`, `tab complete`, `enter go`, `esc close`. Key handling in `cmdLine.update` is unchanged.

### 3.2 `?` help (frame.go `helpBody` line 369 → a modal)

- Title `keys · <last crumb of the top view>`, want 108.
- Three columns, each headed by a `modalSection` label:
  - `MOVE & VIEW`: the view's help keys (`HelpKeys()` when it is a `helpKeyer`, else `Keys()`), minus those in the ACT set;
  - `ACT ON THE ROW`: the view's keys whose Key is one of `s E b x D u g r o`;
  - `ANYWHERE`: `globalKeys`.
- Each entry is `chip(kbdStyle, key)` padded to 12 cells, then the help in muted.
- **Keys:** `esc close`. The routing (esc, `?`, q close) is unchanged.

### 3.3 Gate: one form instead of two prompts (confirm.go `gateCmd` line 296)

New `formBox` overlay (a new file, `internal/ui/form.go`) implementing `overlay`, `modalOverlay` and `overlayKeyer`:
```
type formField struct {
	label    string
	input    textinput.Model
	hint     string             // faint line under the field; "" for none
	validate func(string) error // nil = anything goes
}
type formBox struct {
	kind     string     // box title
	header   []string   // pre-styled lines above the fields
	note     []string   // faint lines under the fields
	fields   []formField
	focus    int
	err      string
	submit   string     // label for enter
	onSubmit func(values []string) tea.Cmd
}
```
- **Keys:**
  - `tab` / `shift+tab` move focus (and focus/blur the inputs).
  - `enter` validates every field in order. The first error sets `err`, moves focus to that field and keeps the box open.
    If all pass, it closes and returns `onSubmit(values)`.
  - `esc` cancels.
  - Every other key goes to the focused input.
- **Render:**
  - `header`, then blank;
  - per field: the label, padded to 10 (accent when focused, else faint), then `input.View()` (focused input on the selection band), then its hint in faint on the next line if any, then blank;
  - `note`;
  - `err` in red, if set;
  - blank;
  - the button row: `chip(kbd,"enter") <submit>   chip(kbd,"tab") next field   chip(kbd,"esc") cancel`.
- Footer keys: the same three.

`gateCmd` builds one `formBox`:
- kind `gate`, submit `gate`;
- **header:** `Gate ` + `candidateText(b)` in accent bold, then faint `  (provider <second /-segment of b.BuilderCandidate>)`;
- **field 1:** label `for`, hint `e.g. 30m, 2h, 1d · empty = until cleared`, and the **existing** duration validator unchanged;
- **field 2:** label `reason`, hint `optional`, no validator;
- **note:** `The pick skips it until then. Builders already running are not stopped.`;
- `onSubmit` parses the duration as today and returns the existing `runAction(… Actions.Gate(ctx, subject, forDur, reason))`.

The subject (`b.BuilderCandidate`) and the Actions call are unchanged.

### 3.4 Send: a plan picker (confirm.go `sendCmd` line 335)

New `sendPicker` overlay (`internal/ui/send_picker.go`) implementing the three interfaces. It wraps today's prompt behaviour:
- **Input:** it keeps the input, the `pathCompletion` tab behaviour, the pre-fill (`plansDir(b)`), and the existing validator
  (file exists, not a directory).
- **The list:**
  - Up to 6 `*.md` files from the directory part of the current input value (or `plansDir(b)` when the input has no directory), newest first by modification time.
  - Filtered to names containing the typed base name (case-insensitive substring).
  - Each row is `modalRow(sel, relative path, "", "<ago> ago", …)`.
  - New helper: `func recentPlans(dir, filter string, n int) []planFile` with `planFile{path string; mod time.Time}`, pure apart from the directory read.
- **Keys:**
  - `↑` / `↓` move the list selection. The selection starts at "none", meaning the typed value is used.
  - `enter`: when a list row is selected, set the input to its path, then validate, then open **the existing send confirm** exactly as today.
  - `ctrl+e` closes the picker and returns `editorCmd(env, b)`, today's `E` flow.
  - `esc` cancels.
- **Render:**
  - title `send`, want 80;
  - the header: `Send a plan to ` + key (accent bold) + faint `  as round <the same number sendConfirmTitle uses> · on <candidateText>`;
  - blank; the `plan file` input row; blank; the list; blank;
  - faint `ctrl+e  write it in $EDITOR instead`;
  - the error in red, if any;
  - the button row: `enter choose`, `tab complete`, `esc cancel`.
- Footer keys: `↑↓ choose`, `tab complete`, `enter choose`, `ctrl+e $EDITOR`, `esc cancel`.

### 3.5 Retry: a candidate list (confirm.go `retryCmd` line 557)

New `listBox` overlay (`internal/ui/list_box.go`) implementing the three interfaces:
```
type listItem struct{ name, status, note string; statusStyle lipgloss.Style; disabled bool }
type listBox struct {
	kind, submit string
	header, note []string
	items        []listItem
	sel          int // index of a selectable item, or -1 when none is selectable
	onPick       func(name string) tea.Cmd
}
```
- `↑` / `↓` move to the previous or next **enabled** item. `enter` on an enabled item closes the box and returns `onPick(name)`. `esc` cancels.
- Each row is the name (width 26), the status (width 16, in its style), and the note in faint. Disabled rows are faint throughout.
- With no enabled item, show faint `no other candidate is ready`; `enter` then does nothing.

`retryCmd` builds it (kind `retry on…`, submit `retry`) from `env.Actions.Candidates(actorCell(b))`, **including** the
current candidate:
- the current candidate (`name == candidateText(b)`): status `current`, muted, disabled, note `running round <Round> now`
  when `roundOpen(b)`, else `last used`;
- gated (an entry in `env.Report.Gated` whose `Name == name`): status `gated <ago(env.Now, g.Until)>` (or `gated`
  when Until is zero), faint, disabled, note = the gate's provider;
- otherwise: status `ready`, green, enabled.

The header is `Retry ` + key (accent bold) + ` round <Round> on another candidate`. The note is:
- when `roundOpen(b)`: `Stops round N, then sends its plan again on the chosen candidate.`;
- otherwise: `Sends the last plan again as a new round on the chosen candidate.`;
- then, in both cases, `The binding stays on it for later rounds.`

`onPick` opens **the existing retry confirm**, unchanged.

Footer keys: `↑↓ choose`, `enter retry`, `esc cancel`.

### 3.6 The remaining prompts (the bind flow)

`promptBox` survives for bind's name, candidate and feature prompts. It implements `modalOverlay` and `overlayKeyer`:
- title `kind` (a new field; "" means `input`; the bind prompts set `bind`), want 72;
- rows: blank, `p.title` in text, blank, `p.input.View()`, the current `choices` hint when set (faint `tab cycles: a · b · c`), then `p.err` in red, blank;
- keys: `enter ok`, `tab complete` (only when choices or complete are set), `esc cancel`.

Its behaviour is unchanged.

## 4. Deletions (closed list)

- **O1** The command box drawn over the body's top lines (frame.go `body` lines 259-266 and `cmdBox` line 334). Replaced by §3.1.
- **O2** The full-screen help list (`helpBody` line 369). Replaced by §3.2.
- **O3** Gate's two chained prompts (duration, then reason). Replaced by §3.3's one form.
- **O4** Send's bare `plan file:` prompt. Replaced by §3.4's picker; its validation and completion survive.
- **O5** Retry's tab-cycled choice prompt. Replaced by §3.5's list.
- **O6** `promptBox.view`'s bottom-line rendering, as the only rendering. `view` may stay for callers; the frame uses `modal`.

Everything else survives: every Actions call, every confirm, `pathCompletion`, `editorCmd`, bind, the key routing order, and `matches()`.

**Ports.** Change the assertions, don't delete. Cite the O-number for each. The likely ones:
- `actions_test.go` tests that drive gate, send or retry through the old prompts (O3-O5);
- `cmdline_test.go` rendering assertions (O1; the `matches` tests stay as they are);
- any test that asserts help lines (O2).

## 5. Tests (new)

- `TestCommandModalGroupsSections`: a report with 2 live, 1 done binding and 1 gate, typed `r`. The stripped box shows
  `VIEWS`, then `BINDINGS` with the done binding last, and the selected row is the first match.
- `TestCommandModalFoldsOverflow`: 12 bindings, empty input. It shows 8 rows and `+ N more match`.
- `TestHelpModalColumns`: on `:fleet` with actions, `ACT ON THE ROW` holds `x` and `D`, `MOVE & VIEW` holds `.` and `/`, and `ANYWHERE`
  holds `:`.
- `TestGateFormOneSubmitCallsGateOnce`: a fake Actions. Type `2h`, tab, type `quota`, enter. Gate is called once with 2h and `quota`.
  An invalid `2x` keeps the box open with an error and focus on `for`.
- `TestGateFormEmptyDurationIsUntilCleared`: an empty `for` field calls Gate with 0.
- `TestSendPickerListsNewestFirst`: a temp dir with three `.md` files at distinct mtimes and one `.txt`. The list shows the three `.md` files, newest
  first. Typing a filter narrows it. `↓` then `enter` opens the send confirm with that path.
- `TestSendPickerCtrlEOpensEditorFlow`: `ctrl+e` returns a non-nil cmd and closes the picker. Assert via the returned overlay state.
- `TestRetryListDisablesCurrentAndGated`: three candidates, one current and one gated. `sel` starts on the ready one, `↑`/`↓` never land on a
  disabled one, and `enter` opens the retry confirm naming it.
- `TestModalFooterShowsOverlayKeys`: with the gate form open, the footer contains `next field` and not `? all keys`.
- **Goldens:**
  - regenerate `cmdline-open`, `help`, `prompt-gate`, `prompt-send`, `confirm-stop`;
  - add `retry-list-132` (132×34) over `realFleetReport()`, and `command-real-132` (`:` then `r` over `realFleetReport()`);
  - no fleet, round or stats golden may change.

All tests are in `internal/ui` with fakes. No harness, network or cmd/relevo subcommand is involved.

## 6. Working efficiently

- Read in one step: frame.go (`keysView`, `body`, `cmdBox`, `helpBody`), cmdline.go, confirm.go lines 1-200 and 290-620,
  compose.go, actions_test.go (the fake Actions and the gate/send/retry tests), cmdline_test.go.
- Make each file's edits in one call. The three new overlay types go in their own files.
- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`. For goldens, add `-run Golden -update`,
  then `git diff --stat internal/ui/testdata/`.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## 7. Steps

1. §2 (keys interface, `modalRow`, `modalSection`) and confirmBox keys. Verify: build.
2. §3.1 command modal and §3.2 help modal (O1, O2) with their tests. Verify: the loop is green.
3. §3.3 formBox and the gate (O3) with tests.
4. §3.4 send picker (O4) with tests.
5. §3.5 listBox and retry (O5) with tests.
6. §3.6 promptBox as a modal (O6). Then ports, goldens and the final checks.

## 8. Report

List:
- files changed, per step;
- every port with its O-number;
- the golden diffs;
- `command-real-132.golden` and `retry-list-132.golden` verbatim;
- the check results.
