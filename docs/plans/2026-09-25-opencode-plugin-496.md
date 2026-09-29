# OpenCode plugin follow-ups (#496): round keys, tables, the relevo block, transcript markdown

Base: `main` at `211117d` or later. Issue: #496. If any step is impossible as
written or contradicts the code you find, **halt and report**; do not improvise,
and do not bend a test or an assertion to pass.

## 0. Working efficiently

Every model step costs a round trip, so:

- Read the files named in §2 once, in one parallel batch, at the ranges given.
  Do not search for what this plan already locates.
- Make all of one file's changes in a single edit pass where you can.
- Iterate with the focused commands, and fix every error they report before
  you run them again:
  - Go: `go test ./internal/chatlabel/ ./internal/doctor/ ./internal/harness/`
  - Plugin: `bash scripts/opencode-plugin-smoke.sh` (needs tmux and opencode,
    both on this machine; takes about 90 s; captures land in the directory it
    prints).
- Run the full check once, at the end: `make check`. If a hook blocks it, run
  its steps directly (gofmt over the tracked `.go` files, `go vet ./...`,
  `go test -race -count=1 ./...`, the `go mod tidy` check, `sh
  scripts/check-plugin-version.sh`, `sh scripts/check-name.sh`, `shellcheck
  scripts/*.sh`, `scripts/*_test.sh`) and paste each one's result.
- Never dump the environment (`env`, `printenv`, `set`) into a report,
  evidence file or commit. It holds live tokens.

## 1. System overview

relevo's OpenCode TUI plugin (`internal/harness/opencodeplugin/tui.tsx`, which
the Go binary embeds and installs) shows a planner's bindings in the sidebar,
on a fleet page and on a binding page with plan, report, diff, log and
transcript tabs. The owner's live test found five faults:

1. `[` and `]` do not switch rounds.
2. Markdown tables show as raw pipes.
3. The report's closing ```` ```relevo ```` block shows raw.
4. The builder's own text in the transcript is not styled: its `**` and
   backticks show as typed.
5. A PAUSED row with a delivered report shows PAUSED in REPORT IN's colour, and
   the fleet page ranks REPORT IN above PAUSED.

There are also two Go faults from the same work:

6. `relevo doctor` counts OpenCode sessions in the legacy `session` table.
   OpenCode 2.0.14 keeps its sessions in `session_v2`.
7. The chat label is empty on pre-2.0 OpenCode. Its query names `session_v2`,
   a table a pre-2.0 database does not have, so sqlite3 fails.

This round fixes all seven. It changes no Go API, no CLI output other than
doctor's count, and no plugin behaviour outside these seven items.

## 2. Files

```
internal/harness/opencodeplugin/tui.tsx        items 1-5 (the only plugin file touched)
scripts/opencode-plugin-smoke.sh               new steps 05g/05h/05i, assertions 32-37
scripts/testdata/opencode-plugin/relevo        ledger's 2nd report fetch -> show-report-ledger.json
scripts/testdata/opencode-plugin/show-report-ledger.json   NEW fixture (table + relevo block)
scripts/testdata/opencode-plugin/show-transcript.json      one builder text line added
scripts/testdata/opencode-plugin/status-1.json  one row "parked" appended
scripts/testdata/opencode-plugin/status-2.json  the same row appended
internal/doctor/opencode.go                    item 6
internal/doctor/opencode_test.go               item 6 tests
internal/doctor/doctor_test.go                 fakeEnv gains commandFn (item 6)
internal/chatlabel/opencode.go                 item 7: OpencodeLegacyQuery
internal/chatlabel/resolve.go                  item 7: fallback call
internal/chatlabel/chatlabel_test.go           item 7 tests
docs/plans/2026-09-25-opencode-plugin-496.md   this plan, saved verbatim
```

Read these first, in one batch:
- `tui.tsx` in full (1311 lines; the line numbers below are for `211117d`);
- `scripts/opencode-plugin-smoke.sh` lines 80-200 and 290-465;
- `scripts/testdata/opencode-plugin/relevo`;
- `internal/doctor/opencode.go` lines 70-100, `internal/doctor/opencode_test.go` lines 1-90, `internal/doctor/doctor_test.go` lines 20-60 and 120-130;
- `internal/chatlabel/opencode.go`, `internal/chatlabel/resolve.go`, and `internal/chatlabel/chatlabel_test.go` lines 145-260.

