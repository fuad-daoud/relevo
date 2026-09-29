# Cockpit D2 overlays, round 4: clean box interiors, live bindings first, relative plan paths

Date: 2026-09-25. Worktree: ck-d2-overlay, which holds round 3 **uncommitted** on top of the WIP commit. Build on it; do not
reset or commit. Line numbers are exact in the worktree now. One round.

**Stop rather than improvise.** If the code differs from what is quoted, halt and report. Change only `internal/ui`.

## 1. What the real-terminal captures showed

The planner opened every overlay in the real TUI (the capturing-tui-screens skill). All of them work. Five things look wrong:

1. **Patchy interiors.** `renderModal` paints each row with `panelStyle`'s background (compose.go:102). The row's own styled
   segments end in a full SGR reset, so the background covers only part of some rows and all of blank ones. The box
   reads as stripes.
2. **Partial selection band.** `modalRow` (compose.go:114-136) wraps an already-styled line in `selBandStyle`. The
   inner resets end the band after the first segment, so only the `▸` or the name is highlighted.
3. **`:r` lists six done bindings and hides the live one.** `matches()` ranks by prefix, then length, then name, and caps at 8
   (cmdline.go:100-110 and the cap after it). Short done names (`round ck-a1`) win the cut, and the live `round ck-d2-overlay`
   never reaches the grouping that puts done bindings last (frame.go:393).
4. **Inputs show `> `.** `newPromptInput` (confirm.go:144-148) keeps textinput's default prompt. The modals already label
   their fields.
5. **The send input shows an absolute path** (`/home/…/.worktrees/<b>/docs/plans/`).
6. **The help sheet lists `/ filter` twice**, in MOVE & VIEW (the view's key) and in ANYWHERE (`globalKeys`).

## 2. Changes

**2.1 No panel background.**
- In `renderModal` (compose.go:102), draw the row without `panelStyle`: `borderSt.Render("│ ") + fitted + borderSt.Render(" │")`.
- Delete the `panelStyle` token (styles.go:18) if nothing else uses it.
- The box interior is the terminal background: `composeBox` already replaces the dimmed cells under the box.

**2.2 A whole-row band.** In `modalRow`, when `selected`, build the row from **plain** text:
- the marker `▸ `, the name, two spaces, the desc, the gap, and the note, all without styles;
- fit it to `innerW`;
- render it once with `selBandStyle.Foreground(<text colour token>).Bold(true)`.

Unselected rows are unchanged. The marker's accent colour is lost on the selected row; that is accepted.

**2.3 Live bindings before done ones, before the cap.**
- In `matches()` (cmdline.go), give each candidate a `done bool`, true for a `round <key>` entry whose report row
  `Display == "DONE"`.
- The sort compares, in order:
  1. prefix match first (as today);
  2. **not done before done**;
  3. then length;
  4. then name.
- The cap of 8 applies after the sort, as today.
- Keep the frame's live/done grouping; it is now just consistent.

**2.4 No input prompt.** In `newPromptInput` (confirm.go:144), set `in.Prompt = ""`. Every modal input then shows just its value and
cursor.

**2.5 The send input is relative to the binding's tree.**
- The `sendPicker` keeps a `root string` = `b.CWD`, where `plansDir` (confirm.go:404-414) is derived from.
- The pre-fill becomes `docs/plans/` when that directory exists: `plansDir(b)` made relative to `root`.
- Add a helper `resolve(v string) string`: `v` if absolute, else `filepath.Join(root, v)`, or `v` unchanged when `root == ""`.
- The list's directory, the validator (`os.Stat`), `pathCompletion`'s argument, and the path handed to the send confirm's
  `Actions.Send` all use `resolve(value)`.
- The confirm title shows the value as typed (relative).
- A value the human types as an absolute path keeps working.

**2.6 No duplicate keys in help.** In `helpColumns` (frame.go ~line 462), ANYWHERE is `globalKeys` minus any key whose `Key` already
appears in the view's keys.

## 3. Tests

- **`TestModalInteriorHasNoBackground`:** set the true-colour profile (as other colour tests do). Render a modal whose rows are
  styled. No row contains the old `#161a21` background escape.
- **`TestModalRowBandCoversRow`:** in true colour, a selected `modalRow` over a styled name and desc. Strip nothing, then check that the
  band's background escape appears once at the start, and that no reset appears before the last visible cell. Equivalently, the
  plain text equals `ansi.Strip` of the result, and the result contains exactly one background-setting SGR.
- **`TestCommandMatchesLiveBeforeDone`:** 7 done bindings with short names and 1 live binding with a long name, typed `r`. The
  live `round <long>` entry is within the first 8.
- **`TestSendPickerRelativePath`:**
  - A temp tree with `docs/plans/a.md`, and a row whose CWD is the tree.
  - The input pre-fill is `docs/plans/`. Typing `a.md` and pressing enter opens the send confirm, whose `Actions.Send` receives the
    absolute path.
  - An absolute path typed by the human also works.
- **`TestHelpNoDuplicateKeys`:** on `:fleet`, `/` appears once in the help modal.
- **Existing tests:** any that assumed the `> ` prompt or the absolute pre-fill are ported. Cite §2.4 or §2.5.
- **Goldens:** regenerate. Only overlay goldens may change (cmdline-open, help, prompt-gate, prompt-send, confirm-stop,
  confirm-stop-real-132, command-real-132, retry-list-132). A fleet, round or stats golden diff is a halt.

## 4. Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`, plus `-run Golden -update`.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`.
- `TestAddBranchDerivesName` in cmd/relevo fails only in your environment (the planner's run passes). Run cmd/relevo, and report it
  as before if it fails the same way; it is not a halt.
- Not `make check`.

## 5. Report

List:
- the files changed;
- the ports;
- the golden diffs;
- `command-real-132.golden` verbatim;
- the check results.
