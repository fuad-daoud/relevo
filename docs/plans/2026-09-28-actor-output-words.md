# The actor's output word: REPORT IN for a writer, ARTIFACT IN for a reader

Owner decisions, 2026-09-28 (issue #642). This plan covers exactly one builder
round. Read CLAUDE.md and the file list below before editing. A delivered
round's payload is a **report** only when a writer (builder) produced it; a
reader's (planner, lite-planner, reviewer, researcher) is an **artifact**,
and the artifact has the actor's output label (`plan`, `findings`, `notes`).
Line numbers are HEAD's (`71731989`); if one moved, find the named function.

Commit this plan as `docs/plans/2026-09-28-actor-output-words.md` in this
round's commit (last step). Never delete a test to get green; retarget it.
Goldens are regenerated, never hand-edited. Halt and report instead of
improvising (§6). No issue or PR numbers in code comments.

## 1. System overview

Every round close writes a `store.KindReport` entry and every output word is
derived from the payload **kind** alone, so a delivered planner round reads
`REPORT IN` / `report in` although it wrote a plan. One word is chosen per
actor shape, and shape comes from the store binding — never from a role name:

- **Writer** (shape `writer`, i.e. a builder): the delivered word stays
  byte-identical — `REPORT IN` in the status column, `report in` in the phase,
  the waiting line, the NEEDS YOU reason and the plugin toast.
- **Reader** (shape `reader`): `ARTIFACT IN` in the status column, `artifact
  in` everywhere that word is carried as a phase or reason (wording singular to
  parallel `REPORT IN`).

Two spelling families follow from that, and they are the whole rule:

- a **row word** (a status, a NOW cell, an event cell) is `artifact` for a
  reader, `report` for a writer;
- a **sentence word** (a halt, a stop, a log detail) is `output` for a reader
  — the output label (`findings`) does not agree with `no … was written`, so
  the neutral word wins there — and the tail of a *positive* sentence that
  names the artifact (the nudge, the unmarked close, the missing remote file)
  uses the reader's resolved label, exactly as `internal/relevo/closeline.go`
  resolves it.

JSON key names do not move: `report_in`, `report_round`, `last_kind`, the
event kind `"report"`, the wire attribute `kind="report"`, the gate kind
`exited_no_report` and the db constants all stay. One additive field is added:
`shape` on the status row (and its statusline row), empty and omitted for a
writer, `"reader"` for a reader.

## 2. File structure

New:

```
docs/plans/2026-09-28-actor-output-words.md   this plan, committed in this round
```

Edited by area (test files follow their package's code in the same step):

```
internal/view/status.go            BindingStatus.Shape
internal/view/statusline.go        StatusLineRow.Shape; rowStatus/phase/waiting take the shape
internal/view/statusline_test.go   reader cases (ARTIFACT IN / artifact in / reason); mutation
internal/view/diagnose.go          BuilderDiagnosis.Reader; Detail's closed-round sentence
internal/view/diagnose_test.go     reader case, writer case byte-identical
internal/relevo/status.go          fill Shape beside Role; the broken-row Detail call
internal/relevo/status_test.go     a reader row's status row carries shape and the artifact word
internal/relevo/stop.go            StopResult.Shape; stopPayload's reader arm
internal/relevo/text.go            StopText's reader arms
internal/relevo/reconcile.go       marker-without-artifact sentence
internal/relevo/headless.go        exit note, unmarked payload, denial halt, halt, switch why
internal/relevo/remotefetch.go     the missing-artifact halt's reader arm
internal/relevo/nudge.go           the nudge prompt's reader arm (label + reader output path)
internal/relevo/{stop,text,headless,reconcile,remotefetch,nudge}_test.go, reader_close_test.go
internal/ui/view_fleet.go          NOW cell "artifact ready" for a reader
internal/ui/fleet_group.go         idle NOW cell; the idle group's hint
internal/ui/view_log.go            the reader's event cell and details; a shape lookup
internal/ui/dash/render.go         the done-with-no-artifact outcome word
internal/ui/actions_test.go, view_log_test.go, artifacts_test.go, golden_test.go, dash/d2_test.go
cmd/relevo/contract_test.go        the planner row in the status fixture
cmd/relevo/testdata/contract/*.golden   regenerated (list in §5 step 8)
internal/ui/testdata/round-reader-artifacts-100.golden, -132.golden   regenerated
internal/delivery/push.go          LogRef names --output for a reader
internal/delivery/deliver_agy.go   the oversize sentence
internal/delivery/*_test.go        LogRef and oversize cases
internal/mcp/instructions.go       the prose around kind="report"
internal/mastermind/guide.md       the wait bullet
internal/harness/opencodeplugin/tui.tsx   the toast and the fallback word
docs/plans/2026-09-28-actor-output-words.md  this plan
```

## 3. Type and interface changes

```
internal/view/status.go        BindingStatus gains (beside Role):
    Shape string `json:"shape,omitempty"`      // store.ShapeWriter / store.ShapeReader
internal/view/statusline.go    StatusLineRow gains:
    Shape string `json:"shape,omitempty"`
internal/relevo/status.go      statusRow fills Shape from b.Shape where it fills Role:
    Role: bindingRole(b), Shape: b.Shape,
internal/view/diagnose.go      BuilderDiagnosis gains Reader bool, set in DiagnoseBuilder from
                               b.Shape == store.ShapeReader; Detail() words the closed-round
                               sentence from it. Both call sites already hand it a binding.
internal/relevo/stop.go        StopResult gains Shape string (the stopped binding's), so
                               StopText can choose the word without a second store read.
internal/relevo/stop.go        stopPayload takes the word/shape; its readers are closeStopped
                               and remote_catchup.go:206, both holding the binding.
internal/delivery/push.go      LogRef(name string, e store.LogEntry) -> LogRef(b store.Binding,
                               e store.LogEntry); its two callers (deliver.go, drain.go) hold b.
internal/ui/view_log.go        buildLogEntries(events, hist, revs, actions, since, name, shapes)
                               where shapes func(binding string) string comes from the
                               runtime's store in fetchEventLog; a binding the store no longer
                               holds answers "" and words as a writer. The two regexes widen to
                               both spellings.
internal/harness/opencodeplugin/tui.tsx  one helper: shape === "reader" gives the artifact word,
                               else the report word; the toast and stateWord's fallback both call it.
```

`internal/view` never infers reader-ness: the only reader test is
`b.Shape == store.ShapeReader` (or `ShapeReader` on a row). No role-name string
is compared anywhere in `internal/view`, `internal/ui` or the plugin.

## 4. The words, surface by surface

Row words (writer untouched, reader new):

```
status column        internal/view/statusline.go rowStatus      REPORT IN  ->  ARTIFACT IN
                     (note and outcome suffixes are appended unchanged, tone "report")
phase / middle       statusline.go phase                        report in  ->  artifact in
waiting + reason     statusline.go waiting                      report in  ->  artifact in
                     "report in (unmarked) · halted"            ->  "artifact in (unmarked) · halted"
plugin toast         tui.tsx poller                             report in, delivered to chat
                                                               ->  artifact in, delivered to chat
plugin fallback      tui.tsx stateWord (a document with no status)
                                                               REPORT IN  ->  ARTIFACT IN
fleet NOW cell       internal/ui/view_fleet.go whatAge          report ready  ->  artifact ready
fleet idle NOW cell  internal/ui/fleet_group.go rowNow          reported <age> -> artifact <age>
idle group hint      fleet_group.go groupMetas (group-level, no row in hand; neutral)
                                                               reported, waiting for the next plan
                                                               ->  finished, waiting for the next plan
log event cell       internal/ui/view_log.go                    reported  ->  artifact
dash outcome word    internal/ui/dash/render.go                 no report ->  no output
```

Sentence words:

```
log exit detail      view_log.go                  without a report            ->  without an output
                     (with the code: "without an output (code N)")
log switch why       view_log.go                  exited without a report     ->  exited without an output
stop payload         internal/relevo/stop.go      no report was written.      ->  no output was written.
stop text            internal/relevo/text.go      round closed without a report unless one was on disk
                                                                              ->  without an output …
                                                  round closed without a report
                                                                              ->  without an output
marker close         reconcile.go                 Builder wrote its completion marker for round N
                                                  but wrote no report.        ->  but wrote no output.
headless exit note   headless.go exitEntry        builder exited (code N) without a report
                                                                              ->  without an output
unmarked payload     headless.go                  after writing its report but never confirmed…
                                                                              ->  after writing its <label> …
denial halt          headless.go                  without a report after a permission denial
                                                                              ->  without an output after …
halt                 headless.go                  without a report; see …     ->  without an output; see …
switch why           headless.go                  exited (code N) without a report
                                                                              ->  exited (code N) without an output
nudge prompt         nudge.go                     before writing the report … write the report to %s
                                                                              ->  before writing your <label> …
                                                                                  write your <label> to %s
                                                  (the reader's %s is the reader's own output path —
                                                   Store.OutputPath with the resolved label — not
                                                   ReportPath)
remote halt          remotefetch.go applyCatchUp   closed round N without a report file
                                                                              ->  without its <label> file
diagnose detail      view/diagnose.go Detail       round N report delivered; … -> round N output delivered; …
delivery ref         delivery/push.go LogRef       showCommand(…, "report")   ->  "output"
oversize sentence    delivery/deliver_agy.go      The report is too long to push
                                                                              ->  The output is too long to push
MCP preludes         internal/mcp/instructions.go  every prose noun "report"
                     (kind="report" and the pinned phrases stay)              ->  "output"
injected guide       internal/mastermind/guide.md  "0 closes with a report"
                                                                              ->  "0 closes with the round's output"
```

## 5. Ordered implementation steps

1. **Shape on the row (§3).** `internal/view/status.go`,
   `internal/view/statusline.go`, `internal/relevo/status.go`. Deliverable:
   `BindingStatus.Shape` and `StatusLineRow.Shape` exist and are filled for a
   stored reader binding, omitted for a writer. Verify:
   `go build ./... && go test ./internal/view/ ./internal/relevo/ -run 'Status' -count=1`.
2. **The delivered word (decisions 1 and 2, §4 row words).**
   `internal/view/statusline.go`'s `rowStatus`, `phase`, `waiting`; new reader
   cases in `internal/view/statusline_test.go`: a planner row with a delivered
   report payload gives `ARTIFACT IN` (plus note/outcome variants), `Waiting`
   `artifact in`, the NEEDS YOU reason `artifact in`, `ReportIn` true and
   `ReportRound` set; a builder row keeps `REPORT IN`. Then the mutation: make
   the reader test in `rowStatus` false, confirm the named reader case and the
   contract golden fail, restore. Verify: `go test ./internal/view/ -count=1`.
3. **The cockpit rows.** `internal/ui/view_fleet.go`, `fleet_group.go`,
   `dash/render.go`; set `Shape: store.ShapeReader` on `readerRoundRow` in
   `internal/ui/artifacts_test.go` so the reader goldens carry the reader word;
   retarget `dash/d2_test.go`'s `no report` case and the idle-cell assertions.
   Verify: `go test ./internal/ui/ ./internal/ui/dash/ -count=1`, then
   `go test ./internal/ui -run Golden -update` and read
   `git diff internal/ui/testdata/` line by line (only `reported <age>` ->
   `artifact <age>` in the two reader goldens, nothing else).
4. **The cockpit log view.** `internal/ui/view_log.go`: the shape lookup, the
   reader's event cell and details, the two widened regexes; retarget the seven
   `buildLogEntries` calls in `internal/ui/view_log_test.go` and add a reader
   row and a reader exit note. Verify:
   `go test ./internal/ui/ -run 'Log|Golden' -count=1`.
5. **The close, stop and halt sentences (package relevo).**
   `stop.go` (`StopResult.Shape`, `stopPayload`), `text.go` (`StopText`),
   `reconcile.go`, `headless.go` (`exitEntry` gains the word, the unmarked
   payload, the denial halt, the halt, the switch why), `remotefetch.go`,
   `nudge.go` (reader arm: label, reader output path, marker). Retarget the
   reader-round cases in `internal/relevo/*_test.go`; writer cases must stay
   byte-identical. Verify: `go test ./internal/relevo/ -count=1`.
6. **Diagnose.** `internal/view/diagnose.go` and its test: a reader binding's
   broken-row sentence says `round N output delivered`; a writer's is
   unchanged. Verify: `go test ./internal/view/ -run Diagnose -count=1`.
7. **Delivery refs.** `internal/delivery/push.go` (`LogRef` by binding),
   `internal/delivery/deliver_agy.go`; add a reader `LogRef` case and retarget
   the oversize assertion. Verify: `go test ./internal/delivery/ -count=1`.
8. **The MasterMind-facing texts.** `internal/mcp/instructions.go` and
   `internal/mastermind/guide.md`: prose nouns only — `kind="report"`, the
   `--report` flag in the guide's show bullet, and every phrase
   `internal/mcp/waitcmd_test.go` pins (`new turns`, `background wait`,
   `relevo wait`, `WaitTimeout`, `relevo status --name`, and the ban on
   `run_in_background` in the opencode prelude) stay exactly as they are.
   Verify: `go test ./internal/mcp/ ./internal/mastermind/ -count=1`.
9. **The contract fixture and the goldens.** Add one planner row to
   `seedStatusFixture` (`cmd/relevo/contract_test.go`): name `atlas`, role
   `planner`, `Shape: store.ShapeReader`, one *confirmed* round-1 to-mastermind
   report entry (so `report_in` is true), same mastermind record. Regenerate
   and read the diff; the expected diff is:

   | golden | diff |
   |---|---|
   | `statusline.golden` | one added reader row, word `ARTIFACT IN` |
   | `statusline-json.golden` | the added row with `"shape":"reader"`, `"status":"ARTIFACT IN"`, `"waiting":"artifact in"`, `"actor":"planner"` |
   | `status.golden`, `status-all.golden` | the added `atlas` row, only it carrying `"shape": "reader"` |
   | `status-name.golden` | `status --name webshop` — unchanged |
   | `serve-status.golden` | writer rows only — unchanged |

   Verify: `go test ./cmd/relevo -run Contract -count=1`; then
   `go test ./cmd/relevo -run Contract -update` and
   `git diff -- '**/*.golden'` against that table.
10. **The plugin's delivered word.** `tui.tsx`: one helper reads `row.shape`
    (`"reader"` -> `ARTIFACT IN` / `artifact in`), and both the toast and
    `stateWord`'s old-document fallback use it; tabs, flags and `initialTab`
    are not touched (#644 owns them). Verify: read the diff and confirm the
    writer's strings are byte-identical to today's (`REPORT IN`,
    `report in, delivered to chat`), so
    `scripts/opencode-plugin-smoke.sh`'s assertions 14, 22 and 37 still hold;
    that script needs a live opencode session and is not run in this round.
11. **Full check, e2e, commit.** `make check`, then `make e2e` (this round
    reaches close paths), then `git diff --stat` against §2. Copy this plan to
    `docs/plans/2026-09-28-actor-output-words.md` and commit code, tests,
    goldens and the plan in one commit. No package moves, so the coverage
    baseline is **not** regenerated.

## 6. Error handling and halt clauses

- No new error types, no new failure mode: every change is a word choice on a
  path that already exists. A reader binding whose store record is missing or
  unreadable words as a writer (`Shape` empty), which is today's behaviour.
- `LogRef` keeps returning `""` for a kind that names no section; a reader's
  `KindReport` now answers `--output` and a writer's still answers `--report`.
  `internal/relevo/show.go` already routes `--report` to the reader's output,
  so both refs resolve during the transition.
- The log view's regexes match both spellings, so a note written before this
  round still renders its short detail rather than a clipped raw note; a note
  this round writes matches too.
- Halt and report, do not improvise, if: a listed surface does not match the
  code as §2/§4 describe it; a reader round can render `internal/ui/fetch.go`'s
  report-tab prose (the report tab is absent from `readerTabs`, so it should
  not); a golden diff carries bytes outside §5 step 9's table; going green
  needs `docs/**`, the plugin's tabs or flags, a rename of `report_in`,
  `report_round`, `last_kind`, `kind="report"` or a db constant; a legacy
  round's stored evidence stops answering a read; or a consumer outside the
  plugin (mcp, delivery) stops working.
- If a step's word cannot be reached without a field decision 4 does not
  authorise, stop there and say which surface and which field.

## 7. Scope fence

These stay, and the report names them:

- The writer's words: `REPORT IN`, `report in`, `Report: … --report`, the
  report tab, `reported` for a writer, "without a report" for a writer.
- Every JSON key, kind, constant and wire spelling listed in §1; the store's
  `Binding` format; `db.OutcomeDoneNoReport` and the `exited_no_report` gate
  kind (its wording in `internal/availability` is a candidate's gate label,
  not the round's artifact word, and is not in the issue's sweep).
- `internal/ui/fetch.go`: its three `report` strings live in `fetchReport`,
  which only the writer's report tab calls (a reader's bar is `readerTabs`,
  `internal/ui/fetch.go:38`); no reader round can reach them, so they keep
  their bytes.
- `internal/relevo/escape.go`'s "without a report": `escapeApplies` returns
  false for a reader, so `escapeDiagnosis` is unreachable from a reader round.
- The dry run's `report` path label (`internal/relevo/text.go:91`): not in the
  issue's sweep; it names the `report_path` field, and a reader's dry run line
  would also have to stop naming `NNN-report.md`, which is a separate defect.
- `internal/relevo/send.go`'s reader prompt ("Your final message is your
  <label>: it is saved as …") already carries no `report`; the builder prompt,
  the origin line, `docs/**` (issue #645) and any other issue's surface.
- `scripts/opencode-plugin-smoke.sh` and its `status-*.json` fixtures: every
  row in them is a writer, whose words do not change; a reader row there needs
  a new fixture binding, a show answer and a live opencode session, which this
  round does not have.

## 8. Closed list of what is deleted

Nothing but words is deleted, and only for a reader (a writer's bytes above
are all byte-identical). Everything not on this list survives:

1. `REPORT IN` as the status word of a delivered **reader** payload, replaced
   by `ARTIFACT IN` (`internal/view/statusline.go`).
2. `report in` as a reader row's phase, waiting word and NEEDS YOU reason,
   replaced by `artifact in` (same file).
3. `report in, delivered to chat` and the old-document `REPORT IN` fallback
   **for a reader row** (`internal/harness/opencodeplugin/tui.tsx`).
4. `report ready` and `reported <age>`/`reported` as a reader row's cockpit NOW
   cells, replaced by `artifact ready` and `artifact <age>`/`artifact`
   (`internal/ui/view_fleet.go`, `internal/ui/fleet_group.go`).
5. `reported, waiting for the next plan` as the idle group's hint, replaced by
   `finished, waiting for the next plan` (a group hint has no row to key on).
6. `reported` as a reader row's log event cell, replaced by `artifact`, and
   `without a report` / `exited without a report` as a reader row's log
   details, replaced by `without an output` / `exited without an output`
   (`internal/ui/view_log.go`).
7. `no report` as the dashboard's done-with-no-artifact word, replaced by
   `no output` (`internal/ui/dash/render.go`; the row carries no shape, so the
   word is neutral for both shapes).
8. The reader-reachable `report` sentences in `internal/relevo/{stop,text,
   reconcile,headless,remotefetch,nudge}.go`, `internal/view/diagnose.go`,
   `internal/delivery/{push,deliver_agy}.go`, `internal/mcp/instructions.go`
   and `internal/mastermind/guide.md`, each replaced per §4; the writer arms of
   the same functions are untouched.
9. The reader arm's use of `rt.Store.ReportPath` in the nudge, replaced by the
   reader's own output path with its resolved label.

No test is deleted. Every test that asserted one of these is retargeted to the
new behaviour and keeps every other assertion; a test whose point is a writer
keeps its bytes and is not touched.

## 9. Report must include

- `git diff --stat`, and the residue of
  `git grep -n -i 'report' -- internal/view/statusline.go internal/relevo/{stop,text,reconcile,headless,escape,remotefetch,nudge}.go internal/view/diagnose.go internal/ui/{view_fleet,fleet_group,view_log,fetch,dash/render}.go internal/delivery/{push,deliver_agy}.go internal/mcp/instructions.go internal/mastermind/guide.md internal/harness/opencodeplugin/tui.tsx`
  with one line per hit saying why it stays (a writer arm, a wire spelling, a
  kind, an identifier, a comment) — the round is not done until every hit is
  accounted for.
- The mutation check of step 2, with the failing test's name.
- Every golden whose bytes changed, one line each, matching §5 step 9's table;
  confirmation that no fixture body changed and that
  `serve-status.golden`/`status-name.golden` are byte-identical.
- `make check` and `make e2e` results, and that the coverage baseline did not
  move (no package moved).
- Which stored texts changed shape for a reader (the exit note, the switch why,
  the halt payloads), so a reader knows old rows still render.
- The §7 fence: each deliberate non-change, and the check that no reader path
  reaches `internal/ui/fetch.go`'s report-tab prose.
- Anything from §6 that needed a halt.

## 10. The decisions this plan makes, and its riskiest steps

Decisions taken where the seed's list met the code (all reported, none bent):

- `internal/ui/fetch.go` and `internal/relevo/escape.go` are listed but no
  reader round can reach their `report` text (`readerTabs` excludes the report
  tab; `escapeApplies` refuses a reader); both stay byte-identical.
- The idle group's hint is a package-level group string with no row in hand, so
  its word is neutral (`finished`) rather than per-actor.
- The reader's *sentence* word is `output` and its *row* word is `artifact`;
  the label is used only where the sentence names the artifact and the label is
  at hand (the nudge, the unmarked close, the missing remote file), because
  `view`/`ui`/the plugin carry no label field and decision 4 authorises one
  additive field, `shape`.
- The dry run's `report` label and the plugin smoke fixtures stay (§7).

Riskiest steps, in order: step 9 (the contract fixture and the five goldens —
the only user-visible pin on the new word), step 5 (the reader-reachable
sentences in package `relevo`: every one must keep its writer arm
byte-identical while changing a stored note's spelling), step 4 (the log view's
shape lookup and its two widened regexes), step 10 (the plugin, which has no
test harness this round can run).
