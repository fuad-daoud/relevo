# Status line: this planner's builders, one row each, under the Claude Code prompt

**Issue:** #122. Third of the 2026-09-13 Claude Code research pass, with
#123 (plugin) and #124 (MCP server and channel); the only one that adds a
verb.
**Depends on:** nothing open. Reads `Report` as `status --json` already
emits it; adds no store field.
**Amends:** `docs/design.md` "Read surfaces" (the `~/.claude/statusline.py`
sentence becomes `relay statusline`); README (a "Status line" section with
the settings snippet); the #114 command-surface freeze, by exactly one verb
(§1, scope boundary).
**Design record:** the row anatomy was settled on a canvas
(claude.ai/code/artifact/7faf7517-5a15-42cc-82ce-6c54b669d5b9) against
Claude Code's own subagent list; the decisions below are the ones taken
there.
**Amended by:** docs/specs/2026-09-14-statusline-edges-design.md (§3.2, §3.3, §4.4, §5, §6).
**Status:** draft; plan to follow at `docs/plans/2026-09-13-statusline.md`.

**Amended 2026-09-28 by `docs/plans/2026-09-27-round-vocabulary.md`:** a
round's input is now its prompt; the current words are `prompt sent` and
`no prompt yet`, and the log kind is `prompt`. The sections below stay as
written.
## 1. System overview

A planner running in Claude Code learns that a builder reported, stalled or
needs a decision by remembering to run `relay status`. Claude Code's
`statusLine` setting runs a command on every turn and, with
`refreshInterval`, on a timer while the session is idle, and renders each
line of its stdout as a row under the prompt. This design adds `relay
statusline`, a verb whose output is one row per binding *this* planner owns,
in the shape of Claude Code's subagent list:

```
○ api     r3 · agy · plan sent                                12m · ACTIVE
● client  r1 · opencode · builder pane gone                    4m · NEEDS YOU
○ docs    r2 · agy · report → planner · quiet 23s of 1m0s     23s · HELD
```

Five decisions, each taken against an alternative:

1. **A verb, not a script.** Rendering lives in Go beside `RenderStatus`,
   tested as a pure function in `internal/relay`, versioned with the fields
   it reads. A shell script over `status --json` would be freeze-safe but
   would carry the planner filter, `COLUMNS` padding and truncation in
   untested shell.
2. **This planner's bindings only.** Rows are the bindings whose stored
   planner endpoint is the pane the command runs in: `$HERDR_PANE_ID`, the
   same identity `add`, `bind`, `fork` and `ask` already take as "the
   calling pane". Not cwd, not every binding on the machine. A binding
   whose planner is gone (orphaned) matches no pane and appears on no
   status line, which is correct.
3. **Store only.** `Status()` calls `herdr agent list`; at `refreshInterval:
   1` that is a probe per second per Claude Code session. The status line
   builds its rows from the store alone: state, round, clocks, `Detail` and
   the last log entry are all there. Live pane status (`working`/`idle`),
   foreign agents and sub-agent coverage are not shown.
4. **Age is time since the last relayed message** (`Last.TS`). There is no
   "entered this state at" stamp and this design does not add one.
5. **Nothing to show prints nothing.** Outside herdr, with no owned
   binding, or on any error, stdout is empty. A status line is not where an
   error message goes; `relay status` is.

Right alignment is real, not aspirational: Claude Code captures the
command's stdout, so no tty is available, but it sets `COLUMNS` and `LINES`
to the terminal's size before running it (statusline docs, "Sizing output to
the terminal"). The renderer takes the width as an argument.

### Scope boundary

