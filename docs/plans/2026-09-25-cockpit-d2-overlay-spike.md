# Cockpit D2: modal overlay spike (the confirm box)

Date: 2026-09-25. Base: branch `relevo/ck-d2-fleet` at 9c7eff44 (PR #449). Line numbers are
exact at that commit. One round. Design: canvas board **overlay · confirm stop**
(https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA).

**Stop rather than improvise.** If a step is impossible as written or the code differs
from what is quoted, halt and report. Do not bend a test to make it pass. Change only
`internal/ui` (and `go.mod`/`go.sum` if `go mod tidy` promotes `charmbracelet/x/ansi` to a
direct dependency).

## 1. Purpose

Every D2 overlay is a rounded box drawn **over** the current screen. The screen behind it is
dimmed, and the box is centred. Today an overlay replaces the body's last rows with plain lines
(frame.go `body`, the `m.overlay != nil` branch). This round proves the compositing on one
overlay type, `confirmBox`, so the user can press `x` in a real terminal and judge it before
the other overlays follow. The prompts, the `:` box and the help sheet are **unchanged** in
this round.

## 2. Mechanism

New file `internal/ui/compose.go`. It uses `github.com/charmbracelet/x/ansi` (already in the
module graph at v0.8.0; `ansi.Strip`, `ansi.Truncate`, `ansi.TruncateLeft` are ANSI- and
wide-character-aware).

```
// dimLines strips every style from each line and re-renders it in shadeStyle, so
// the screen behind a modal reads as one flat dark tone.
func dimLines(lines []string) []string

// composeBox draws box over base at column x, row y. For each box row i with
// y+i in range: left := ansi.Truncate(base[y+i], x, ""); right :=
// ansi.TruncateLeft(base[y+i], x+boxW, ""); line = left + box[i] + right, then
// fit(line, width). Rows outside base are dropped; x is clamped to [0, width-boxW]
// (0 when the box is wider than width). base is not modified.
func composeBox(base, box []string, x, y, width int) []string

// renderModal draws the D2 box: innerW = min(want, width-6) (floor 20); a top
// border "╭─ " + title + " " + "─"… + "╮"; one line per row, "│ " + row padded
// to innerW on panelStyle's background + " │"; "╰─…─╯". The border and title
// use dangerStyle when danger, else accentStyle; the title is bold.
func renderModal(title string, rows []string, want, width int, danger bool) []string
```

**Placement.** The box is centred horizontally. Vertically,
`y = max(0, (bodyHeight - len(box)) / 3)`, which puts it in the upper third.

**New tokens** in styles.go (the only file that names colours):
- `shadeStyle`: foreground `#2a2f39`;
- `panelStyle`: background `#161a21`;
- `dangerStyle`: foreground `#ff6b81` (the red token; alias `redStyle` if that reads better);
- `chipDangerStyle`: background `#3a1820`, foreground `#ff6b81`, bold.

## 3. Wiring

- `view.go` or `confirm.go`: a new optional interface
  `type modalOverlay interface{ modal(width int) (title string, rows []string, want int, danger bool) }`.
- frame.go `body` (the `m.overlay != nil` branch, lines 271-282 at the base commit): when
  `m.overlay` implements `modalOverlay`:
  1. `lines := strings.Split(m.top().Body(env, env.Width, bh), "\n")`;
  2. `dimmed := dimLines(lines)`;
  3. `box := renderModal(...)`;
  4. compose it at the placement above;
  5. `fitLines(..., env.Width, bh)`.

  Otherwise, keep today's behaviour exactly. The header and the footer are **not** dimmed.
- `confirmBox` (confirm.go:40-64) gains three fields and implements `modalOverlay`:
  - `kind string`, the box title; "" means `confirm`;
  - `yes string`, the label after the `y` chip; "" means `yes`;
  - `danger bool`.

  `modal` returns these rows:
  1. a blank line;
  2. `c.title` in text, bold;
  3. a blank line;
  4. each of `c.lines` in muted;
  5. a blank line;
  6. the buttons row: `chip(chipDangerStyle or kbdStyle, "y")` + " " + muted(yes) + six spaces + `chip(kbdStyle,"n")` + muted(" cancel") + six spaces + faint("any other key cancels");
  7. a blank line.

  `want` is 72. `confirmBox.view` stays for any caller that still uses it.
- **Key behaviour changes (this is the one semantic change):** `confirmBox.update` treats every
  key other than `y`/`Y` as cancel. Today only `n`, `N` and `esc` cancel, and other keys are ignored.
  The box now says "any other key cancels", and it must be true.
- The stop confirm (confirm.go, the `Stop %s round %d?` constructor near line 217) sets
  `kind: "stop"`, `yes: "stop the round"`, `danger: true`. No other confirm changes its fields,
  so they render as neutral `confirm` modals with `y yes`.

## 4. Tests

- `TestComposeBoxKeepsBothSides`:
  - The base holds a styled line with a wide character (`“quoted” 日本 text`), and a box is placed in the middle.
  - `ansi.Strip` of the result equals the stripped base prefix + the stripped box + the stripped base suffix.
  - `lipgloss.Width(result) == width` on every composed row.
- `TestComposeBoxClips`: a box wider than the width is clamped at x=0 and fitted. A box taller than
  the base keeps only the in-range rows. The base slice is unchanged.
- `TestDimLinesStripsStyle`:
  - Set `lipgloss.SetColorProfile(termenv.TrueColor)` for the test, restoring it after.
  - The dimmed output of a line styled with accent contains the shade escape and not the accent escape.
- `TestConfirmAnyKeyCancels`: `q`, `x` and `enter` on a confirmBox close it with a nil cmd. `y` returns onYes.
- Goldens:
  - Regenerate. `confirm-stop.golden` must now show the box over the fleet; read it.
  - Add `confirm-stop-real-132` (132×34): `realFleetReport()`, move the cursor to `spool-db`, then press `x`.
  - Every golden other than confirm goldens must be byte-identical. Any other diff is a halt.

All tests are in `internal/ui` with fakes. No harness, network or cmd/relevo subcommand is involved.

## 5. Working efficiently

- Read frame.go (body), confirm.go:1-240, styles.go and golden_test.go in one step. Make each file's
  edits in one call.
- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`. For goldens, add `-run Golden -update`,
  then run `git diff --stat internal/ui/testdata/`.
- Final: `go mod tidy`, then `gofmt -l $(git ls-files '*.go') internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Do not run
  `make check`: its release guard fails for an unrelated reason.

## 6. Steps

1. styles.go tokens; compose.go (`dimLines`, `composeBox`, `renderModal`) with its three tests. Verify: they pass.
2. The `modalOverlay` interface, the frame.go body branch, the confirmBox fields/`modal`/any-key-cancels, and the stop
   confirm's fields. Verify: `TestConfirmAnyKeyCancels` passes.
3. Goldens (regenerate and add), then the final checks.

## 7. Report

List:
- the files changed;
- the golden diffs, confirming no non-confirm golden changed;
- `confirm-stop-real-132.golden` verbatim;
- whether `go.mod` changed;
- the check results.
