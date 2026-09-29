# OpenCode plugin: fixes from the first live walk (#496)

Base: `main` at `6124a79` or later. That commit is PR #499; read
`docs/plans/2026-09-25-opencode-plugin-496.md` and `-496-r2.md` for the
contracts this builds on. If any step is impossible as written or contradicts
the code you find, **halt and report**. Do not improvise, and do not bend a
test or an assertion.

## 0. Working efficiently

- Read these once, in one parallel batch, at the ranges given:
  - `internal/harness/opencodeplugin/tui.tsx` in full (line numbers below are
    for `6124a79`);
  - `scripts/opencode-plugin-smoke.sh` in full;
  - `scripts/testdata/opencode-plugin/relevo`, `history.json`,
    `show-report-ledger.json` and `show-plan.json`.
- Make all of one file's changes in one edit pass where you can.
- Test with `bash scripts/opencode-plugin-smoke.sh` (about 90 s; needs tmux,
  opencode and jq, all present). Fix every failure before the next run.
- Run the full check once at the end: `make check`. If a hook blocks it, run
  its steps directly and paste each result. If you run it through `dev run`,
  also run `gofmt -l $(git ls-files '*.go')` and `sh scripts/check-name.sh`
  locally: on the remote host a worktree's `git ls-files` fails, so those two
  steps pass vacuously there.
- Never dump the environment into a report, evidence file or commit.

## 1. System overview

The planner drove the installed plugin (v0.13.0-66) in a real OpenCode
session, on real bindings. The earlier fixes held: `[`/`]`, tables, the relevo
block, the PAUSED colour, and Mark done and Tell the planner through the
dialog. The walk also found the eight faults below. This round fixes exactly
these eight, **F1-F8**, and nothing else.

| # | Fault seen live | Cause |
|---|---|---|
| F1 | "Send plan file…" and "Tell the planner…" silently do nothing when the typed text contains `a`. The fleet cursor also moves when you press Down inside the dialog. | While a dialog is open, the page underneath still receives every key. On the binding page, `onKey` (1426) is on both the box and the scrollbox; on the fleet page it is the `onKeyDown` at 800. Each typed `a` opens a new action dialog, which dismisses the prompt with `undefined`. Typed `[`, `]` and Tab switch rounds and tabs underneath; Down and Up move `fleetSelected`. |
| F2 | The round row of a new binding named `question` lists `r1 r2`. r2 belongs to an older, unbound binding with the same name and shows "(no report)". | `relevo history --binding <name>` returns every binding that ever had the name. The rounds (1408) are taken from all of those rows. |
| F3 | The `── relevo` rule wraps onto a second line. The `---` rule is one column too wide as well. | `ruleWidth` (943-948) is the terminal width minus 2, but the body is narrower: one column of padding on each side plus the scrollbar column. |
| F4 | In the relevo block, `changed_paths:` shows `—` even though its list items follow on the next lines. | `relevoLine` (1123-1150) treats a blank value as empty without looking at the next line. |
| F5 | A list item that wraps loses the space after its marker, so `- Step 2:` shows as `-Step 2:`. | The marker node holds `-` alone. The space starts the next node, and the wrap drops it. |
| F6 | The action dialog's title reads "question needs you" on a row whose state is REPORT IN. | The title and placeholder at 488-489 hard-code "needs you". |
| F7 | The fleet's "recent" list still shows haiku r2 as `open` after it reported. | `historyCache` is filled once (403) and never invalidated. |
| F8 | "recent" times are UTC (16:03 while the local time is 19:03). | Line 902 uses `toISOString().slice(11, 16)`. |

## 2. Files

```
internal/harness/opencodeplugin/tui.tsx               F1-F8
scripts/opencode-plugin-smoke.sh                      new steps, assertions 39-46
scripts/testdata/opencode-plugin/relevo               history --binding filters by name (jq)
scripts/testdata/opencode-plugin/history.json         rows appended (webshop r1, r2; an older webshop binding's r7)
scripts/testdata/opencode-plugin/show-report-ledger.json   a block-list key in the relevo block
docs/plans/2026-09-25-opencode-plugin-live-fixes.md   this plan, verbatim
```

## 3. Data and contracts

### F1. Keys belong to the dialog while it is open

- Add a module-level `let dialogOpen = false;` next to `currentRouteParams`
  (line 18).
- `showNeedsYouDialog` (479) sets `dialogOpen = true` on entry and resets it
  to `false` in a `finally` that covers the whole function: the select, the
  follow-up prompt or confirm, and the spawn. Every return path resets it.
- The first statement of both page key handlers is "if `dialogOpen`, return
  without acting and without `preventDefault()`":
  - the fleet's `onKeyDown` (800);
  - the binding page's `onKey` (1426).