## 3. Data structures

### 3.1 Plugin store (tui.tsx, `api.storage.store("relevo", …)`, lines 395-402)

| field | type | meaning |
|---|---|---|
| `rev` | number | unchanged |
| `fleetSelected` | number | unchanged |
| `bindingTab` | string | unchanged |
| `bindingRound` | number | the round the user picked on the binding page. `0` means none was picked, so the page's route default applies. Already exists but is never read today: this is fault 1. |
| `bindingRoundFor` | string | **new**, initial `""`. The binding name `bindingRound` belongs to. A picked round applies only while the page shows that binding. |

### 3.2 `StateWord` (tui.tsx, module level)

`{ word: string; tone: "needs" | "held" | "quiet" | "report" | "none" }`

- `word` is the state word a row shows. It is `""` for a row with no word.
- `tone` picks the colour. See §4.1.

### 3.3 Parsed table (local to the renderer)

- `header: string[]`: the raw cell texts of the header row.
- `body: string[][]`: the raw cell texts of each body row.
- `widths: number[]`: one display width per column.

Every row is padded with `""` cells to the table's column count, which is the
largest cell count of any row.

### 3.4 Fixture row "parked" (status-1.json and status-2.json, appended as the last row)

`{"name":"parked","round":1,"display":"PAUSED","needs_you":false,"report_in":true,"report_round":1,"harness":"opencode","candidate":"opencode/openai/gpt-4o","waiting":"report in","clock":"1h","tokens":"2k tok","last_kind":"report","last_ts":"2026-09-24T19:00:00Z","route":"deliverer"}`

The row is identical in both files, so it raises no toast. It is appended last,
so the fleet indices of webshop (0), ledger (1) and landing (2) are unchanged,
and so is every existing smoke navigation.

## 4. Contracts

### 4.1 `stateWord(row) -> StateWord` and `toneColor(api, tone) -> colour` (module level, next to `initialTab`, tui.tsx ~278-288)

Single responsibility: one rule for a row's state word and its colour, used
by the sidebar, the fleet page and the binding header. The word order is the
Claude status line's (§2 of #479):

1. `row.needs_you` gives `NEEDS YOU` with tone `needs`.
2. Else, if `row.display` is set and is not `ACTIVE`, the word is
   `row.display`. Its tone is `held` for `HELD` and `quiet` for anything else
   (PAUSED, DONE).
3. Else `row.report_in` gives `REPORT IN` with tone `report`.
4. Else the word is `""` with tone `none`.

`toneColor` maps the tones to theme colours:

| tone | colour |
|---|---|
| `needs` | `text.feedback.warning.base` |
| `held` | `text.feedback.warning.base` |
| `report` | `text.feedback.info.base`, falling back to `text.muted` |
| `quiet` | `text.muted` |
| `none` | `text.feedback.success.base` |

The `quiet` choice matches the cockpit, where PAUSED and DONE share the done
style (`internal/ui/styles.go` `stateStyle`).

Call sites:

- **Sidebar**, lines 573-616:
  - `stateText` becomes `stateWord(row).word`.
  - `stateColor` becomes `toneColor(tone)`.
  - The dot colour (line 609) is warning for `needs`, info for `report`, and
    muted otherwise.
  - This removes the PAUSED-in-REPORT-IN colour (fault 5).
- **Fleet rows**, lines 831-877:
  - `state` becomes `stateWord(row).word || "ACTIVE"`. The fleet keeps its
    STATE column's ACTIVE word, but the order is now the §4.1 order, so a
    PAUSED row with `report_in` shows PAUSED.
  - An unselected row's colour is warning for `needs`, info for `report`,
    and muted otherwise.
  - A selected row keeps `interactiveColor`.
- **Binding header**, lines 1168-1177 and line 1231:
  - `display` becomes `stateWord(row).word`.
  - The header colour becomes `toneColor(tone)`.
  - Remove the local `isReportIn` if nothing else uses it.

### 4.2 `openBinding(row, round)` (inside `setup`, next to `showNeedsYouDialog`)

Single responsibility: every way into a binding page opens it the same way.
It takes a status row and a round number, and returns nothing.

- **Effect:** it sets `bindingTab = initialTab(row)`, `bindingRound = 0` and
  `bindingRoundFor = ""` in one `setStore`, then calls
  `api.ui.router.navigate({ type: "plugin", name: "relevo.binding", params: { name: row.name, round } })`.
