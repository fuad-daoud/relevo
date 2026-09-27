lp-a5 round 2 of 2 · summary
# Plan: a reader's summary.md drops the status block; the reader close line names its output label

Two defects in reader rounds (a binding bound with `--actor <reader>`). Both are
contract-level fixes; no stored type changes, no new dependencies.

## 1. System Overview

A reader round closes when its runner exits. relevo takes the runner's *final
message* out of the stream, saves it as `NNN-<actor>/summary.md`, and that file
is the round's report entry (reconcile.go `queueReport` → `writeReaderSummary`).
The intended flow is: the planner reviews that summary and sends it as-is to a
builder with `relevo send --file .../NNN-<actor>/summary.md`.

**Defect 1.** The prompt asks the reader to end its final message with a
`relevo` status block, and relevo saves the message verbatim. A plan handed to a
builder therefore ends with a stray status fence. Fix: the block is parsed first
(exactly where reader blocks are parsed today: `queueReport` in
`internal/relevo/reconcile.go`, through `internal/reporttail`), and only then is
the saved file rewritten without it. The parsed status stays on the report log
entry (`Outcome`, `HaltedAt`, `ChangedPaths`, `CommandsRun`, `NotDone`), which is
where outcome, halted/blocked detection (`internal/relevo/wait.go:99`), the
status line and ingest already read it.

