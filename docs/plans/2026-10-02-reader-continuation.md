# Plan: continue a reader round that ends without its deliverable, then halt

Base: origin/main. One builder round, new commits only.

## What the code does today

- **The close path** is `internal/relevo/headless.go`, the exited-runner branch, roughly lines 826-900.
  - After the marker re-check, it calls `writeReaderSummary` (`internal/relevo/summary.go:48`).
  - That function saves the stream's last assistant text as the reader's output file, even when no message carried a relevo block.
  - The `StatFile(reportPath)` check that follows then succeeds, so the round closes through the "exited after writing a report but no marker" branch (`note = "unmarked"`). That is why `wait` exits 2 and the stray sentence becomes the artifact.
- **The exit-without-report path** is the later part of the same function.
  - It handles denial, oom, stop and lost-to-restart cases.
  - At about line 1072 it calls `nudgeResume` (`internal/relevo/nudge.go:81`).
  - It then falls to `haltBinding` when not switchable, or `switchBuilder`.
- **`nudgeResume`** already resumes the same runner session through `resumeRound` (`headless.go:~404`).
  - It is limited to exit code 0, a non-empty `StreamSessionID`, and one nudge per plan (`nudgedSincePlan`).
  - It has a reader variant, `readerNudgePromptFormat`, but that prompt tells the reader to write a file and create the marker.
  - That is the wrong contract for a reader whose final message is the deliverable.
- **Readers never reach the nudge** when their last text is non-empty, because `writeReaderSummary` runs first.
  - It only reaches the without-report path when the stream has no text at all.