One verb, `statusline`, read-only, no flags. It is the single exception to
the #114 freeze (step 1, in effect since 2026-09-12) and the spec says so
here so the exception is deliberate. No install verb: the settings entry is
documented, the user pastes it (Q5 of #122, decided). No row cap and no
`↓ N more`: a `statusLine` is plain stdout, an arrow could never expand
anything, and the planner filter already bounds the rows to what one
session owns. No OSC 8 links in this round. No header row. No change to
`Status`, `RenderStatus`, `status --json`, the TUI or the store.

## 2. File structure

```
internal/relay/statusline.go        PlannerStatus (store-only report for one pane),
                                    RenderStatusLine (pure), AgeText, the row
                                    vocabulary in §3
internal/relay/statusline_test.go   tests in §7
internal/relay/status.go            Status split: ListAgents call stays, row building
                                    moves to buildReport(ctx, rt, bindings, agents)
                                    so PlannerStatus can call it with agents == nil
cmd/relay/main.go                   case "statusline": cmdStatusline
README.md                           "Status line" section: the settings snippet
docs/design.md                      "Read surfaces": statusline.py sentence replaced
```

## 3. Data structures and type definitions

### 3.1 Inputs the verb reads

| source | name | use |
|---|---|---|
| env | `HERDR_PANE_ID` | the planner pane; empty means "not in herdr", print nothing |
| env | `COLUMNS` | terminal width; unset or unparsable means 80 |
| stdin | Claude Code's session JSON | read and discarded; nothing in it is needed (`cwd` is not the scope) |

### 3.2 `Report` fields consumed

No new fields. `RenderStatusLine` reads, per `BindingStatus`: `Name`,
`Round`, `Display`, `BuilderCandidate`, `Detail`, `Pending` (+`Hold`),
`Nudge`, `Last` (`TS`, `Kind`, `Direction`, `Note`). `Gated` and
`DoneHidden` are ignored.

### 3.3 Row vocabulary

A row is four cells: dot, name, middle, right.

| cell | content | rule |
|---|---|---|
| dot | `○` | dim (245) for `ACTIVE` and `HELD` |
| | `●` | amber (214) bold for `NEEDS YOU` |
| name | `Name` | padded to the longest name in the report (`ValidName` caps at 32) |
| middle | `r<Round> · <BuilderCandidate> · <waiting>` | `BuilderCandidate` omitted with its separator when empty |
| right | `<age> · <STATE>` | `STATE` is `Display`, coloured as the TUI colours it: 42 for `ACTIVE`, 214 bold for `NEEDS YOU`, plain for `HELD` |

`<waiting>` is the first of these that applies, and it reuses the text
`RenderStatus` already produces so the two never say different things:

| condition | text |
|---|---|
| `Detail != ""` | `Detail` verbatim |
| `HoldText(b) != ""` | `report → planner · ` + `HoldText(b)` |
| `Nudge != nil` | `nudged · ` + `NudgeText(*Nudge)` |
| `Last != nil` and `Last.Kind == plan` | `plan sent` |
| `Last != nil` and `Last.Kind == report` | `report in`, then ` (<Note>)` when `Note != ""` |
| `Last != nil`, any other kind | the kind word verbatim (`question`, `answer`, `switch`, ...) |
| `Last == nil` | `no plan yet` |

`<age>` is `AgeText(now.Sub(Last.TS))`; `--` when `Last == nil`.

### 3.4 `AgeText`

```
func AgeText(d time.Duration) string
```

Truncating, coarse, at most two units: `0s`..`59s`, `1m`..`59m`, `1h 0m`,
`1h 12m`, `27h 4m`. Negative durations render as `0s`. Days are not
introduced: a builder that has been quiet for a day reads better as `26h`
than as `1d`, and the status line is not a log.

### 3.5 Colour

256-colour SGR, the TUI's numbers (`internal/ui/styles.go`), reset after each
coloured span:

| role | sequence |
|---|---|
| dim | `\x1b[38;5;245m` |
| active | `\x1b[38;5;42m` |
| needs you | `\x1b[1;38;5;214m` |
| reset | `\x1b[0m` |

Colour is applied after layout: widths are computed on plain strings and the
escapes are wrapped around the finished cells, so a colour code never counts
as a column.

## 4. Interface definitions and component contracts

### 4.1 `PlannerStatus` (new, `statusline.go`)

Single responsibility: the `Report` for one planner pane, from the store
alone.

```
func PlannerStatus(ctx context.Context, rt Runtime, pane string) (Report, error)
```

Preconditions: `pane != ""` (the verb checks; the function returns an empty
report for `""` rather than every binding, so a mistake fails closed).
Postconditions:

- `Bindings` holds exactly the stored bindings with `Planner.PaneID == pane`
  and `State != done`, sorted by name, each built by `buildReport`'s row
  builder with `agents == nil`, so `PlannerStatus`/`BuilderStatus` read
  `gone`, `Foreign` is empty and `PlannerPane` is the stored pane id.
- `rt.Herdr` is never called. This is a contract, not an optimisation: the
  test in §7 step 3 fails if it is.
- `Gated` is populated as `Status` populates it (a ledger read, not herdr).
- Errors: `rt.Store.List`, `ReadLog`, `PendingForPlanner` errors, wrapped.

Dependencies: `rt.Store`, `rt.Now`, `rt.LedgerPath`/`rt.Candidates` (for
`Gates`), the process table via `headlessStatus` for headless rows (a
`/proc` read, not herdr).

### 4.2 `buildReport` (refactor of `Status`, `status.go`)

```
func Status(ctx, rt) (Report, error)
    bindings := rt.Store.List()
    agents   := rt.Herdr.ListAgents(ctx)
    return buildReport(ctx, rt, bindings, agents)

func buildReport(ctx, rt, bindings []store.Binding, agents []herdr.Agent) (Report, error)
    -- the loop, sort and Gates call that Status has today, verbatim
```

`Status`'s contract is unchanged; every existing test passes without edits.
`statusRow` already tolerates an `agents` slice that contains no match
(`FindAgent` returns `false`), so `nil` needs no special case.

### 4.3 `RenderStatusLine` (new, `statusline.go`)

Single responsibility: the text Claude Code displays.

```
func RenderStatusLine(r Report, now time.Time, columns int) string
```

Preconditions: none; `columns <= 0` is treated as 80.
Postconditions:

- `len(r.Bindings) == 0` returns `""` -- not `"\n"`, so the verb prints
  nothing.
- Otherwise one line per binding in `r.Bindings` order, each terminated by
  `\n`, laid out per §3.3 and §5.
- Every line's visible width is `<= columns`. When the columns cannot fit
  `dot + name + 1 + right` (fewer than ~24 columns) the row is emitted
  unpadded as `dot name middle · right` and may exceed `columns`; Claude
  Code truncates. This is the only case a line may be wider than the
  terminal.
- Pure: no I/O, no clock other than `now`, deterministic for a given input.

### 4.4 `cmdStatusline` (new, `main.go`)

```
case "statusline": return cmdStatusline(args)

func cmdStatusline(args []string) error
```

Preconditions: none. Refuses arguments (`usage: relay statusline`).
Postconditions:

- `HERDR_PANE_ID` empty: prints nothing, returns `nil`.
- Otherwise `newRuntime()`, `PlannerStatus(ctx, rt, pane)`,
  `RenderStatusLine(rep, rt.Now(), columns)`, printed to stdout verbatim.
- Any error: nothing on stdout, the error on stderr, **exit 0**. Claude Code's
  behaviour on a non-zero exit is undocumented, and a stale row is worse
  than an empty one; the message on stderr is for a human who runs the verb
  by hand.
- stdin is drained and ignored so a pipe never blocks the caller.

Dependencies: `newRuntime` (constructs the herdr client; the client is never
used).

## 5. High-level pseudocode

```
cmdStatusline:
    pane := env HERDR_PANE_ID
    if pane == "": return
    columns := atoi(env COLUMNS) or 80
    drain stdin
    rt := newRuntime()                 -- on error: stderr, return nil
    rep := PlannerStatus(ctx, rt, pane) -- on error: stderr, return nil
    print RenderStatusLine(rep, rt.Now(), columns)

PlannerStatus(rt, pane):
    if pane == "": return empty
    all := rt.Store.List()
    mine := [b for b in all if b.Planner.PaneID == pane and b.State != done]
    return buildReport(ctx, rt, mine, nil)

RenderStatusLine(r, now, columns):
    if no bindings: return ""
    if columns <= 0: columns = 80
    nameW := max(len(b.Name))
    for each b:
        dot, dotColour := "○", dim   ; if Display == "NEEDS YOU": "●", needsYou
        middle := "r" + Round
                  + (" · " + BuilderCandidate if non-empty)
                  + " · " + waiting(b)              -- §3.3 table
        right  := age(b) + " · " + Display
        leftW  := 2 + nameW + 2                     -- "○ " name "  "
        midW   := columns - leftW - 1 - len(right)
        if midW < 8:
            line := dot + " " + Name + "  " + middle + " · " + right
        else:
            middle = truncate(middle, midW)          -- "…" as the last rune
            line := dot + " " + pad(Name, nameW) + "  " + pad(middle, midW) + " " + right
        colour dot per dotColour; colour the Display word per state
        emit line + "\n"

waiting(b):
    if Detail != "":        return Detail
    if HoldText(b) != "":   return "report → planner · " + HoldText(b)
    if Nudge != nil:        return "nudged · " + NudgeText(*Nudge)
    if Last == nil:         return "no plan yet"
    switch Last.Kind:
        plan:   "plan sent"
        report: "report in" + (" (" + Note + ")" if Note != "")
        other:  string(Last.Kind)

age(b):
    if Last == nil: "--" else AgeText(now - Last.TS)
```

## 6. Error handling strategy

There is one consumer and it cannot show an error usefully, so the strategy
is: fail to silence, say why on stderr.

| error | where | handling |
|---|---|---|
| `HERDR_PANE_ID` unset | verb | not an error; empty stdout, exit 0 |
| `COLUMNS` unset/garbage | verb | 80 |
| `newRuntime` fails (config unreadable) | verb | stderr, empty stdout, exit 0 |
| store list/log/pending read fails | `PlannerStatus` -> verb | wrapped, then as above |
| headless pid unreadable | `headlessStatus` (existing) | already degrades to `unknown`; row still renders |
| terminal narrower than a row | renderer | unpadded row, Claude Code truncates |

Nothing is logged: the verb runs every second and a log line per tick is
noise. Observability is `relay status` in a shell, which shows the same
store with the herdr half added.

## 7. Ordered implementation steps

Every step ends with `make check` green. Steps 1-3 are `internal/relay`
only; the CI rule (no `cmd/relay` test may reach herdr) is satisfied by
step 3's contract that `PlannerStatus` never calls herdr, and step 4 adds no
CLI test.

1. **`AgeText`** in `statusline.go` with a table test: `0`, `59s`, `60s`
   -> `1m`, `59m59s` -> `59m`, `1h` -> `1h 0m`, `27h4m` -> `27h 4m`, `-5s`
   -> `0s`.
   Verify: the table passes; `gofmt -l .` is empty.

2. **`RenderStatusLine`** with `now` and `columns` as arguments. Tests, each
   asserting on the plain text after stripping SGR sequences and separately
   on the presence of the exact colour prefix for the `NEEDS YOU` row:
   - empty report -> `""`.
   - three-row fixture (active with `Last` plan, `NEEDS YOU` with `Detail`,
     held with `Pending.Hold`) at `columns: 100` -> golden string; every
     line has visible width `<= 100` and the `right` cell ends at column
     100.
   - the same fixture at `columns: 40` -> middles end in `…`, widths
     `<= 40`.
   - `columns: 0` renders as 80.
   - `columns: 20` -> the unpadded form.
   - nudge fixture -> `nudged · quiet 23s of 1m0s`; report with note
     `unmarked` -> `report in (unmarked)`; `Last == nil` -> `no plan yet`
     and age `--`.
   Mutation check: delete the `Detail` branch of `waiting` and confirm the
   `NEEDS YOU` golden fails.
   Verify: tests pass; no test imports `herdr`.

3. **`buildReport` split and `PlannerStatus`.** Extract the body of `Status`
   after `ListAgents` into `buildReport`; `Status` becomes the two-call
   function in §4.2. Add `PlannerStatus`. Tests with the existing fake
   store: two bindings on pane `%1`, one on `%2`, one DONE on `%1` -> only
   the two live `%1` rows, sorted; `pane == ""` -> empty. Contract test:
   the fake herdr's `ListAgents` panics or fails the test if called, and
   `PlannerStatus` runs against it.
   Mutation check: make `PlannerStatus` call `Status` instead of
   `buildReport` and confirm the contract test fails.
   Verify: every existing `status_test.go` test passes unedited.

4. **`cmdStatusline`** in `main.go`, the `case "statusline"` dispatch, the
   usage line. Behaviour per §4.4, including exit 0 on error. No CLI test
   (CI rule; the logic is all in steps 2-3). Manual check inside herdr:
   `relay statusline` prints this pane's rows; `HERDR_PANE_ID= relay
   statusline` prints nothing; `COLUMNS=60 relay statusline` narrows.
   Verify: `make check`; `relay statusline | wc -l` equals the number of
   live bindings this pane owns.

5. **Docs.** README "Status line" section with the snippet and the two
   preconditions (`relay` on Claude Code's `PATH`; Claude Code started
   inside a herdr pane so `HERDR_PANE_ID` is inherited):

   ```json
   "statusLine": { "type": "command", "command": "relay statusline", "refreshInterval": 1 }
   ```

   `docs/design.md` "Read surfaces": replace the `statusline.py` sentence
   with `relay statusline`. Note the freeze exception in the #114 comment
   trail when the PR merges.
   Verify: the snippet pasted into `~/.claude/settings.json` shows rows in
   a Claude Code session running under herdr; a session outside herdr
   shows no relay rows and no error.