- Nothing else about the key handling changes.

### F2. The round row shows one binding's rounds

This change is in the binding render, at lines 1403-1412.

- **Sort:** sort `bHistory` by `StartedAt`, newest first. Compare the date
  values, not the strings.
- **Pick:** take the newest row's `BindingID` as `liveID`. A live binding is
  always the newest binding with its name.
- **Rounds:** build `rounds` only from rows whose `BindingID === liveID`. If
  `liveID` is undefined, use every row, which is today's behaviour.
- Everything after that is unchanged: the de-dup, the ascending sort, and the
  `rounds.length === 0` fallback.

### F3. Rule width fits the body

- In the `ruleWidth` block (943-948), the width becomes `terminalWidth - 4`.
  That leaves room for the page's two padding columns, the scrollbar column
  and one spare column.
- The 40-column fallback stays.
- `renderTable`'s fit loop and both rules already read `ruleWidth`, so no
  other change is needed.

### F4. A block-list value is not blank

- `relevoLine(line)` becomes `relevoLine(line, next)`. `next` is the next line
  of the text, or `""` at the end.
- The em dash `—` renders only when the value is empty or `""` or `[]`, and
  `next` does **not** match `^\s*-\s`.
- When `next` is a list item, the key renders alone as its muted
  `"  key:"`, with no value node.
- The one caller, in `renderMarkdownLines`' fence branch, passes
  `lines[i + 1] ?? ""`.

### F5. The list marker keeps its space

In the list branch (1251-1260):

- The marker node's text becomes `marker + " "`.
- The rest is passed to `renderInline` with its leading whitespace removed.
- An item with no text, such as a lone `-`, renders just the marker and its
  space.

### F6. Neutral dialog title

At lines 488-489:

- **title:** `` `${name} · r${round}` ``, plus `` ` · ${word}` `` when
  `stateWord(row).word` is non-empty.
- **placeholder:** `` `${name} · r${round} · ${waiting}` ``.
- The words "needs you" appear in neither.

### F7. History refreshes when its binding changes

- Add a module function `invalidateHistory(name)`. It deletes these keys:
  - `b:${name}` and every key starting with `p:` from `historyCache`;
  - the matching `history:b:${name}` and `history:p:…` entries from
    `failedFetches`;
  - their `storeSigs` channels.
- In `pollStatus`, call it wherever `invalidateBindingBodies(row.name)` is
  called (187).
- Also call it for every row name when the doc's **set of row names** differs
  from the previous doc's. That covers a binding appearing or disappearing,
  such as Mark done removing a row.

### F8. Local clock time

At line 902, the time is the local `HH:MM` of `new Date(h.StartedAt)`,
built from `getHours()` and `getMinutes()`, each zero-padded to 2. A missing
`StartedAt` still gives `--:--`.

## 4. Pseudocode: the dialog guard

```
showNeedsYouDialog(row):
  dialogOpen = true
  try:
    choice = await select(...)        # title per F6
    ... every existing branch, unchanged ...
  finally:
    dialogOpen = false

fleet onKeyDown(e) / binding onKey(e):
  if dialogOpen: return               # the dialog owns every key
  ... unchanged ...
```

## 5. Error handling

- F2, F4 and F7 must not throw on missing fields. They cover history rows
  with no `BindingID` or `StartedAt`, an absent next line, and an empty doc.
- A thrown dialog promise still resets `dialogOpen`, because the reset is in
  `finally`.

## 6. Ordered steps

1. **Fixtures.**
   - **`history.json`:** append three rows, keeping every existing row and
     its order:
     - `{"BindingID":"b_webshop","BindingName":"webshop","Number":2,"StartedAt":"2026-09-24T18:00:00Z","ClosedAt":"2026-09-24T18:20:00Z","Outcome":"reported","BuilderCandidate":"opencode/openai/gpt-4o","ReportOutcome":"completed"}`
     - `{"BindingID":"b_webshop","BindingName":"webshop","Number":1,"StartedAt":"2026-09-24T17:00:00Z","ClosedAt":"2026-09-24T17:20:00Z","Outcome":"reported","BuilderCandidate":"opencode/openai/gpt-4o","ReportOutcome":"completed"}`
     - `{"BindingID":"b_webshop_old","BindingName":"webshop","Number":7,"StartedAt":"2026-09-20T09:00:00Z","ClosedAt":"2026-09-20T09:30:00Z","Outcome":"reported","BuilderCandidate":"opencode/openai/gpt-4o","ReportOutcome":"completed"}`
   - **fake `relevo`:** `history` with `--binding <name>` prints only the rows
     whose `BindingName` is that name. Filter with `jq`, keeping the JSON-array
     output. Every other `history` call prints the whole file, as today.
   - **`show-report-ledger.json`:** in the relevo block, add a
     `commands_run:` line with nothing after the colon, followed by the line
     `  - git status`.
     - Put both after `changed_paths` and before `not_done`.
     - Keep every existing line.