- **Postcondition:** the page opens on the route's round. No round picked on
  an earlier visit survives.

It replaces the four inline blocks that set `bindingTab` and navigate:

| entry point | lines |
|---|---|
| sidebar row `onMouseDown` | 597-606 |
| the `relevo.next` command | 697-704 |
| fleet enter | 784-792 |
| fleet row `onMouseDown`, `isSelected` branch | 856-864 |

Each call passes the round that call site passes today.

### 4.3 Round selection on the binding page (render, lines 1143-1214 and 1254-1265)

**The round the page shows:**

- If `store.bindingRoundFor === name` and `store.bindingRound > 0`, the round
  is `store.bindingRound`.
- Else it is today's value: `params.round` if defined, else
  `row.report_round || row.round || 1`.
- `currentRouteParams = { name, round }` is set from this value, as today, so
  the poller's live refetch follows the picked round.

**`selectRound(r)`**, a local function in the render: one `setStore` that
sets `bindingRound = r` and `bindingRoundFor = name`.

**`isKey(e, ch)`**, a local predicate: true when `e.name === ch`, or
`e.sequence === ch`, or `e.raw === ch`. OpenTUI may report `[` only in the
key's sequence and not in its name, and this covers both.

**Keys:**

- `[` (tested with `isKey`): the previous round is the largest entry of
  `rounds` that is less than `round`. If one exists, call `preventDefault()`
  and `selectRound(prev)`.
- `]` (tested with `isKey`): the next round is the smallest entry greater
  than `round`. If one exists, call `preventDefault()` and `selectRound(next)`.

Both keys compute their target from the render's `round`. They never
increment a stored value, so the handler stays idempotent while it is attached
to both the outer box and the scrollbox.

Delete these lines as part of this replacement:

- 1192-1198: the `[` branch that assigns `currentRouteParams.round` and
  `s.bindingRound` through `rounds.indexOf(curRnd)`;
- 1202-1208: the matching `]` branch.

**Mouse:** each round label in the round row (lines 1257-1263) gets
`onMouseDown={() => selectRound(rNum)}`.

The `Show keyed` body (line 1272) already keys on `${name}:${round}:${currentTab}`,
so a round change starts the new body at its top. Leave it as it is.

### 4.4 `renderMarkdownLines(text, special?)` (render, replaces lines 967-1043)

Single responsibility: turn markdown text into styled line boxes. The plan and
report tabs call it with no `special`; the transcript tab calls it with
`transcriptSpecial` (§4.6).

- **Input:**
  - `text`: string.
  - `special`: an optional function that takes a line and returns a JSX line,
    or null.
- **Output:** an array of JSX nodes. A line the renderer hides contributes
  nothing.

It becomes an index loop, because a table consumes several lines. The loop
keeps two pieces of state: `inFence` (boolean) and `fenceLang` (string).

```
for i over lines:
  line = lines[i]
  if special and special(line) is not null:
      push it; continue                       # checked first, even inside a fence;
                                              # a special line never toggles the fence
  if line matches ^\s*```(\S*)\s*$:
      if not inFence:
          inFence = true; fenceLang = the captured word
          if fenceLang == "relevo": push a muted line "── relevo " + "─" x max(0, ruleWidth-10)
          else if fenceLang != "": push a muted line holding fenceLang
          # an unlabelled fence pushes nothing
      else:
          inFence = false; fenceLang = ""     # the closing fence pushes nothing
      continue
  if inFence:
      if fenceLang == "relevo": push relevoLine(line)
      else: push a code-coloured line "  " + line (a single space when empty)
      continue
  if isTableRow(line) and i+1 < len and isTableSeparator(lines[i+1]):
      j = i; collect lines while isTableRow(lines[j]) (the separator counts)
      push renderTable(the collected lines); i = last collected index; continue
  # otherwise: today's rule, heading, quote, list, empty and paragraph branches, unchanged
