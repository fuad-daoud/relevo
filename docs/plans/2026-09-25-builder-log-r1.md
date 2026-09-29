# builder.log round 1: the stream fixes (no layout change)

Date: 2026-09-25. Base: origin/main (b37a60a6 or later).
Spec: docs/specs/2026-09-25-builder-log-design.md (§1 items 2, 3, 5; §4.1; §5 round 1).
This is round 1 of 2. `NNN-builder.log` is **still written** exactly as today, and every
reader keeps reading it. Round 2 switches the layout. Do not start round 2 work: nothing
changes about stderr, markers, readers of the log, serve, or the remote mirror.

**Stop rather than improvise.** If a step is impossible as written, if the code contradicts
a fact stated here, or if a test outside the fenced list (§7.3) fails, halt and report
what you found. Do not bend a test to make it pass.

## 1. System overview

This round fixes three bugs the spike measured on real data.

1. **agy's usage-limit text is dropped from the render.**
   - agy's `result` event carries the reason in a string field `result.error`, e.g.
     `"Individual quota reached. … Resets in 2h38m7s."`. `renderAgy` prints only
     `result: ERROR`.
   - Rendering `error: <first line>` lets a scan of the rendered stream catch 5/5 real
     limits. Round 2 depends on this.
2. **A mid-round switch re-renders the whole stream with the new harness kind.**
   - `switchBuilder` installs a fresh Endpoint from `resolveBuilder` (bind.go:653),
     losing `StreamRound`, `StreamOffset` and the rest.
   - `startProcess` then resets the cursor to 0 (headless.go:234-238), and the drain
     renders the old harness's lines again with the new kind.
   - Real rounds got 1,151 junk `[type]` lines and 193 duplicated lines.
   - The fix has two parts:
     - the switch keeps the stream cursor;
     - relevo records which harness kind wrote which byte range (stream segments), and the
       drain renders each line with its segment's kind.
3. **`relevo status` reads the exit code from the log.**
   - `headlessStatus` calls `ExitCode(…, e.LogPath)` (headless.go ~1006). The reconcile
     path reads the stream (~616).
   - Status must read the stream too.

A small related fix: the drain must take `StreamSessionID` only from the *current*
process's bytes (offset ≥ `StreamStart`). Otherwise an undrained old line could set it once
the cursor is carried over.

## 2. File structure

```
internal/transcript/agy.go                 renderAgy: result.error line
internal/transcript/agy_test.go            (or the package's existing render test file) N1
internal/transcript/testdata/agy-error-results.jsonl   NEW fixture (§7.2 N1)
internal/store/types.go                    Endpoint.StreamSegments + StreamSegment type
internal/store/format.go                   BindingFormat 5 doc (never stamped)
internal/store/testdata/binding-shape.golden   regenerated with -update
internal/relevo/headless.go                startProcess (segments), drainStream + drainFile (per-offset kind, session guard), headlessStatus (exit code from stream)
internal/relevo/switch.go                  switchBuilder keeps the stream cursor (carryStream)
internal/relevo/bind.go                    the rebind at ~363 uses carryStream when it is the same round (see §4.4)
internal/relevo/headless_test.go           N3-N7
internal/relevo/limit_test.go              N2
internal/relevo/switch_test.go             N8 (or wherever switchBuilder tests live; find with grep)
docs/specs/2026-09-25-builder-log-design.md   the spec, verbatim (step 8)
docs/plans/2026-09-25-builder-log-r1.md       this plan, verbatim (step 8)
```

## 3. Data structures

**`StreamSegment`** (internal/store/types.go, next to Endpoint):

- `Start int64 json:"start"`: the byte offset in the round's builder stream where this
  process's output begins. It equals the stream's size when the process was spawned
  (`StreamStart`).
- `Kind string json:"kind"`: the harness kind of that process, i.e. `Endpoint.Kind` at
  spawn.

**`Endpoint.StreamSegments []StreamSegment json:"stream_segments,omitempty"`**

- Place it directly after `StreamStart`.
- Doc comment: "one entry per process spawned in StreamRound, in spawn order; the drain
  renders each stream line with the Kind of the last segment whose Start ≤ the line's
  offset. Empty for a round started before segments existed; the drain then uses Kind.
  Belongs to the round's file like StreamRound and StreamOffset: a mid-round switch keeps
  it, and a later round starts a new list."
- Update the existing comment on `StreamRound`/`StreamOffset` (types.go ~118-123) so it
  names `switchBuilder`'s carry-over (§4.3) as the reason a switch keeps them.

**BindingFormat** (store/format.go):

- Bump the constant to 5.
- Extend its doc: "Format 5 adds `builder.stream_segments`, never stamped: an older relevo
  that drops it renders a switched round with the endpoint's kind, which is the behaviour
  before format 5."
