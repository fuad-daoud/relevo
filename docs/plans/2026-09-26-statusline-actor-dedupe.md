# Status line: one status slot, the actor named, the clock last (2026-09-26)

## 1. System Overview

Today a binding row in the Claude Code status line says where the round is in **two** places. The middle carries a
phase (`plan sent`, `report in`) and the right carries a state word (`REPORT IN`, `NEEDS YOU`, `PAUSED`). The row also
never names the actor. The clock sits *before* the word, so it moves whenever the word's length changes:

```
○ cl-p3-leaves1   r2 · claude@contabo · report in · 8.8M tok        33m · REPORT IN
○ cl-p3-db        r1 · opencode · plan sent · 7.3M tok                        8m
```

After this round a row has three parts:

- **Middle: identity only.** Round, actor, harness, tokens. It carries a *reason* only when there is one (why a row
  needs you).
- **Status column: the one status.** It is padded to the widest status among the rows, so it lines up.
- **Clock: last.** It is right-aligned in its own column, so it never moves.

```
○ cl-p3-planner-policy  r1 · builder on claude@contabo · 3.4M tok                 plan sent            7m
○ cl-p3-leaves1         r2 · builder on claude@contabo · 8.8M tok                 REPORT IN           33m
○ cl-p3-proc            r1 · builder on opencode · 9.9M tok                       REPORT IN · halted  21m
○ rev-api               r1 · reviewer on claude · 1.1M tok                        plan sent            2m
● cl-p3-hooks-mcp       r1 · builder on claude · round 1 was open -- that work…   NEEDS YOU           32m
```

The status vocabulary has two cases:

- **Attention words** are uppercase and coloured: `NEEDS YOU`, `REPORT IN`, `QUESTION IN`.
- **relevo state words** (`PAUSED`, `DONE`, `HELD`) and **passive phases** (`plan sent`, `no plan yet`, `answered`)
  keep their current case. The phases are lowercase and dim.

The OpenCode plugin renders the same document (`relevo status --line --json`) in its sidebar, fleet page and binding
header, and has the same duplication.

**The rule lives in one place.** `StatusLineRows` (Go) computes `actor`, `status`, `tone` and `reason`. The Go
renderer and the plugin read those fields and do not re-derive them. `waiting` stays in the document unchanged, because
the plugin's notifications and chat placeholder use it.

## 2. File Structure

```
internal/relevo/statusline.go                     StatusLineRow +Actor +Status +Tone +Reason; rowStatus(); phase(); RenderStatusLine layout
internal/relevo/statusline_test.go                new tests (step 2); port expected strings of existing render tests
cmd/relevo/testdata/contract/statusline-json.golden   regenerated
internal/harness/opencodeplugin/tui.tsx           stateWord reads the doc; toneColor +phase; sidebar; fleet columns; rowActor
scripts/testdata/opencode-plugin/status-1.json    rows gain actor/status/tone/reason
scripts/testdata/opencode-plugin/status-2.json    rows gain actor/status/tone/reason
docs/specs/2026-09-24-statusline-redesign-design.md   row layout section/examples updated (if it spells one out)
docs/plans/2026-09-26-statusline-actor-dedupe.md  this plan, copied in as the last step
```

Nothing else changes. If any other file must change, stop and report.

## 3. Data Structures

`StatusLineRow` (`internal/relevo/statusline.go`, `type StatusLineRow struct`) gains four fields. All four are always
emitted (no `omitempty`):

| Field | JSON | Contract |
|---|---|---|
| `Actor` | `actor` | Never empty. It is `b.Role` when that is non-empty, else `"builder"` (a builder binding stores `""`, see `normRole`, `writer_role.go`). `Role` itself is kept unchanged. |
| `Status` | `status` | The row's one status text, from `rowStatus` (§4). It is never empty for a row. |
| `Tone` | `tone` | One of `"needs"`, `"held"`, `"quiet"`, `"report"`, `"phase"`. It picks the colour. |
| `Reason` | `reason` | Middle-segment text explaining the status. Usually `""`. Defined in §4. |

`Waiting`, `Display`, `NeedsYou`, `ReportIn` and the other existing fields keep their values and meanings.

