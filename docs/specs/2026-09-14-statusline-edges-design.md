# Status line edges: margin, payload vocabulary, short harness, tty stdin

**Issues:** #155 (Claude Code clips rows at `COLUMNS - 4`), #152 (full
candidate token crowds the row), #154 (the verb blocks on a tty), and the
`drift` vocabulary case noted on #155.
**Amends:** `docs/specs/2026-09-13-statusline-design.md` §3.2, §3.3, §4.4,
§5, §6 -- each amendment is stated below against the section it replaces.
The 2026-09-13 spec stays the design of record; this one is the errata from
the first night of use.
**Depends on:** #151 (merged).
**Status:** draft; plan at `docs/plans/2026-09-14-statusline-edges.md`.

**Amended 2026-09-28 by `docs/plans/2026-09-27-round-vocabulary.md`:** a
round's input is now its prompt; the current words are `prompt sent` and
`no prompt yet`, and the log kind is `prompt`. The sections below stay as
written.
## 1. System overview

`relay statusline` shipped in #151 and worked on first use, with four
things wrong that only a live terminal shows:

1. **The right cell never reached the screen.** Claude Code sets
   `COLUMNS=146` and renders 141 cells of a row before its own `…`
   (measured with a ruler line, 2026-09-14). A row laid out to `COLUMNS`
   loses its last four columns -- `· ACTIVE`. The four are Claude Code's
   chrome in character cells (the fullscreen border and built-in inset),
   not a property of any device; they move with `tui` mode, the `padding`
   setting and Claude Code releases.
2. **The middle column said `drift`.** `Send` writes a `drift` entry right
   after the `plan` entry, so `Last.Kind` is `drift` for the whole round and
   the fallthrough printed the kind word. `pick`, `switch`, `exit`, `diff`
   are the same class: relay's bookkeeping, not something the planner is
   waiting on.
3. **The candidate token is 32 characters.** `agy/google/gemini-3.8-flash-high`
   left the waiting text as `re…` at 60 columns. The canvas showed `agy`.
4. **Typed by hand, the verb hangs.** §4.4 said to drain stdin so Claude
   Code's pipe never blocks; on a tty there is no EOF.

None of these change the row's shape, the planner filter or the store-only
rule.

### Scope boundary