**Defect 2.** Every close payload says `Report: relevo show … --report`, even
for a reader, whose artifact is the summary under the noun its agent gave its
output (`plan`, `findings`, `notes` — `roles.ShippedAgent.Output`, resolved by
`ActorOutput` in `internal/relevo/actoroutput.go`). Fix: one helper builds the
artifact clause; a reader gets `<Label>: relevo show <name> --round <n>
--summary`, a writer keeps `Report: … --report` byte-identical at all four
sites, including the one the task did not name (`headless.go`'s unmarked close).

**Not touched:** the reader prompt's first line and the `readerPrompt` template
(the e2e fake parses that line); the stream file naming `NNN-builder.jsonl`;
writer report files and writer payload bytes; the two close lines that name no
artifact at all (`reconcile.go:357` "…but wrote no report.",
`stop.go:188` "; no report was written.").

## 2. File Structure

```
internal/reporttail/reporttail.go        + StripTail: drop the trailing relevo block (pure)
internal/reporttail/reporttail_test.go   + TestStripTailRemovesTheTrailingBlock
internal/relevo/reconcile.go             queueReport: strip a reader's summary.md after the entry is queued;
                                         closeOnMarker: the finished line is shape-aware
internal/relevo/closeline.go             NEW: artifactClause, closeClause, titleFirst
internal/relevo/closeline_test.go        NEW: TestArtifactClause, TestCloseClauseResolvesTheActorLabel,
                                         TestCatchUpPayloadNamesTheReaderSummary
internal/relevo/actoroutput.go           + readerOutputLabel: the output label of the agent a binding's actor plays
internal/relevo/send.go                  readerPromptFor calls readerOutputLabel (prompt and close line share one label path)
internal/relevo/stop.go                  stopPayload takes the artifact clause; closeStopped computes it
internal/relevo/remote_catchup.go        catchUpPayload takes the artifact clause; applyCatchUpReport computes it
internal/relevo/headless.go              the unmarked-close payload's tail is closeClause
internal/relevo/stop_test.go             clause field on the table + a reader case
internal/relevo/reader_close_test.go     stripped-summary assertions, outcome/payload assertions, a halted case,
                                         a runner-written-summary-with-a-block case
internal/e2e/reader_test.go              expected summary is the final message minus the block; the entry outcome is done
docs/plans/2026-09-26-reader-summary-and-close-line.md   this plan, committed with the change
```

Nothing else changes. If any other file must change to make the tests pass, stop
and report.

## 3. Data Structures & Type Definitions

No stored type changes (`store.Binding`, `store.LogEntry`, `remote.BindingView`
untouched).

The reader close keeps today's entry fields filled from the block parsed
*before* the file is stripped — `Outcome`, `HaltedAt`, `ChangedPaths`,
`CommandsRun`, `NotDone` (reconcile.go:527-534). What changes is only the bytes
of the file the entry's `Path` names. `Outcome` is read back from the entry, not
the file, by `wait.go:99`, by the status line
(`view.BindingStatus.LastPayload.Outcome`) and by ingest
(`internal/ingest/facts.go:73-77`), so the stripped status stays available.

New or changed signatures (contracts in §4):

- `reporttail.StripTail(body []byte) []byte` — new, pure.
- `artifactClause(shape, output, name string, round int) string` — new, pure.
- `closeClause(rt Runtime, b store.Binding, round int) string` — new.
- `titleFirst(s string) string` — new, pure.
- `readerOutputLabel(rt Runtime, b store.Binding) string` — new.
- `stopPayload(how, name string, round int, where string, haveReport bool, clause string) (payload, note string)` — one parameter added.
- `catchUpPayload(b store.Binding, view remote.BindingView, haveReport bool, clause string) (payload, note string)` — one parameter added.

## 4. Interface Definitions & Component Contracts

### 4.1 `reporttail.StripTail` — package `internal/reporttail`

- `func StripTail(body []byte) []byte`
- Single responsibility: return `body` without its trailing `relevo` block.
- Pure: no I/O, no errors, total.
- Postconditions, in order:
  1. empty body → unchanged (nil).
  2. no ` ```relevo ` fence → `body` byte-identical. Use `SplitFenceLines` +
     `FindRelevoBlock`, so "the block" means exactly what the parser means.
  3. the last fence is followed by prose (`FindRelevoBlock`'s reason is
     `"tail: prose after closing fence"`) → `body` byte-identical: that fence is
     not the tail, and cutting it would eat prose. The close's existing reject
     note still explains it.
  4. otherwise (well-formed block, or a fence left unclosed): the lines before
     the opening fence, with trailing blank lines dropped, joined with `"\n"`
     and terminated by exactly one `"\n"`; an empty slice when nothing remains
     (a final message that is only a block). This normalises line endings in the
     kept part to LF, which is the form relevo writes.
- Dependencies: `SplitFenceLines`, `FindRelevoBlock`, `strings`, `bytes` — all
  already in the package.

### 4.2 The strip in `queueReport` — `internal/relevo/reconcile.go`

- Where: immediately after `delivery.Queue` (reconcile.go:539-541) returns nil,
  before the `stopped` block (reconcile.go:543).
- What: when `b.Shape == store.ShapeReader`, compute
  `reporttail.StripTail(body)` and rewrite `path` with it only when the bytes
  changed (`os.WriteFile(path, stripped, 0o644)`).
- Why *here* and not in `writeReaderSummary` (the save-time vs read-time
  decision): the tail parse (reconcile.go:402) and the injection scan
  (reconcile.go:411) must keep reading exactly the bytes they read today, and
  the report entry must keep the outcome. Stripping inside `queueReport`, after
  those reads and after the entry is queued, means the outcome survives a close
  that fails later and is retried on the next tick — a retry still finds the
  block on disk. Stripping at save time would need the parsed tail threaded
  through every close path, and a retry would see a stripped file and classify
  the round `unstructured`.
- Stripping at read time was rejected: the file handed to a builder by
  `relevo send --file` is the file on disk, so the block must be gone from disk.
- Applies to a summary the runner wrote itself too: after any close, a reader's
  summary.md never ends with the block. A runner-written summary with no block
  stays byte-identical.
- Write failure: `slog.Warn("reader summary not stripped", "binding", …, "round", …, "err", …)`, then continue — a strip never fails a close, the rule `writeReaderSummary` and `removeReaderScratch` already follow.
- Writers: not read, not written (`b.Shape != store.ShapeReader` guards it).

### 4.3 `artifactClause`, `closeClause`, `titleFirst` — new file `internal/relevo/closeline.go`

- `func artifactClause(shape, output, name string, round int) string` — pure:
  - reader: `titleFirst(output) + ": " + showCommand(name, round, "summary")`;
    an empty `output` becomes `defaultOutput` ("notes") so the clause is never
    `": relevo show …"`.
  - writer: `"Report: " + showCommand(name, round, "report")` — the literal word
    "Report", not the writer agent's output label ("report" today, but a custom
    writer agent may differ), so the bytes cannot drift.
- `func closeClause(rt Runtime, b store.Binding, round int) string` —
  `artifactClause(b.Shape, readerOutputLabel(rt, b), b.Name, round)`. The label
  is resolved only for a reader, so a writer close never loads config for a noun
  it will not print.
- `func titleFirst(s string) string` — the first byte upper-cased; `""` stays
  `""`. Labels from `agentsrc` match `[a-z][a-z0-9-]{0,23}`, so ASCII is enough
  and a one-line comment says why.
- Single responsibility of the file: name a round's artifact the way its close
  payload should.

### 4.4 `readerOutputLabel` — `internal/relevo/actoroutput.go`

- `func readerOutputLabel(rt Runtime, b store.Binding) string`
- The output label of the agent `bindingRole(b)` plays: the definition from
  `bindingSpec(rt, b, b.Builder.Kind)` when it resolves, else the actor name
  itself, passed to `actorOutput`. This is exactly what `readerPromptFor`
  (send.go:810-819) computes inline today.
- Contract: never errors, never panics; an unknown role falls back to the actor
  name and then to `defaultOutput`.
- `readerPromptFor` then calls it, so the label the prompt shows the reader and
  the label the close line prints can never drift.

### 4.5 The four close-payload sites

Let `closeClause(rt, b, round)` be the clause from §4.3. Writer bytes must stay
identical; reader examples assume the reviewer actor → `Findings`.

| site | writer (unchanged) | reader |
|---|---|---|
| reconcile.go:348-350 | `The runner finished round 1. Report: relevo show webshop --round 1 --report` | `The runner finished round 1. Findings: relevo show reader-bind --round 1 --summary` |
| stop.go:184-189 (`stopPayload`, haveReport branch) | `The runner was stopped (killed) for round 1. Report: … --report` | `The runner was stopped (killed) for round 1. Findings: … --summary` |
| remote_catchup.go:163-169 (`catchUpPayload`, finished branch) | `The runner finished round 1 on zen. Report: … --report` | `The runner finished round 1 on zen. Findings: … --summary` |
| headless.go:749-752 (unmarked close) | `Builder exited (code 0) after writing its report but never confirmed completion (no 001-done). Report: … --report.` | same sentence, `Findings: … --summary.` |

- `stopPayload`'s `haveReport == false` branch and `catchUpPayload`'s stopped
  branch keep their present bytes (`"; no report was written."`); the new
  `clause` parameter is unused there.
- The outcome annotation at reconcile.go:421-443 prefixes on
  `"The runner finished round %d"`, which the reader line keeps, so a halted
  reader still reads
  `The runner finished round 1 -- halted at "step 2". Findings: … --summary`.
- `catchUpPayload` keeps its purity: the caller resolves the clause. A reader
  binding is local-only today (`internal/serve/bindings.go:164-169` refuses
  one), so that branch is contract consistency, not a live path.

## 5. High-Level Pseudocode

```
queueReport (reader):
    body                     = read(path)                       # NNN-<actor>/summary.md
    tail, ok, reject         = reporttail.ParseWithReason(body) # unchanged; still reads the block
    outcome                  = tail.Status when ok else unstructured
    scan                     = scanForInjection(body)           # unchanged, raw bytes
    payload                  = annotate(payload, outcome, tail.HaltedAt)   # unchanged
    entry                    = report entry (Outcome, HaltedAt, lists, payload)
    delivery.Queue(entry)                                       # log entry now durable in the tx
    stripped                 = reporttail.StripTail(body)
    if stripped != body: write(path, stripped)                  # warn on failure, never fatal
    ... round advances exactly as today

StripTail(body):
    lines            = SplitFenceLines(body)
    open, close, why = FindRelevoBlock(lines)
    if open < 0 or why == "tail: prose after closing fence": return body
    kept             = lines[:open] with trailing blank lines dropped
    return "" when kept is empty, else join(kept, "\n") + "\n"

artifactClause(shape, output, name, round):
    if shape == reader:
        return titleFirst(output or "notes") + ": " + showCommand(name, round, "summary")
    return "Report: " + showCommand(name, round, "report")

closeClause(rt, b, round):
    return artifactClause(b.Shape, readerOutputLabel(rt, b), b.Name, round)

readerOutputLabel(rt, b):
    actor      = bindingRole(b)                       # "" means "builder"
    definition = spec.Definition when bindingSpec(rt, b, b.Builder.Kind) succeeds, else actor
    return actorOutput(rt, actor, definition)

closeOnMarker's finished line:
    "The runner finished round <n>. " + closeClause(rt, b, b.Round) + gateSuffix
```

## 6. Error Handling Strategy

- No new error type, category, or exit code. `StripTail`, `artifactClause`,
  `titleFirst` and `readerOutputLabel` are total functions.
- The strip write is best-effort: a failed rewrite logs
  `reader summary not stripped` and the close proceeds. Same rule as
  `writeReaderSummary` and `removeReaderScratch`.
- The outcome/halted/blocked classification is computed before the strip and is
  unchanged, so `relevo wait` (exit 3), the status line and ingest keep working
  from the report entry.
- Halt the round and report — do not improvise — if: a line range in §8 does not
  match the code; a reader payload line comes out containing `--report`; or a
  writer payload byte changes anywhere (existing expectations:
  reconcile_test.go:591, 776, 1351; stop_test.go:78, 90).

## 7. Working Efficiently

- Read once, from these locations; do not re-search:
  `internal/relevo/reconcile.go` 336-360, 386-415, 519-556;
  `internal/relevo/stop.go` 180-228; `internal/relevo/remote_catchup.go` 125-182;
  `internal/relevo/headless.go` 736-762; `internal/relevo/send.go` 806-820;
  `internal/relevo/actoroutput.go` 1-40; `internal/reporttail/reporttail.go` 180-230;
  `internal/relevo/reader_close_test.go` 1-150 and 371-410;
  `internal/relevo/stop_test.go` 60-110; `internal/e2e/reader_test.go` 30-140.
- The edits are independent per file: batch them as parallel tool calls in one
  step, one edit call per file (the new files are one `write` each).
- Focused commands, one invocation per package; fix every reported error before
  the next run:
  - `go test ./internal/reporttail/ -run TestStripTail -count=1`
  - `go test ./internal/relevo/ -run 'TestReaderClose|TestReaderRound|TestStopPayload|TestArtifactClause|TestCloseClause|TestCatchUpPayload' -count=1`
  - `go test ./internal/e2e/ -run TestHeadlessE2EReaderRound -count=1`
  Baseline on this tree, already run: all three ok.
- Full check once at the end: `make check`, then `gofmt -l .` (must print
  nothing).
- No subagents, no network, no harness: fixtures only (CI has neither).
- Style, enforced by `make check`: a comment says why and never cites history
  (no issue numbers, no spec sections, no "round N"); functions at most 70
  lines, non-test files at most 600; `gofmt` clean.
- Headless: never pause for a decision. If a step is impossible as written or
  contradicts the code, halt and report.

## 8. Ordered Implementation Steps

**Step 1 — `StripTail` + its test (§4.1).**
- `internal/reporttail/reporttail.go`: insert `StripTail` between
  `SplitFenceLines` (ends line 224) and `listOf` (line 226). No import change
  (`strings` and `bytes` are already imported).
- `internal/reporttail/reporttail_test.go`: append
  `TestStripTailRemovesTheTrailingBlock` pinning: no fence (identical); a
  well-formed trailing block (exact bytes before the fence, one trailing `\n`);
  a block-only body (empty); prose after the closing fence (identical); an
  unclosed fence (cut to the end); two blocks (only the last goes); extra blank
  lines before the fence (dropped); a CRLF body (kept part is LF).
- Deletes: nothing.
- *Done when:* the reporttail focused command is green.

**Step 2 — strip a reader's summary at close (§4.2).**
- `internal/relevo/reconcile.go`: in `queueReport`, insert the reader branch
  between line 541 (the closing `}` of the `delivery.Queue` error check) and
  line 543 (`if stopped {`). Comment says why: the parse above is the only
  reader of the block, and the file the planner reads must not carry it.
- *Done when:* `go build ./...` is clean.

**Step 3 — pin the reader summary and its status (§4.2).**
- `internal/relevo/reader_close_test.go`:
  - after `readerCloseFinal` (lines 17-19) add
    `readerCloseSummary = "The review is done.\n"` with a one-line comment.
  - `TestReaderCloseWritesSummaryFromTheFinalMessage` (117-152): line 135's
    comparison becomes the stripped constant (and the message says so); line 143
    inverts — the saved summary must have **no** parseable relevo block; add
    `entry.Outcome == reporttail.OutcomeDone` and a payload assertion:
    `strings.Contains(e.Payload, "The runner finished round 1. Findings: relevo show reader-bind --round 1 --summary")`
    and `!strings.Contains(e.Payload, "--report")`.
  - after `TestReaderCloseKeepsARunnerWrittenSummary` (ends 188) add two tests:
    `TestReaderCloseStripsARunnerWrittenSummaryWithABlock` (write the runner's
    own summary ending in a block; after the close the file is the stripped
    bytes and `entry.Outcome` is `done`) and
    `TestReaderCloseKeepsTheHaltedStatusOutOfTheSummary` (final message with
    `status: halted`, `halted_at: "step 2"`; the file has no block, the entry's
    `Outcome`/`HaltedAt` carry the status, the payload contains
    `The runner finished round 1 -- halted at "step 2".`, and
    `WaitOutcome(b, entries, 1, …)` returns `WaitHalted`).
  - `TestReaderRoundWaitsForExitAfterMarker` (371-410): lines 402-403 compare
    against the stripped constant.
- Deletes (closed list): item 1 below.
- *Done when:* the relevo focused command is green.

**Step 4 — mutation check for fix 1.**
- Mutation A (the strip itself): comment out the new reader branch in
  `queueReport`; run the relevo focused command; confirm
  `TestReaderCloseWritesSummaryFromTheFinalMessage` fails on the summary bytes.
  Restore.
- Mutation B (the ordering): in `queueReport`, assign
  `body = reporttail.StripTail(body)` right after `os.ReadFile` (line 391),
  before the parse; run the focused command; confirm the outcome assertions in
  `TestReaderCloseWritesSummaryFromTheFinalMessage` (or the halted test) fail,
  because the status is gone before it is parsed. Restore.
- Both restored, rerun the focused command green. If either mutation passes,
  the tests do not pin the behaviour — halt and report.

**Step 5 — the clause helpers + the shared label (§4.3, §4.4).**
- New `internal/relevo/closeline.go`: `artifactClause`, `closeClause`,
  `titleFirst`.
- `internal/relevo/actoroutput.go`: append `readerOutputLabel` after
  `actorOutput` (ends line 40).
- `internal/relevo/send.go`: `readerPromptFor` (810-819) replaces its inline
  `definition`/`bindingSpec` block with `readerOutputLabel(rt, b)`; the rendered
  prompt must not change by one byte.
- New `internal/relevo/closeline_test.go`: `TestArtifactClause` (writer; reader
  with `plan` → `Plan: … --summary`; reader with `""` → `Notes: … --summary`)
  and `TestCloseClauseResolvesTheActorLabel` (reviewer → `Findings`, researcher
  → `Notes`, `Role: ""` → `Report: … --report`, an unknown reader role → `Notes`
  and no panic).
- Deletes (closed list): item 2 below.
- *Done when:* `go test ./internal/relevo/ -run 'TestArtifactClause|TestCloseClause' -count=1` is green and `internal/relevo/send_test.go` still passes.

**Step 6 — the four payload sites (§4.5).**
- reconcile.go:349: `fmt.Sprintf("The runner finished round %d. %s", b.Round, closeClause(rt, b, b.Round)) + gateSuffix`.
- stop.go:184: add the `clause string` parameter; use it in the `haveReport`
  branch (line 186). stop.go:214: pass `closeClause(rt, b, stoppedRound)`.
- remote_catchup.go:163: add the `clause string` parameter; line 167 passes it
  to `stopPayload`; line 169 becomes
  `fmt.Sprintf("The runner finished round %d on %s. %s", n, server, clause)`.
  Line 133 passes `closeClause(rt, b, view.ClosedRound)`.
- headless.go:749-752: the payload's tail becomes
  `closeClause(rt, b, b.Round) + "."` (the `Report: %s.` format slot becomes
  `%s.`).
- `internal/relevo/stop_test.go` (74-105): add a `clause` field to the table;
  the two haveReport cases keep their exact `wantPayload` and get the writer
  clause; add a reader case (`clause: "Findings: relevo show reader-bind
  --round 1 --summary"`, want
  `The runner was stopped (killed) for round 1. Findings: relevo show reader-bind --round 1 --summary`);
  update the call at line 103.