## 4. Interface Definitions

### `phase(b BindingStatus) string` (new, unexported, `statusline.go`)

It returns the bare phase of the last payload. It ignores `b.Detail`, the note and the outcome:

| Last payload | Result |
|---|---|
| nil | `"no plan yet"` |
| plan | `"plan sent"` |
| report | `"report in"` |
| question | `"question in"` |
| answer | `"answered"` |
| anything else | `""` |

`waiting(b)` must keep its exact current outputs, which `TestWaitingReportOutcome` pins. It may call `phase` for its
base words, but only if its outputs stay the same.

### `rowStatus(b BindingStatus, needsYou, reportIn bool) (status, tone string)` (new, unexported)

`needsYou` and `reportIn` are the values `StatusLineRows` already computes. Apply the rules in order. The first match
wins:

1. `needsYou` → `("NEEDS YOU", "needs")`.
2. `b.Display != "" && b.Display != "ACTIVE"` → `(b.Display, "held")` if `b.Display == "HELD"`, else
   `(b.Display, "quiet")`.
3. `reportIn` with a report as the last payload → `"REPORT IN"`, then the report's qualifiers, each prefixed with
   `" · "`, in this order:
   - the note (bare, no parentheses)
   - the outcome, when it is non-empty and not `OutcomeDone`

   The tone is `"report"`. Examples: `REPORT IN`, `REPORT IN · halted`, `REPORT IN · unmarked · halted`.
4. `reportIn` with a question as the last payload → `("QUESTION IN", "report")`.
5. Otherwise → `(phase(b), "phase")`. If `phase(b)` is `""`, use `"--"`. This case covers a report whose push is
   still in flight, which shows as a dim `report in` for under `PendingNeedsYouAfter`.

### Reason (computed in `StatusLineRows`)

- `needsYou` → `waiting(b)`. This returns `b.Detail` when it is set (for example "round 1 was open …"). For a stalled
  report it returns `report in`, which tells you *why* the row needs you.
- otherwise, `b.Detail != ""` → `b.Detail`
- otherwise → `""`

### `RenderStatusLine` (`statusline.go`, `func RenderStatusLine`, ~lines 57-133)

Pseudocode:

```
rows = StatusLineRows(r, now)
nameW   = max rune width of row.Name     (existing)
statusW = max rune width of row.Status
clockW  = max rune width of row.Clock
for each row:
  dot    = existing rule
  mid    = "r" + round + " · " + row.Actor
           + (" on " + row.Harness  if row.Candidate != "")
           + (" · " + row.Reason    if row.Reason != "")
           + (" · " + row.Tokens    if row.Tokens != "")
  colour = tone: needs → ansiNeedsYou, report → ansiReportIn, phase → ansiDim, held/quiet → none (as today)
  right  = colour(pad(row.Status, statusW)) + "  " + padLeft(row.Clock, clockW)
  midW   = columns - (2 + nameW + 2) - 1 - statusW - 2 - clockW
  if midW < 8:  line = dot + " " + name + "  " + mid + " · " + colour(status) + " · " + clock   (unpadded fallback, as today)
  else:         line = dot + " " + pad(name,nameW) + "  " + pad(truncate(mid,midW),midW) + " " + right
```

`round` is the existing rule: `ReportRound` when it is > 0, else `Round`.

Colour codes must wrap only the visible status text. Padding counts runes of the uncoloured text. Add a small `padLeft`
helper next to `pad`. The old `switch` that picked `word`/`colouredWord` goes away, because `row.Status`/`row.Tone`
replace it.

### OpenCode plugin (`internal/harness/opencodeplugin/tui.tsx`)

- **`stateWord(row)`** (~line 342): when `typeof row?.status === "string"`, return
  `{ word: row.status, tone: row.tone }`. Otherwise keep the current body as the fallback for an older binary. Extend
  the `StateWord` tone union with `"phase"`.
- **`toneColor`** (~line 355): add `case "phase":`, which returns `paint(api, "text.muted")`.
- **`rowActor(row)`** (new, next to `stateWord`): returns `row.actor || row.role || "builder"`. Use it at the fleet
  actor (~line 925) and the binding header actor (~line 1503) instead of `row.role || "builder"`.