- `recordFormat` does **not** change: segments never raise the record's format. Mention
  StreamSegments (format 5) beside RemoteLive and StreamStart in its doc comment.
- Regenerate the golden with the flag the test declares
  (`go test ./internal/store/ -run <the format golden test> -update`). Verify that the
  only golden change is the new `…stream_segments[].start` / `…stream_segments[].kind`
  lines (under builder, consults[].endpoint and planner).

## 4. Interfaces and contracts

### 4.1 `renderAgy` (internal/transcript/agy.go, `case "result":` ~28-47)

- After the existing `result: <status>` line and **before** `response`, add:
  - if `msg := str(r["error"]); msg != ""`, append `"error: " + oneLine(msg)`.
- Everything else in the case is unchanged: the denied lines, the status line, the
  response.
- `oneLine` caps the line at `maxArg` bytes with a first-line cut (transcript.go:125).
- Update the file's doc comment if it lists what `result` renders.

### 4.2 Segment kind lookup (internal/relevo/headless.go, near drainStream)

`func segmentKind(segs []store.StreamSegment, off int64, fallback string) string`

- Returns the Kind of the last segment with `Start <= off`.
- Returns `fallback` when there is none, i.e. segs is empty or off is before the first
  Start.
- It is pure.

### 4.3 The stream cursor survives a switch

**`carryStream(from, to store.Endpoint) store.Endpoint`** (headless.go):

- It copies `StreamRound`, `StreamOffset`, `StreamStart` and `StreamSegments` from
  `from` into `to`, and returns `to`.
- It is pure.

**switch.go:150:** `b.Builder = ep` becomes `b.Builder = carryStream(b.Builder, ep)`.

**`startProcess`** (headless.go:233-270):

- When `b.Builder.StreamRound != b.Round`: in addition to today's
  `StreamRound, StreamOffset = b.Round, 0`, set `StreamSegments = nil`.
- After `StreamStart` is computed (~239-243), append
  `store.StreamSegment{Start: b.Builder.StreamStart, Kind: b.Builder.Kind}`.
- When the last segment already has the same Start, replace it instead of appending. A
  failed spawn that is retried must not leave two segments at one offset.
- Nothing else in startProcess changes. `LogPath` is still `BuilderLogPath`.

### 4.4 The rebind at bind.go ~363

- `if rebinding { b.Builder = builder …` installs a fresh Endpoint too. Read the
  surrounding function (bind.go ~194-391, `resume`) and decide which case applies.
- **Case 1:** the rebind can happen while the round's stream is still the current round's
  (`b.Builder.StreamRound == b.Round`). Use `b.Builder = carryStream(b.Builder, builder)`.
- **Case 2:** the rebind only happens between rounds, or it clears the round. Leave it
  unchanged, and say which case it was, with the lines that show it.
- Anything else: halt and report.

### 4.5 The drain renders per segment (headless.go drainStream ~330-352, drainFile ~358-395)

**`drainFile` render callback:**

- Signature `func(line []byte) []string` becomes `func(off int64, line []byte) []string`.
  `off` is the absolute byte offset of the line's first byte in `src`.
- `drainFile` knows its start offset and walks the lines of `data[:end]`, so it passes
  `start + position of the line in data`.
- `drainFile` has one caller (drainStream). If grep finds another, halt.

**`drainStream`'s callback:**

- Kind: `kind := segmentKind(b.Builder.StreamSegments, off, b.Builder.Kind)`.
- Session id: set `StreamSessionID` from `transcript.SessionID(kind, line)` only when it
  is still "" **and** `off >= b.Builder.StreamStart`.
- Return `transcript.Render(kind, line)`.
- Everything else in drainFile is unchanged: the cursor, partial lines, one write,
  never failing the tick.

### 4.6 Status exit code from the stream (headless.go headlessStatus ~977-1013)

- Replace `rt.Runner.ExitCode(ctx, handleOf(e), e.LogPath)` with
  `rt.Runner.ExitCode(ctx, handleOf(e), rt.Store.BuilderStreamPath(b.Name, b.Round))`.
  This matches the reconcile path at ~616.
- The tail (`logTail(e.LogPath, statusTailLines)`) is **unchanged** in this round.

## 5. Pseudocode