```

**`isTableRow(line)`:** the trimmed line starts with `|`.

**`isTableSeparator(line)`:**

- the line matches `^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`;
- and it contains at least one `|`.

**`relevoLine(line)`:** a line of the relevo status block.

- If the line matches `^\s*([A-Za-z_][\w-]*):\s?(.*)$`, it is a key and a
  value. Trim the value.
- The line is pushed as two text nodes:
  - the key, muted, as `"  " + key + ":"`;
  - then `" " + value`.
- The value's colour:
  - a value of `""`, `[]` or empty renders as the muted text `—`;
  - for the key `status`, `done` is success, `halted` and `blocked` are
    warning, and anything else is base;
  - every other key's value is base.
- A line that does not match is code-coloured, like a code line.
- `—` is U+2014.

### 4.5 `renderTable(lines) -> JSX[]` (render, local)

**Parse:**

- Drop the separator line.
- `splitRow(line)` splits a row into cells:
  - trim the line;
  - remove one leading `|` and one trailing `|` that is not escaped;
  - split on each `|` that is not preceded by `\` and not inside a backtick
    span;
  - trim each cell;
  - unescape `\|` to `|`.
- Pad every row to the column count (§3.3).

**`visible(cell)`** is the text a cell shows once its inline markup is removed:

- `` `x` `` becomes `x`;
- `**x**` becomes `x`;
- `[t](u)` becomes `t (u)`.

**Widths:**

- `widths[c]` is the largest `visible(cell).length` in column c, header
  included.
- Fit: while `sum(widths) + 3 * (n - 1)` is greater than `ruleWidth` and the
  widest column is wider than 3, reduce the widest column by 1. Of equal
  columns, reduce the leftmost first.

**Render:**

- **Header row:** one row box.
  - Each cell is `<text fg=base><b>` holding its visible text, ellipsized to
    the width and padded to it.
  - Cells are joined by a muted `" │ "`.
- **Separator:** one muted line, with each column's width in `─`, joined by
  `"─┼─"`.
- **Body rows:** one row box each.
  - A cell whose visible text fits its width renders as
    `renderInline(cell)`, followed by a base text node of spaces for the
    remaining width.
  - A cell that does not fit renders as base text: `ellipsize(visible, width)`,
    padded.
  - Cells are joined by a muted `" │ "`.
- The table has no outer border.

### 4.6 `transcriptSpecial(line) -> JSX | null` (render, replaces `renderTranscriptLine`, lines 1045-1084)

It keeps today's four styled branches, unchanged: the `●` tool line, the
`⎿ error` line, the `⎿ ok` line and any other `⎿` line.

Delete the final default branch (lines 1079-1083, the plain `<text
fg={baseColor}>{line || " "}</text>` box) and return `null` in its place. A
null sends the line on to the markdown classifier, which fixes fault 4.

The transcript body (lines 1284-1291) renders
`renderMarkdownLines(tabContent, transcriptSpecial)` in place of
`tabContent.split("\n").map(renderTranscriptLine)`.

### 4.7 Doctor session count (internal/doctor/opencode.go, `opencodeSessionCount`, lines 83-97)

It returns `(int, bool)` as today. The steps:

- Run `select count(*) from session_v2`.
- If the command fails, run `select count(*) from session`, which is the
  pre-2.0 table.
- The first query that succeeds and parses as an integer gives the count.
- If both fail, return `(0, false)`, as today.
- Update the doc comment to say so.

**Test seam:** `fakeEnv` in `doctor_test.go` gains a field `commandFn
func(bin string, args ...string) ([]byte, error)`. When it is non-nil,
`fakeEnv.Command` returns its result. When it is nil, today's
`commandOut`/`commandErr` behaviour applies unchanged.

### 4.8 Chat label fallback (internal/chatlabel)

- `opencode.go` adds `OpencodeLegacyQuery(sessionID string) string`, which
  returns `select title from session where id = '<id>'`.
  - The id's single quotes are doubled, as in `OpencodeQuery`.
  - Its doc comment says it is for pre-2.0 databases, which have no
    `session_v2`.
- `OpencodeQuery` is unchanged.
- `resolve.go`, case `"opencode"`:
  - Run `OpencodeQuery`.
  - If that returns an error, run `OpencodeLegacyQuery` with the same Exec
    and context.
  - If the fallback also errors, return the empty Label.
  - A successful first query never runs the second.

## 5. Pseudocode: the binding page after this round

```
render(relevo.binding):
  name from params; if none -> "no binding selected"
  row = the status row for name
  round = (bindingRoundFor == name and bindingRound > 0) ? bindingRound
          : params.round ?? row.report_round || row.round || 1
  currentRouteParams = {name, round}
  rounds = unique history round numbers, ascending (unchanged)
  sw = stateWord(row); header shows sw.word in toneColor(sw.tone)
  body keyed on name:round:tab:
    plan|report  -> renderMarkdownLines(text)
    transcript   -> renderMarkdownLines(text, transcriptSpecial)
    diff, log    -> unchanged
  keys: tab/shift-tab unchanged; [ ] -> selectRound(prev/next); a -> dialog (unchanged)
  round label click -> selectRound(n)