- **Sidebar** (~lines 687-722). Line A is the dot, the name (ellipsized), then the status in its tone colour, then the
  clock at the far right. `padLine` puts the status plus `" " + clock` right-aligned at width 37, so the clock never
  moves. Line B is `"  r" + displayRound + " · " + rowActor(row) + " on " + (row.harness || "opencode")`, then
  `" · " + row.reason` when it is non-empty, then `" · " + row.tokens` when it is non-empty. Line B is
  `ellipsize(…, 37)` in the muted colour.
  - The dot colour stays: warning for the needs tone, info for the report tone, muted otherwise.
  - Remove the `const lineA = padLine(...)` line if it becomes unused.
- **Fleet page** (~lines 910-940). The header becomes
  `NAME           ACTOR     ON                   RND  TOKENS    STATUS              TIME   REASON`. Each row is:
  - `namePad` (14), `actorPad` (9) and `modelPad` (21), as today
  - `rndPad` (5)
  - `tokens` padded to 9
  - `status` from `stateWord`, ellipsized to 19 and padded to 20
  - `row.clock || "--"` padded to 7
  - `row.reason`, ellipsized to what is left of 96

  Drop the old `STATE`/`NOW`/`TOKENS` layout (`statePad`, `nowCol`, `nowPad`, `tokensCol`). The whole-row colour rule
  is unchanged.
- **Do not touch** the `row.waiting` uses in notifications (~lines 237 and 245) or in the chat placeholder and label
  (~lines 532-567).

## 5. High-Level Pseudocode

```
StatusLineRows(r, now):
  for each binding b:
    ... existing: harness, lastKind/lastTS, toPlannerPayload, pending, stalled, needsYou, reportRound, reportIn ...
    actor          = b.Role or "builder"
    status, tone   = rowStatus(b, needsYou, reportIn)
    reason         = waiting(b) if needsYou else b.Detail
    append row{ ...existing fields..., Actor, Status, Tone, Reason }
```

## 6. Error Handling Strategy

There are no new error paths. All the new code is pure formatting over data that is already loaded. `rowStatus` checks
`reportIn` before it dereferences `b.LastPayload` (non-nil whenever `reportIn`). `phase` handles a nil `LastPayload`.

## 7. Working Efficiently

Each step costs a round trip, so:

- Read `internal/relevo/statusline.go`, `internal/relevo/statusline_test.go`, `tui.tsx` lines 330-370, 640-730,
  880-960 and 1495-1600, and both fixture JSONs in **one** step of parallel reads. Every location is named here, so
  don't search for them again.
- Make all of `statusline.go` in one edit, and all of `tui.tsx` in one edit.
- Focused loop: `go test ./internal/relevo -run 'StatusLine|Waiting|RenderPlanner|RoundClock' -count=1`. Fix every
  failure before the next run.
- Golden: `go test ./cmd/relevo -run Contract -update -count=1`, then again without `-update`.
- Full check, once, at the end: `make check`. Also run `gofmt -l .`. It must print nothing, because `make check`'s
  gofmt step can be vacuous in a worktree.
- This round adds no `cmd/relevo` test. It only regenerates a golden. CI runners have no harness and no network.

If a step is impossible as written or contradicts the code, **stop and report**. Don't improvise. Examples: a named
function is missing, or `origin/main` has already reshaped `statusline.go`.

## 8. Ordered Implementation Steps

**Step 0 — sync.** Run `git fetch origin && git merge --ff-only origin/main`. If it isn't a fast-forward, stop and
report.

**Step 1 — Go** (`statusline.go`). Add the fields (§3), `phase`, `rowStatus` and `padLeft`, fill the fields in
`StatusLineRows`, and rewrite the per-row layout in `RenderStatusLine` (§4). Comments explain *why* only. No issue
numbers, no "used to".
*Done when:* the package compiles.