```
startProcess(b):
  if b.Builder.StreamRound != b.Round: StreamRound=b.Round; StreamOffset=0; StreamSegments=nil
  StreamStart = size(stream)
  segments += {StreamStart, b.Builder.Kind}   (replace if the last one has the same Start)
  ... spawn (unchanged)

switchBuilder(b):
  ep = resolveBuilder(...)                     (fresh endpoint)
  b.Builder = carryStream(b.Builder, ep)       (cursor + segments survive)
  ... startRound -> startProcess appends the new segment at the current stream size

drainStream(b):
  drainFile(log, stream, StreamOffset, func(off, line):
     kind = segmentKind(StreamSegments, off, b.Builder.Kind)
     if StreamSessionID == "" and off >= StreamStart: StreamSessionID = SessionID(kind, line)
     return Render(kind, line))

headlessStatus: ExitCode(stream path), not the log path
renderAgy result: ... "result: ERROR", "error: <first line of result.error>", response
```

## 6. Error handling

No new errors.

- `carryStream` and `segmentKind` are pure.
- A spawn failure after the segment was appended leaves a segment for a process that
  never ran. That is harmless: nothing was written at that offset, and the next spawn
  replaces it (same Start).

## 7. Ordered implementation steps

### 7.0 Working efficiently

- Read these in one parallel batch, and do not re-search what this plan locates:
  - internal/transcript/agy.go (whole)
  - transcript.go:100-140
  - the transcript package's test files (list them)
  - store/types.go:100-140
  - store/format.go (whole)
  - store/format_test.go:1-60
  - relevo/headless.go:225-275, 318-400 and 970-1015
  - relevo/switch.go:90-175
  - relevo/bind.go:190-391
  - `grep -n "func Test" internal/relevo/headless_test.go | grep -i -E "drain|switch|status"`
- Make every change to a file in one edit call.
- Iterate with these focused commands, fixing every error before the next run:
  - `go test ./internal/transcript/ -count=1`
  - `go test ./internal/store/ -count=1`
  - `go test ./internal/relevo/ -run 'Drain|Switch|Status|Limit|StartRound|Segment|Carry|Headless' -count=1`
- The full check runs once at the end (step 7). `make check` may be blocked by a hook on
  this machine. If it is, run its steps directly and say so:
  - `gofmt -l $(git ls-files '*.go')` must print nothing. **Run it, and paste its empty
    output.**
  - `go vet ./...`
  - `go test -race -count=1 ./...`
  - `sh scripts/check-plugin-version.sh`
  - the `go mod tidy` diff check.
- A cmd/relevo test must not run a subcommand that spawns a harness or reaches the
  network. This round adds no CLI test.

### 7.1 Steps

1. **agy error line.** §4.1, with N1 and N2.
2. **Store field and format.** §3, with the golden regenerated.
3. **segmentKind and carryStream.** §4.2 and §4.3, with N3 and N4.
4. **startProcess segments.** §4.3, with N5.
5. **Drain per segment.** §4.5, with N6 and N8.
6. **Status exit code.** §4.6, with N7. Then decide §4.4 and report the case.
7. **Full check.** See §7.0. Then run the §7.4 mutations, one at a time, restoring after
   each.
8. **Docs in the PR.**
   - Copy `/home/fuad/projects/relevo/docs/specs/2026-09-25-builder-log-design.md` to the
     same path in your worktree, unchanged.
   - Save this plan verbatim at `docs/plans/2026-09-25-builder-log-r1.md`.
9. **Commit and PR.**
   - One commit: `fix(headless): a mid-round switch keeps the stream cursor and renders
     each process with its own harness; agy's result error is rendered; status reads the
     exit code from the stream`.
   - Push and open a PR against main, with the §7.4 results in the body.

### 7.2 New tests

- **N1** (transcript package): render test over a new fixture,
  `internal/transcript/testdata/agy-error-results.jsonl`. It has exactly these 7 lines
  (real agy results, ids and usage trimmed):
  ```
  {"event":"result","result":{"conversation_id":"c1","status":"ERROR","response":"","error":"API error (attempt 8): RESOURCE_EXHAUSTED (code 429): Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h39m27s."}}
  {"event":"result","result":{"conversation_id":"c2","status":"ERROR","response":"","error":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h43m8s."}}
  {"event":"result","result":{"conversation_id":"c3","status":"ERROR","response":"","error":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 20m48s."}}
  {"event":"result","result":{"conversation_id":"c4","status":"ERROR","response":"","error":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h38m7s."}}
  {"event":"result","result":{"conversation_id":"c5","status":"ERROR","response":"","error":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 43h27m30s."}}
  {"event":"result","result":{"conversation_id":"c6","status":"ERROR","response":"/state/ck-d2-round/001-report.md\n","error":"API error (attempt 1): request failed: Post \"https://example.invalid/v1:streamGenerateContent\": read tcp: connection reset by peer"}}
  {"event":"result","result":{"conversation_id":"c7","status":"ERROR","response":"/state/spaceapi-ingest/001-report.md\n","error":"API error (attempt 1): UNAVAILABLE (code 503): No capacity available for model gemini-3.8-flash-high on the server"}}
  ```
  - Assert that each line renders `result: ERROR`, then `error: <the error text>`, then
    the response when it is non-empty, in that order.
  - Assert that a result with no `error` renders exactly as before. Keep one existing
    SUCCESS fixture case unchanged.