entry (sidebar click, fleet enter/click, relevo.next) -> openBinding(row, round)
  -> resets bindingRound/bindingRoundFor, sets tab, navigates
```

## 6. Error handling

- The plugin swallows render-time errors as it does today. Nothing new may
  throw from a render:
  - a table with ragged rows is padded;
  - a table with no body rows renders its header and separator only;
  - an unclosed fence runs to the end of the text, code-coloured.
- The doctor count and the chat label never surface an error. Each failure
  ends in "no count" or "empty label", as today.

## 7. Ordered steps

1. **State word (§4.1).**
   - Add `stateWord` and `toneColor`, and rewire the sidebar, fleet and header
     call sites.
   - Verify: grep shows no remaining `isReportIn ? "REPORT IN"` ordering in
     the fleet.
2. **Entry and round selection (§3.1, §4.2, §4.3).**
   - Add `bindingRoundFor` to the store's initial value.
   - Add `openBinding` and replace the four entry blocks.
   - Rewrite the round logic, the keys and the round-label clicks.
3. **Markdown renderer (§4.4, §4.5, §4.6).**
   - Restructure `renderMarkdownLines`.
   - Add the table and relevo-block rendering.
   - Convert the transcript renderer.
4. **Fixtures and smoke (§8).** Run the smoke and fix the plugin until every
   assertion, old and new, passes.
5. **Go items 6 and 7 (§4.7, §4.8)**, with their tests (§8.2). Run the
   focused `go test`.
6. **Full check (§0).** Then run the §9 mutations, restoring after each one.
7. **Save and ship.**
   - Save this plan verbatim at `docs/plans/2026-09-25-opencode-plugin-496.md`.
   - Make one commit: `fix(opencode): [ ] switch rounds; markdown tables, the
     relevo block and transcript text styled; PAUSED colour; doctor and chat
     label read session_v2 and pre-2.0 (#496)`.
   - Push with `git push -u origin <branch>`.
   - Open a PR against main whose body lists the smoke result (all
     assertions), the §9 results and the full-check output.
     Write `Refs #496` in the PR body, not `Closes`: the owner closes #496
     after a live test.

## 8. Tests

### 8.1 Smoke (scripts/opencode-plugin-smoke.sh and fixtures)

**Fixtures:**

- **`show-transcript.json`:** in `Text`, insert the line
  ``**Step 1:** `sleep 120` done`` just before `building next round`.
- **`show-report-ledger.json`:** new. It has the same shape as
  `show-report.json`, with `Name` `ledger`, `Round` 2, `Rounds` 2,
  `Section` `report` and `Missing` false. Its `Text` is exactly:

  ````
  # Report

  ledger r2: checkout flow implementation is ready for review

  | Step | Text | Status |
  | --- | --- | --- |
  | 1 | `sleep 120` | done |
  | 2 | **write** the ledger | done |

  ```relevo
  status: done
  halted_at: ""
  changed_paths: ["ledger.txt"]
  not_done: []
  ```
  ````

- **fake `relevo`:** when `NAME` is `ledger` and `TAB` is `report`, and the
  first-call Missing branch did not fire, print `show-report-ledger.json`
  instead of `show-report.json`. Everything else is unchanged.
- **`status-1.json` and `status-2.json`:** append the §3.4 row.

**New steps.** Add each at the place given, following the file's
`send`/`type_lit`/`capture` helpers:

- **Round keys.** Just after `capture "05d-plan"`. The page is webshop, which
  opened on r3:
  - `type_lit "]"`, sleep 1.5, then `capture "05g-round-next"`;
  - `type_lit "["`, sleep 0.5, `type_lit "["`, sleep 1.5, then
    `capture "05h-round-prev"`.
- **Re-entry.** In "Return to webshop for 06-dialog", after the `Enter` that
  opens webshop and before `send a`: `capture "05i-reenter"`.

**New assertions.** Number them 32-37, in the file's `check_assertion_N`
style, and update the final `PASSED` count:

| # | Capture | Asserts |
|---|---|---|
| 32 | `05f-ledger-after.txt` | a line matching `Step +│ +Text +│ +Status`; a line containing `─┼─`; a line matching `1 +│ +sleep 120 +│ +done`; no line matching `^[[:space:]]*\|` |
| 33 | `05f-ledger-after.txt` | contains `── relevo`, `status: done` and `halted_at: —`; no triple backtick anywhere |
| 34 | `05e-transcript.txt` | contains `Step 1: sleep 120 done`; no `**`; no backtick anywhere |
| 35 | `05g-round-next.txt`, `05h-round-prev.txt`, fake log | 05g's header contains `webshop › r4`; 05h's contains `webshop › r2`; the log has a line containing `show webshop --json --plan --round 4` and one containing `--plan --round 2` |
| 36 | `05i-reenter.txt` | header contains `webshop › r3`: re-entry drops the picked round |
| 37 | `03-fleet.txt` and `02-after-toast.ansi` | 03-fleet's `parked` row contains `PAUSED` and not `REPORT IN`; in 02-after-toast, the SGR run immediately before the sidebar's `PAUSED` differs from the one immediately before ledger's `REPORT IN` |

For assertion 37, take the last escape sequence that precedes the word on its
line. Assert only that the two sequences differ, not what they are.

**Existing assertions 1-31 must pass unchanged.** If one fails because of a
fixture change in this plan, halt and report. Do not edit it.

**Assertion 11:** the fake log still holds only relevo verbs.

**Assertion 13:** the plugin source must not contain the names `parked` or
`ledger.txt`.

### 8.2 Go tests

This round adds no `cmd/relevo` test. None of these spawns a harness or
reaches the network.

- **`internal/doctor/opencode_test.go`**, alongside the existing
  "opencode.db present adds the session count" subtest:
  - **D1:** `commandFn` answers `5` for a query that names `session_v2`.
    Detail contains `5 session(s)`, and the first query ran was the
    `session_v2` one.
  - **D2:** `commandFn` errors on `session_v2` and answers `2` for the
    legacy `session` query. Detail contains `2 session(s)`.
  - **D3:** both queries error. Detail has no `session(s)`.
  - The three existing subtests pass unchanged.
- **`internal/chatlabel/chatlabel_test.go`:**
  - **C1:** `TestOpencodeLegacyQuery` checks the exact SQL for `ses_a'b`,
    with the quote doubled.
  - **C2:** `TestResolveOpencodePre2`:
    - it uses a real fixture DB built with `opencodeFixture`, holding only
      `create table session (id text, title text)` and one row
      `('ses_old', 'Pre-2 title')`;
    - Resolve gives `Pre-2 title`.
  - **C3:** extend `TestResolveOpencode`, or add a test beside it: a fake
    Exec whose first call errors and whose second succeeds. It asserts that
    two calls ran, the second being `OpencodeLegacyQuery`, and that the label
    is the second call's output. The existing success case still asserts
    exactly one call.
  - `TestResolveOpencodeV2Title` passes unchanged.

## 9. Mutation checks (run each one, report pass or fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | the render ignores `store.bindingRound` (always use the route round) | smoke 35 |
| M2 | `openBinding` does not reset `bindingRound`/`bindingRoundFor` | smoke 36 |
| M3 | `isKey` checks `e.name` only | report whether smoke 35 fails. This tells us which field OpenTUI fills. Either result is acceptable; report it. |
| M4 | the table branch is disabled (never call `renderTable`) | smoke 32 |
| M5 | `transcriptSpecial` keeps its old default branch | smoke 34 |
| M6 | `stateWord` checks `report_in` before `display` | smoke 37 |
| M7 | `opencodeSessionCount` queries `session` only | D1 |
| M8 | `Resolve` drops the legacy fallback | C2 and C3 |

Smoke mutations are slow (~90 s each). Run M1, M2, M4, M5 and M6 as single
smoke runs. If a mutation does not make its named check fail, report it. Do
not strengthen the checks beyond this plan.

## 10. Scope check

`git diff --stat` shows only the §2 files. Nothing else in the plugin
changes: not the dialog, the poller, the fetch cache, the log tab, the diff
tab or the key bindings other than `[`/`]`. If the work needs another file,
halt and report.