**Step 2 — tests** (`statusline_test.go`). Depends on step 1.
- New `TestRowStatus`, table-driven over `StatusLineRows`. Assert both `Status` and `Tone`:
  - plan sent → `plan sent`/`phase`
  - no payload → `no plan yet`/`phase`
  - delivered plain report → `REPORT IN`/`report`
  - delivered report, note `unmarked`, outcome `halted` → `REPORT IN · unmarked · halted`
  - delivered report, outcome `done` → `REPORT IN`
  - delivered question → `QUESTION IN`/`report`
  - pending report pushed less than 60s ago on a live deliverer route → `report in`/`phase`
  - stalled report → `NEEDS YOU`/`needs`, with `Reason` `report in`
  - `Display` `PAUSED` → `PAUSED`/`quiet`
  - `Display` `HELD` → `HELD`/`held`
  - `Display` `NEEDS YOU` with `Detail` `"round 1 was open"` → `NEEDS YOU`, with `Reason` `"round 1 was open"`
- New `TestStatusLineRowActor`: `Role ""` → `builder`, and `Role "reviewer"` → `reviewer`.
- New `TestRenderStatusLineOneStatusClockLast`: render three rows at 140 columns: plan sent, delivered report and
  delivered report with outcome halted. Strip the ANSI codes, then assert:
  - each line contains its status exactly once, case-insensitively (`report in` appears once on the report row)
  - every line ends with its clock
  - the clock's last rune sits at the same column on every line
  - the status starts at the same column on every line
  - the middle contains `builder on agy`
- Port the existing render tests to the new layout. Change only the expected strings or positions and delete no test:
  `TestRenderStatusLineAt80`, `…TruncatesAt40`, `…NarrowDropsUsageFirst`, `…UnpaddedWhenTooNarrow`, `…Colours`,
  `…RemoteServer`, `…SharesTheRowRule`, `…WaitingFallthrough`, `…LiveSegment`, `…ClosedRoundTokens`,
  `…LiveWinsOverSpend` and any others that fail. `TestStatusLineRows` gets the new fields in its expectations.
  `TestWaitingReportOutcome` must pass **unchanged**.
- **Mutation checks.** Make each of these breaks, confirm the named test fails, then revert. Say in the report that you
  did.
  - Put the clock back before the status. `TestRenderStatusLineOneStatusClockLast` must fail.
  - Drop rule 3's qualifiers. `TestRowStatus` must fail.
  - Add `" · " + row.Status` back into the middle. `TestRenderStatusLineOneStatusClockLast` must fail.

*Done when:* the focused test command passes.

**Step 3 — contract golden.** Regenerate `cmd/relevo/testdata/contract/statusline-json.golden` with `-update`.
*Done when:* the diff adds exactly `actor`, `status`, `tone` and `reason` to its one row. That row is `needs_you`, so
the expected values are `"status":"NEEDS YOU"`, `"tone":"needs"` and `"reason":"report in"`. The test also passes
without `-update`.

**Step 4 — OpenCode plugin** (`tui.tsx`, §4) and fixtures (`status-1.json`, `status-2.json`). Give each fixture row
`actor`, `status`, `tone` and `reason` values that follow §4's rules. Leave `waiting` as it is.
If `opencode` and `tmux` are on PATH, run `bash scripts/opencode-plugin-smoke.sh` and report the result. Otherwise say
it was skipped.
*Done when:*
- the sidebar, fleet page and header read `stateWord`/`rowActor`
- no rendered surface shows both a status and the phase
- `git diff` shows no change at the notification and placeholder sites (~lines 237, 245 and 532-567)

**Step 5 — spec.** If `docs/specs/2026-09-24-statusline-redesign-design.md` shows a row example or describes the
row's segments, update it to this layout: identity in the middle, one status column, clock last. Include §4's status
vocabulary. Otherwise leave it untouched and say so.

**Step 6 — full check and plan.**
1. Run `make check` and `gofmt -l .`.
2. Copy this plan to `docs/plans/2026-09-26-statusline-actor-dedupe.md`.
3. Commit everything in one commit:
   `feat(statusline): one status column, the actor named, the clock last (status line + OpenCode)`.

*Done when:* `make check` is green, and `git diff --stat origin/main` lists only the files in §2.

## Report

Include:
- the diff stat
- the mutation-check results
- whether the smoke test ran
- whether the spec changed
- the rendered, ANSI-stripped status lines from `TestRenderStatusLineOneStatusClockLast`