- New `TestCatchUpPayloadNamesTheReaderSummary` in `closeline_test.go`: a reader
  binding with `Builder.Server = "zen"`, `view.ClosedRound = 1`,
  `haveReport = true`, clause from `closeClause` → the finished line of §4.5;
  and `view.Stopped = "killed"` → the stopped line.
- *Done when:* the relevo focused command is green, including every existing
  writer payload expectation.

**Step 7 — mutation check for fix 2.**
- Mutation: in `artifactClause`, drop the reader arm (always return
  `"Report: " + showCommand(name, round, "report")`). Run the relevo focused
  command; confirm the reader payload assertions fail (the reader close test's
  payload check, the new stop_test case, the catch-up test). Restore.
- The inverse is already pinned: making readers keep `--report` is the same
  mutation, and making writer clauses reader-shaped fails
  reconcile_test.go:591/776/1351 and stop_test.go:78/90, which must not be
  edited.
- Rerun the focused command green. If the mutation passes, halt and report.

**Step 8 — the e2e reader round (§2).**
- `internal/e2e/reader_test.go`: after `fakeReaderFinal` (34-45) add
  `fakeReaderSummary` (its bytes minus the block, ending
  `…artifact directory.\n`); line 134-135 compares the summary against
  `fakeReaderSummary`; assert `entry.Outcome == "done"` and that `entry.Payload`
  contains `Findings: relevo show e2e-reader --round 1 --summary`. The sealed
  summary assertion (155-158) stays: it checks the text survives sealing.