- **The reader contract** is in `readerPrompt` (`internal/relevo/send.go:~75`).
  - The final message is the actor's output label, saved as that label's file.
  - The final message ends with the relevo block.
  - The marker is created after it.
  - `transcript.LastWithBlock` (`internal/transcript/final.go:73`) already finds the last message carrying a ```` ```relevo ```` fence.

## Behaviour and cases

Applies only to a reader (`b.Shape == store.ShapeReader`) that has exited with no marker. The marker path (`holdReaderOnMarker` / `markerClose`) is untouched.

**Deliverable present** means either of the following:

- A regular file already exists at the reader's output path (`reportPathFor`). The runner wrote it itself.
- `transcript.LastWithBlock(lastStreamKind(b), stream) != ""`. This is the block the actor's prompt demands, and the chain readers' own block also matches.

A length heuristic is never used. A DSML text or a narration sentence is not a deliverable. A fenced but malformed block counts as present and keeps its existing "unstructured" handling.

**Cases**

1. **Present.** Behaviour is unchanged: `writeReaderSummary`, then the unmarked close.
2. **Absent, exit 0, session known, fewer than 2 continuations since the latest prompt.**
   - Do not call `writeReaderSummary`, so the stray text is never stored.
   - Resume the same session with the continuation prompt and hand the round back. No switch is counted and `RoundStartedAt` is kept.
   - On the next exit, evaluate again. If the final message now carries a block, the round closes with the continued output as the artifact.
3. **Absent after 2 continuations, or exit 0 with no session id.**
   - Close halted through `haltBinding`, with a reason that says the reader ended without its deliverable.
   - The reason names the output label and the continuation count, for example `<name>: <label> not delivered: runner ended without its relevo block after 2 continuations; see <show --transcript>`.
   - Nothing is saved as the artifact, and `wait` exits on the halted outcome.
4. **Absent, non-zero exit.** The existing path is unchanged (switch or halt as today). Limit, denial, oom, stop and restart branches all run before the continuation and keep winning.
5. **Writer.** Nothing changes. It keeps one nudge per plan and its existing prompt.

The continuation prompt text is: "you stopped before your deliverable; continue, and make your final message the complete deliverable". Add that the final message must end with the relevo block, then the marker. Do not tell it to write a file.

A resend (a newer prompt entry) resets the continuation count, as `nudgedSincePlan` does now.

## Seams

- `internal/relevo/nudge.go`:
  - Turn `nudgedSincePlan` into a count of nudge switch entries since the latest prompt, `nudgesSincePlan`. Keep the `nudgeNotePrefix` match.
  - Add a limit by shape: 1 for a writer, 2 for a reader.
  - Replace `readerNudgePromptFormat` with a continuation prompt that fits the reader contract.
  - `nudgePromptFor` selects it for a reader.
- New file `internal/relevo/reader_deliverable.go`, which keeps `headless.go` under the size and function-length limits:
  - `readerDeliverablePresent(rt, b) bool`, implementing the definition above.
  - A small helper that builds the halt reason.
- `internal/relevo/headless.go`:
  - In the exited branch, for a reader with no deliverable, skip `writeReaderSummary` and the unmarked close and fall to the without-report path.
  - In that path, after `nudgeResume` declines for a reader whose exit code is "0", call `haltBinding` with the new reason instead of `switchBuilder`.
  - Do not extend the function past 70 lines; extract a helper if needed.
- `internal/relevo/summary.go`: the doc comment on `writeReaderSummary` says a reader with an empty text closes without a report. Update it to say the caller no longer calls it when no deliverable exists.
- No `cmd/relevo` change, so no CLI test. Per CLAUDE.md, tests live in `internal/relevo` and use fakes only: no harness binary and no network.

## Steps

1. Read `nudge_test.go` (`TestExitZeroWithoutReportResumesSessionOnce`, `TestReaderNudgeNoteSaysWithoutAnOutput`, `TestSecondExitAfterNudgeSwitchesAsBefore`) and `summary_test.go` for the fake runner, stream and session helpers. Deliverable: the helpers to reuse. Check: no new fake harness is needed.
2. Add `reader_deliverable.go` with `readerDeliverablePresent` and the halt-reason helper. Deliverable: the pure function. Check: it compiles.
3. In `nudge.go`, replace the boolean once-per-plan check with a count and a per-shape limit, and add the reader continuation prompt. Deliverable: the writer's behaviour is byte-identical and the reader gets 2 continuations. Check: the existing writer nudge tests still pass.
4. Wire `headless.go` as in the seams above. Deliverable: the reader-with-no-block path reaches the nudge, then halts. Check: the focused tests below pass.
5. Update the `TestReaderNudge*` test and the `writeReaderSummary` comment to the new contract.
6. Add the tests below.
7. Save this plan to `docs/plans/2026-10-02-reader-continuation.md` as the last step. Per the repo rule it ships in the same PR as the code.

Run the focused tests with `go test ./internal/relevo -run 'Nudge|ReaderContinuation|ReaderDeliverable|ReaderSummary' -count=1`, and fix every reported error before the next run. Run `make check` once at the end. The coverage baseline must not drop more than a point. Regenerate it only if code moved between packages, and say so in the report.

## Tests

Pure-function style, using fake runner and transcript fixtures. Each test names the mutation that must fail it.

- **`TestReaderDeliverablePresent`**: table of cases.
  - Present: a block-carrying final message; a runner-written output file; a chain-block message.
  - Absent: a narration sentence; DSML text; empty.
  - Mutation: make it return true on any non-empty text, and the narration case fails.
- **`TestReaderWithoutBlockIsContinuedOnceAndClosesWithContinuedOutput`**:
  - A reader exits 0 with narration only.
  - Assert one resume with the continuation prompt, and a switch entry with `nudgeNotePrefix`.
  - Assert nothing is saved as the output file at that point.
  - A second exit carrying a block closes via the normal reader close, and the artifact is the continued message.
  - Mutation: skip the `writeReaderSummary` bypass, and the junk file gets saved and the assertion fails.
- **`TestReaderWithoutBlockHaltsAfterTwoContinuations`**:
  - Three exits with no block.
  - Assert exactly two resumes, then the binding halted with a reason naming the output label and "not delivered".
  - Assert no output file exists and the close is not "unmarked".
  - Mutation: raise the reader limit to 3, or revert to `switchBuilder`, and the test fails.
- **`TestReaderWithoutBlockAndNoSessionHaltsWithoutResume`**.
- **`TestReaderNonZeroExitKeepsSwitchPath`**: exit code 1 with no block still switches, as before.
- **`TestWriterNudgeLimitStaysOne`**: a writer's second exit after one nudge switches as today. Mutation: apply the reader limit to writers, and it fails.
- **`TestResendResetsReaderContinuationCount`**.
- **`TestReaderWithMarkerIsUnaffected`**: a marker-present reader closes through the marker path with no continuation.

## Report must include

- The files changed, and `git diff --stat` against this scope.
- The `make check` result and the focused test command output.
- Whether `testdata/coverage-baseline.txt` was touched.
- The mutation results, one per named test.
- Whether the OpenCode `--fork` resume gave each continuation a fresh session id. The second continuation reads the forked session's announced id from `StreamSessionID`, so say whether this held.

## Out of scope and risks (stated, not built)

- A reader that writes the marker and then ends on junk goes through the marker path and is still not checked. That case was not among the observed failures.
- A reader whose actor has no relevo-block requirement is treated the same way. `readerPrompt` demands the block for every non-chain reader, so this holds today.
- A resumed OpenCode process forks the session. Check that `abandonSession` and the reaper do not delete the session the continuation is using.


## MasterMind amendment (binding on this round)

Observed live on 2026-10-02 with a Sonnet lite-planner: the reader wrote the
complete plan in one message (ending with its relevo block), then sent a short
closing summary ("The plan is above, ..."). Today `writeReaderSummary` saves
the LAST assistant text, so the artifact became the summary, not the plan.

Case 1 ("Present") must therefore save the message that carries the relevo
block (`transcript.LastWithBlock`'s message, the full text of that message)
as the reader's output, not the stream's last text, whenever a block exists.
Add `TestReaderArtifactIsTheBlockCarryingMessageNotATrailingSummary`
(mutation: save the last text instead -> fails).