- **N2** `TestAgyLimitDetectedInRenderedStream` (relevo/limit_test.go):
  - Render each fixture line with `transcript.Render("agy", line)`, and run
    `matchLimit(joined, <agy limit patterns>, now, 0)`. The patterns come from the same
    source `limitPatterns` uses (`harness` agy `LimitPatterns`), compiled the way
    limit.go compiles them.
  - Lines 1-5 must match. Lines 6-7 must not.
  - The fixture sits in the transcript package. Read it by relative path
    (`../transcript/testdata/…`), or duplicate the seven lines inline, and say which.
- **N3** `TestSegmentKind` (table):
  - empty segs gives the fallback;
  - off before the first Start gives the fallback;
  - exactly at a Start gives that segment's kind;
  - between two segments gives the earlier one;
  - after the last gives the last.
- **N4** `TestCarryStream`:
  - the four fields are copied;
  - `to`'s Kind, AgentName, Mode and PID are untouched.
- **N5** `TestStartProcessAppendsSegments`:
  - first spawn in a round gives one segment `{0, kind}`;
  - write N bytes to the stream, then spawn again in the same round with a different
    Kind: the result is two segments, the second being `{N, newKind}`;
  - a spawn with the same Start replaces rather than appends;
  - a spawn in a later round gives a fresh list with one segment.
  - Use the existing fake runner; startProcess tests exist near
    `TestStartRoundOnTheSameRoundKeepsTheCursor` (headless_test.go ~213).
- **N6** `TestDrainRendersEachSegmentWithItsKind`:
  - A stream whose first part is agy lines (segment `{0,"agy"}`) and whose second part is
    claude stream-json lines (segment `{N,"claude"}`), with Builder.Kind "claude".
  - Drain it. The log contains the agy lines rendered as agy (no `[init]` / `[step_update]`
    placeholder lines) and the claude lines rendered as claude, each exactly once.
  - Draining again appends nothing.
  - Build fixtures from lines already used in the transcript package's tests.
- **N7** `TestStatusExitCodeReadsTheStream`:
  - A headless binding whose process has exited. The fake runner records `exitPaths`
    (fake_test.go:701-705).
  - After `headlessStatus` (through `relevo.Status` or directly), the recorded path is
    `BuilderStreamPath(name, round)`, not the log path.
- **N8** `TestSwitchKeepsTheStreamCursor`:
  - A headless binding mid-round with `StreamOffset` > 0 and one segment.
    `switchBuilder` to a candidate of a different kind.
  - After it: `StreamRound` and `StreamOffset` are unchanged, and there are two segments
    (the second at the stream's size).
  - A drain then renders only bytes after the old offset. No line before the old offset
    appears twice in the log.
  - Use the existing switch test fixtures.

### 7.3 Fenced tests

You may change only these existing tests:

- the transcript render test's expectations for agy `result` lines **that have an `error`
  field**. Report every changed expectation line.
- `TestStartRoundOnTheSameRoundKeepsTheCursor` and other startProcess and drain tests,
  **only** to add `StreamSegments` to an expected Endpoint or binding. Their other
  assertions must stand.
- `internal/store/testdata/binding-shape.golden`, via `-update`.

Every other existing test must pass unchanged, and markers and log content included. If
one fails, halt.

### 7.4 Mutation checks (run each, report pass/fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | agy.go: drop the `error:` line | N1, N2 |
| M2 | switch.go: back to `b.Builder = ep` | N8 |
| M3 | drainStream: render with `b.Builder.Kind` instead of segmentKind | N6 |
| M4 | segmentKind: pick the first segment with Start ≤ off instead of the last | N3, N6 |
| M5 | headlessStatus: ExitCode back to `e.LogPath` | N7 |
| M6 | startProcess: always append (never replace same-Start) | N5 |
| M7 | drainStream: drop the `off >= StreamStart` session guard | a test you add to N8 or N6 that asserts `StreamSessionID` comes from the new process's lines. Add it. |

If a mutation does not make its named test fail, report it. Do not strengthen tests
beyond this plan, except M7's.

## 8. Scope check for the reviewer

- `git diff --stat` shows only the §2 files. Test files may be the package's existing
  ones.
- The golden changes only by the new `stream_segments` lines.
- `builder.log` is still written, markers unchanged, readers unchanged.
- New exported names: `store.StreamSegment` and `Endpoint.StreamSegments`.