- *Done when:* `go test ./internal/e2e/ -run TestHeadlessE2EReaderRound -count=1` is green.

**Step 9 — full check, commit, report.**
- `make check` and `gofmt -l .`; fix anything the round caused before committing.
- Copy this plan to `docs/plans/2026-09-26-reader-summary-and-close-line.md`,
  then one commit with the code, the tests and the plan:
  `fix(a5): a reader's summary.md drops the status block; its close names the output label`.
- *Done when:* `make check` is green and `git diff --stat HEAD~1` lists only the
  files in §2.

### Closed list of what is deleted (everything not on this list survives)

1. `internal/relevo/reader_close_test.go:143-145` — the assertion that the saved
   summary's relevo block parses; it is inverted, not dropped (the block must be
   gone).
2. `internal/relevo/send.go:813-816` — the inline `definition := actor` /
   `bindingSpec` block in `readerPromptFor`, replaced by `readerOutputLabel`.

No test is deleted. Every modified test keeps every other assertion it had. The
line edits at reader_close_test.go:135-136 and 402-403 are changed expectations
(the stripped constant) for the same behaviour, not removed assertions.

### Report

Include: the diff stat; both mutation results (A/B for fix 1, the artifact
mutation for fix 2) with the failing test name and message; the four reader
payload lines as produced; `make check`'s result; and any file listed in §2 that
did not need a change.