2. **F1-F8** in `tui.tsx`, as specified in §3.
3. **Smoke steps and assertions (§7).** Run the smoke until 1-38 and the new
   ones pass.
4. **Full check (§0).** Then run the §8 mutations, restoring after each one.
5. **Save and ship.**
   - Save this plan verbatim at
     `docs/plans/2026-09-25-opencode-plugin-live-fixes.md`.
   - Make one commit: `fix(opencode): dialog owns its keys; one binding's
     rounds; rules fit; relevo block lists; list spacing; neutral dialog
     title; recent refreshes, local time (#496)`.
   - Push with `git push -u origin <branch>`.
   - Open a PR against main whose body lists the smoke result, the §8 results
     and the full-check output. Write `Refs #496` in it, not `Closes`.

## 7. Smoke changes

**Existing assertions 1-38 must pass unchanged.** If one fails because of this
plan's fixture changes, halt and report.

**New steps.** Use the file's `send`/`type_lit`/`capture` helpers:

- **S1**, right after `capture "05i-reenter"`, on webshop's binding page, and
  before the existing `send a` for 06-dialog:
  - `send a`, sleep 1.5, `send Down`, sleep 0.5, `send Enter` (this chooses
    "Send plan file…"), sleep 1.5;
  - `type_lit "a[b]/plan a.md"`, sleep 0.8, `send Enter`, sleep 2;
  - `capture "05j-sent"`.
  - The existing 06-dialog steps then continue as today.
- **S2**, after `capture "10-palette"` and its `send Escape`:
  - open the fleet with `send C-x; sleep 0.4; send o; sleep 1.0`, then
    `send Up; sleep 0.3; send Up; sleep 0.3`, which puts the cursor on
    webshop;
  - `send a`, sleep 1.5, `send Down`, sleep 0.3, `send Down`, sleep 0.3,
    `send Escape`, sleep 1.0;
  - `capture "11-fleet-after-dialog"`.

**New assertions**, numbered 39-46 in `check_assertion_N` style. Update the
final `PASSED` count.

| # | Asserts |
|---|---|
| 39 (F1) | The fake log has a line exactly `send --name webshop --file a[b]/plan a.md`, and `05j-sent.txt`'s header still contains `webshop › r3`. |
| 40 (F1) | In `11-fleet-after-dialog.txt`, the selected row (the line starting with optional space then `› `) is webshop. |
| 41 (F2) | The round row in `05-binding-report.txt` has no `r7`. Assertion 21's `r1 r2 r3 r4` still holds. |
| 42 (F3) | `05d-plan.txt` has exactly one line made only of `─` and spaces. In `05f-ledger-after.txt`, the line after the one containing `── relevo` is not a line made only of `─`. |
| 43 (F4) | `05f-ledger-after.txt` has a line matching `commands_run:[[:space:]]*$` (no `—`) and a line containing `- git status`. |
| 44 (F6) | `06-dialog.txt` contains `webshop · r3 · NEEDS YOU` and does not contain `needs you ·`. |
| 45 (F7) | The fake log has at least 2 lines starting `history --json --planner`, because status-2 changes rows and the recent list refetches. |
| 46 (F8) | `03-fleet.txt`'s recent list contains the local time of `2026-09-24T20:00:00Z`, computed in the script with `date -d 2026-09-24T20:00:00Z +%H:%M`. |

F5 has no smoke assertion: the smoke's fixtures do not wrap a list item. State
in the report that F5 was checked by reading the code.

## 8. Mutations (smoke; run each one, report pass or fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | remove the `dialogOpen` return from the binding page's `onKey` | 39 |
| M2 | remove it from the fleet's `onKeyDown` | 40 |
| M3 | build `rounds` from every row (drop the `liveID` filter) | 41 |
| M4 | `ruleWidth = terminalWidth - 2` | 42 |
| M5 | `relevoLine` ignores `next` | 43 |
| M6 | never call `invalidateHistory` | 45 |
| M7 | put back the `toISOString` time | 46, if the machine's timezone is not UTC. Report the TZ. |

If a mutation does not make its check fail, report it. Do not strengthen the
checks beyond this plan.

## 9. Scope

`git diff --stat main` shows only the §2 files. Nothing else in the plugin
changes: not the poller's timing, the fetch cache for bodies, the diff or log
tabs, the tables, the state words or the entry points. If the work needs
another file, halt and report.