Four fixes, no new verb, no new flag (the #114 freeze). One additive JSON
field on `BindingStatus`. `RenderStatus`, the TUI, `Status`'s behaviour and
the store are untouched. The `padding` setting's extra inset is out of
scope until someone sets it.

## 2. File structure

```
internal/relay/status.go            BindingStatus.LastPayload; statusRow sets it
internal/relay/statusline.go        StatusLineWidth, ShouldDrainStdin, harness
                                    segment, waiting/age over LastPayload
internal/relay/statusline_test.go   tests in §7
internal/relay/status_test.go       one test for LastPayload
cmd/relay/main.go                   cmdStatusline: margin, conditional drain
README.md                           "Status line": margin override, ruler one-liner
docs/specs/2026-09-13-statusline-design.md   pointer to this file in the header
```

## 3. Data structures and type definitions

### 3.1 `BindingStatus.LastPayload` (amends 09-13 §3.2)

```
LastPayload *LastEvent `json:"last_payload,omitempty"`
```

The most recent log entry whose `Kind` is one of `plan`, `report`,
`question`, `answer` -- the four that cross between planner and builder.
Nil when the log has none. `Last` keeps its meaning (the most recent entry
of any kind); `RenderStatus` keeps reading `Last`. Additive: a consumer
that never learned the field sees the document it always did.

### 3.2 Row vocabulary (amends 09-13 §3.3)

The `<waiting>` table's last three rows are replaced. The full table is
now:

| condition | text |
|---|---|
| `Detail != ""` | `Detail` verbatim |
| `HoldText(b) != ""` | `report → planner · ` + `HoldText(b)` |
| `Nudge != nil` | `nudged · ` + `NudgeText(*Nudge)` |
| `LastPayload.Kind == plan` | `plan sent` |
| `LastPayload.Kind == report` | `report in`, then ` (<Note>)` when `Note != ""` |
| `LastPayload.Kind == question` | `question in` |
| `LastPayload.Kind == answer` | `answered` |
| `LastPayload == nil` | `no plan yet` |

There is no other branch: the four kinds are exhaustive by construction of
`LastPayload`.

`<age>` is `AgeText(now.Sub(LastPayload.TS))`; `--` when nil. Age therefore
means "since the last plan, report, question or answer crossed", which is
what the 09-13 decision meant by "last relayed message" before relay's own
entries got in the way.

The middle column's builder cell is the **harness segment** of
`BuilderCandidate`: the token up to its first `/` (`agy`, `claude`,
`opencode`). A token with no `/` is shown whole. Empty stays omitted with
its separator.

### 3.3 Width (amends 09-13 §4.3 precondition and §5)

```
const claudeCodeMargin = 4          -- cells Claude Code's chrome takes from COLUMNS; measured 2026-09-14
env   RELAY_STATUSLINE_MARGIN       -- non-negative integer; overrides the constant
```

```
func StatusLineWidth(columns int, override string) int
```

- `columns <= 0` -> `0` (the renderer's "not under Claude Code" default).
- margin = `override` parsed as a non-negative int when it parses; else
  `claudeCodeMargin`.
- result = `max(columns - margin, 1)`.

`RenderStatusLine` is unchanged: it still lays out to the `columns` it is
given. The subtraction happens in the verb, once.

### 3.4 Stdin (amends 09-13 §4.4)

```
func ShouldDrainStdin(mode os.FileMode) bool     -- mode&os.ModeCharDevice == 0
```

The verb drains stdin only when `ShouldDrainStdin(fi.Mode())` is true for
`os.Stdin.Stat()`. A failed `Stat` counts as "drain" (a pipe is the common
case, and draining a closed pipe returns at once).

## 4. Interface definitions and component contracts

### 4.1 `statusRow` (existing, gains one assignment)

After `row.Last` is set, walk `entries` from the end and set
`row.LastPayload` to the first whose `Kind` is in `{plan, report, question,
answer}`. Precondition and postcondition otherwise unchanged; `Status`'s
existing tests pass unedited.

### 4.2 `RenderStatusLine` (existing, contract narrows)

Reads `LastPayload` instead of `Last` for `waiting` and `age`; renders the
harness segment. Layout, colour, truncation and the unpadded form are as
before.

### 4.3 `cmdStatusline` (existing, two lines change)

```
columns := StatusLineWidth(atoi(COLUMNS), os.Getenv("RELAY_STATUSLINE_MARGIN"))
if fi, err := os.Stdin.Stat(); err != nil || ShouldDrainStdin(fi.Mode()) { drain }
```

Everything else (empty pane, errors to stderr with exit 0, no arguments)
stands.

## 5. High-level pseudocode

Only the changed pieces:

```
statusRow:
    ...row.Last set as today...
    for i from len(entries)-1 down to 0:
        if entries[i].Kind in {plan, report, question, answer}:
            row.LastPayload = &LastEvent{TS, Round, Direction, Kind, Note of entries[i]}
            break

harness(token):
    i := index of "/" in token
    if i < 0: return token
    return token[:i]

waiting(b):
    Detail / HoldText / Nudge as before
    if LastPayload == nil: "no plan yet"
    switch LastPayload.Kind:
        plan:     "plan sent"
        report:   "report in" [+ " (" Note ")"]
        question: "question in"
        answer:   "answered"

cmdStatusline:
    if len(args) > 0: usage error
    if fi, err := os.Stdin.Stat(); err != nil || ShouldDrainStdin(fi.Mode()):
        io.Copy(io.Discard, os.Stdin)
    pane := HERDR_PANE_ID; if "": return
    columns := StatusLineWidth(atoi(COLUMNS), RELAY_STATUSLINE_MARGIN)
    ...as before...
```

## 6. Error handling strategy

Unchanged from 09-13 §6, plus:

| error | handling |
|---|---|
| `RELAY_STATUSLINE_MARGIN` unparsable or negative | the constant; nothing logged |
| `os.Stdin.Stat()` fails | drain, as before this spec |

## 7. Ordered implementation steps

Each step ends with the fmt/vet/`go test -race`/tidy set green; `make` is
intercepted on the laptop, so the plan names the constituents.

1. **`LastPayload`**: field, `statusRow` assignment, one test in
   `status_test.go` -- a log of `plan, drift` has `Last.Kind == drift` and
   `LastPayload.Kind == plan`; a log of `plan, diff, report` has both
   `== report`; an empty log has both nil.
2. **Vocabulary and harness**: `waiting`/`age` over `LastPayload`;
   `harness()`; the existing statusline fixtures move from `Last` to
   `LastPayload`; the fallthrough test loses its `switch` row and gains
   `question in` and `answered`; a fixture with `BuilderCandidate:
   "agy/google/gemini-3.8-flash-high"` renders `agy`. Mutation: make
   `waiting` read `Last` and confirm a `plan, drift` fixture fails.
3. **`StatusLineWidth` and `ShouldDrainStdin`**: table tests
   (`146,""`->`142`; `146,"0"`->`146`; `146,"10"`->`136`; `146,"x"`->`142`;
   `146,"-1"`->`142`; `0,""`->`0`; `3,""`->`1`; char-device mode -> false,
   `0` mode -> true, `os.ModeNamedPipe` -> true).
4. **Verb**: the two lines in §4.3. By hand: `relay statusline` on a tty
   returns at once; `COLUMNS=146 relay statusline </dev/null` prints
   142-wide rows; `RELAY_STATUSLINE_MARGIN=0` prints 146-wide.
5. **Docs**: README "Status line" gains the override and the ruler
   one-liner; the 09-13 spec header gains an "Amended by" line pointing
   here. Planner, after merge: remove the `- 4` from
   `~/.claude/statusline.py`.
