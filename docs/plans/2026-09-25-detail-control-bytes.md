# Round detail: control bytes in a tab's text no longer scroll the screen

Date: 2026-09-25. Worktree: this binding's, on origin/main. This is one round. Commit once at the end. Do not push.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/detail.go`
- `internal/ui/detail_test.go`, or a new `internal/ui/sanitize_test.go`
- `docs/plans/`

## 1. The bug and its cause

The user scrolled the transcript tab of a round detail (`:fleet › <binding> › rN`, the transcript tab). Past a certain
point, the whole screen shifted up: the header lines went off the top, and transcript lines showed above the binding's
status row.

**The cause was confirmed on the real data.** That transcript (binding `oc-ui5`, round 1:
`relevo show oc-ui5 --round 1 --transcript`) contains the output of `strings /usr/bin/opencode`. Its lines 364-472 carry raw
control bytes: `\x00`, `\x01`, `\x04`, `\x06`, `\x0b` (VT), `\x0c` (FF), `\x14`, `\x18` and so on, plus invalid UTF-8.

- **What the width functions see:** `lipgloss.Width` counts a control byte as zero cells, so every width check in the
  cockpit passes.
- **What the terminal does:** it acts on those bytes. VT and FF move the cursor down a line. Others ring the bell, move the
  cursor, or start an escape sequence.
- **The effect:** the frame bubbletea writes is taller than the screen, so the terminal scrolls, and the header goes
  first.

Every tab's body reaches the screen through `bodyOf` (`internal/ui/detail.go:60-81`):
- `plan`/`report` go through `renderMarkdown`;
- `diff` goes through `colourDiff`;
- the transcript goes through `colourTranscript`;
- everything else goes through `st.Render`.

Then `wrapBody` (`:88-93`) wraps the result. The raw `c.body` is untrusted builder output in every case: the plan and report
are files written by the builder or planner, and the diff and transcript are tool output.

## 2. The fix

### 2.1 `func sanitizeText(s string) string` (new, in `internal/ui/detail.go`)

- **Responsibility:** make untrusted text safe to draw. It is called on raw text **before** any styling, so the ANSI
  sequences relevo itself adds afterwards are untouched.
- **Rules, in one pass over the runes:**
  - Invalid UTF-8 bytes become `�` (U+FFFD). Ranging over a string already yields `utf8.RuneError` for them.
  - `\n` is kept.
  - `\r\n` becomes `\n`. A lone `\r` is dropped.
  - `\t` becomes four spaces, which is what lipgloss renders a tab as today, so no existing golden moves.
  - Every other rune in `0x00-0x1F`, `0x7F` (DEL) and `0x80-0x9F` (C1) becomes `�`. That includes `\x1b` (ESC): an escape
    sequence in builder output must not be able to move the cursor or recolour the frame.
  - Everything else is kept as is.
- **Postcondition:** the result contains no rune below `0x20` except `\n`, no DEL and no C1 control.

### 2.2 `bodyOf`

- At the top of the function, after the `!c.loaded` check, apply `c.body = sanitizeText(c.body)`. `c` is a value copy, so
  this is local.
- Also apply it to `c.empty` and to the error text: `c.err.Error()`.
- Nothing else changes.

## 3. Tests

1. `TestSanitizeText`, table-driven:
   - `"a\x0bb"` → `"a�b"`, and `"a\x0cb"` → `"a�b"`;
   - `"a\r\nb"` → `"a\nb"`, and `"a\rb"` → `"ab"`;
   - `"a\tb"` → `"a    b"`;
   - `"a\x1b[2Jb"` → `"a�[2Jb"`;
   - `"a\x00\x06b"` → `"a��b"`;
   - `"a\xffb"` (invalid UTF-8) → `"a�b"`;
   - `"a\u0085b"` (C1 NEL) → `"a�b"`;
   - `"héllo — ✓"` is unchanged, and `"line1\nline2"` is unchanged.
2. `TestBodyOfControlBytesKeepLineCount`:
   - Render the transcript tab: `bodyOf(tabTerminal, tabContent{loaded: true, body: <3 lines, the middle one holding \x0b, \x0c and \x1b[2J>}, true)`,
     then `wrapBody` at width 80.
   - The result must contain no rune below `0x20` except `\n` and the `\x1b` of relevo's own SGR styling. To check, strip
     the SGR sequences first with the package's existing `stripANSI`.
   - The stripped result must also have exactly 3 lines.
   - Do the same for `tabLog`, via the default `st.Render` path, and for `tabDiff`.
3. **Mutation check (report the result):** remove the `\x0b` case from `sanitizeText`, confirm test 2 fails, then restore it.

No test may spawn a harness or reach the network. These are pure functions.

## 4. Steps

1. `sanitizeText` and test 1.
   - **Done when:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Sanitize' -count=1` passes.
2. Wire it into `bodyOf`, and add test 2.
   - **Done when:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1` is all green, with no golden
     changes. If a golden changes, halt and report which one.
3. The full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
4. Copy this plan, `/home/fuad/projects/relevo/docs/plans/2026-09-25-detail-control-bytes.md`, into the worktree's
   `docs/plans/`, and commit:
   - `fix(cockpit): control bytes in a round's text no longer scroll the screen`

## 5. Report

Include:
- the tests added;
- the mutation check's result;
- `git diff --stat origin/main HEAD`.
