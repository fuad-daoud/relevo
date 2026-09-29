# A5 fix (round 2, with the grace decided): a reader round's summary is its runner's real final message

**The bug**, found by a real smoke round on 2026-09-26 (binding `a5-smoke`, lite-planner on
deepseek via opencode):
- `001-lite-planner/summary.md` held the runner's **first** message, "I'll start by
  reading the plan file…";
- its actual final message was the two-sentence plan plus the `relevo` block. It is in
  the stream; `relevo show a5-smoke --transcript` shows it.

**The cause:**
- The reader prompt says to create the done marker "as the very last thing". The runner
  creates it with a **tool call**, and its final message necessarily comes **after**
  that call.
- The reconcile loop closes the round as soon as the marker appears (`markerClose`,
  internal/relevo/headless.go, and `closeOnMarker`, reconcile.go), and writes summary.md
  from the stream **at that moment**, before the final message has been written.
- `transcript.FinalText` is correct.
- The e2e fake (`internal/e2e`) prints its result line **before** touching the marker,
  so it never showed this.

**The fix:** a reader round's summary is taken once the runner's stream is complete, so
a reader round closes when its **runner process has exited**, not on the marker. Writers
are unchanged: their report is a file written before the marker.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. Close a reader round on exit

In the headless reconcile path (`reconcileHeadless`, headless.go, and its marker branch
`markerClose`), for a **reader** binding (`b.Shape == store.ShapeReader`, or whatever R3
named it):
- **Marker present, process still running:** do **not** close yet. Leave the round
  open. The process is expected to exit shortly after its final message. The next ticks
  re-check. Reuse whatever "still alive" check the unmarked-exit path uses.
- **Marker present, process exited:** close as today via the marker path, writing
  summary.md from the now-complete stream.
  - Mid-round switches: the stream is the round's `BuilderStreamPath`, and the kind is
    the last segment's (R4b's rule).
- **Process exited without a marker:** the existing unmarked close. Summary.md is still
  written from the final message when one exists, as R4b does.
- **Grace limit (the planner's decision; there is no existing one):**
  - Add `const readerFinalMessageGrace = 2 * time.Minute` in internal/relevo, with a
    one-line comment saying why: a runner writes its final message just after the
    marker and then exits, so two minutes is generous. A hung one must not hold the
    round forever.
  - Measure the grace from the **marker file's mtime** (`os.Stat(DonePath).ModTime()`
    against `rt.Now()`), so no new binding field and no format bump are needed.
  - If the marker is present, the process is still alive and the grace has passed:
    - close with the stream as it is;
    - add the note "runner still running after its marker; summary taken early";
    - stop the process with the **same function `relevo stop` uses** to end a
      headless runner (find it from `Stop` in internal/relevo/stop.go).
  - Writers are unaffected. Their marker close keeps today's behaviour.

## 2. The reader prompt

In `readerPrompt` (send.go), the marker line becomes:
"Then create this empty file: <marker>. Your final message comes after it: relevo saves
it once you finish."

Keep every other line of the prompt as is.

## 3. The e2e fake matches reality

In the fake harness script (`internal/e2e`, the reader branch R4b added):
1. touch the marker **first**;
2. sleep briefly (200ms);
3. **then** print the result line whose text is the final message;
4. then exit.

With the old close-on-marker behaviour, `TestHeadlessE2EReaderRound` must now **fail**:
summary.md would lack the final text. Assert that summary.md equals the final message,
including its `relevo` block.

## 4. Tests (internal/relevo)

1. `TestReaderRoundWaitsForExitAfterMarker`: with a fake Runner, the marker is present
   and the process is alive, so after a reconcile tick the round is still open. After the
   process exits and the stream gains a final text event, the next tick closes the round,
   and summary.md holds that final text.
2. `TestReaderRoundGraceClosesALingeringRunner`: the marker's mtime is more than 2
   minutes before `rt.Now()` and the process is alive. The round closes with the note,
   and the fake records the stop.
3. `TestWriterRoundStillClosesOnMarker`: a writer with the marker and a live process
   closes on the marker as today.
- **Required mutation.** Make readers close on the marker again (skip the alive check).
  Test 1 and the e2e must fail. Then restore.

## 5. Working efficiently

- Batch-read these:
  - internal/relevo/{headless,reconcile,send,stop}.go;
  - internal/transcript/final.go;
  - internal/e2e/* (reader test and fake script);
  - the R4b reader tests (reader_round_test.go and nearby).
- Focused loop: `go build ./... && go test -count=1 ./internal/relevo/ -run 'Reader|Marker|Close|Headless|Reconcile' && go test ./internal/e2e/ -run TestHeadlessE2E -count=1`
- Before committing, run `grep -n '§\|#[0-9]' <every file you touched>`: it must print
  nothing in comments.
- This runs on a laptop. Do **not** run `make check`, `go test ./...` or `-race`. At the
  end, run:
  - `gofmt -l $(git ls-files '*.go')`, which must print nothing;
  - `go vet ./internal/relevo/ ./internal/e2e/`;
  - `sh scripts/check-comments.sh`;
  - `sh scripts/check-filesize.sh`;
  - `golangci-lint run ./internal/relevo/... ./internal/e2e/...`;
  - `go test -count=1 ./internal/relevo/ ./internal/e2e/ ./internal/store/`.

## 6. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r4c-reader-close-on-exit.md`.
Commit as **one new commit**: `fix(a5): a reader round closes when its runner exits, so summary.md is the final message`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- where the alive check and the grace come from;
- the mutation, with the unit test and e2e results;
- the check outputs;
- anything that did not match.
