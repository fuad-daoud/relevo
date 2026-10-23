# Correction plan: plan 3 of 3 (plan-writing planner nudge)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. New commit only, never an amend or rebase of the plan-3 commit.

## What the review asks

One change: the exit-without-marker hook (`internal/relevo/headless.go:1004`, `readerDeliverablePresent(rt, b) && !readerPlanNudgeDue(rt, b, entries, codeText)`) is pinned by no test. `exitedChainReader` in `internal/relevo/nudge_plan_test.go` always writes the done marker, so every test takes the marker path through `markerClose`. Reverting line 1004 to `readerDeliverablePresent(rt, b)` alone leaves `go test -run 'PlanWriting|ReviewerWithoutSteps|Nudge'` green.

The reviewer confirmed the behaviour is right (specs 2→3, state active, one switch note, chain still running); only the test is missing. The minor notes ask for no action: no code, wording or seam change.

## Steps

1. `internal/relevo/nudge_plan_test.go`: add `TestPlanWritingReaderExitedWithoutMarkerIsNudgedOnce`. It builds the reader with `planWriterReader` (plan text with no steps) and deletes the marker (`rt.Store.DonePath(b.Name, b.Round)`) before `reconcile`, either in the test body or via a small option on the helper, so that the process-exited, no-marker branch runs. It asserts:
   - one resume spec is added after the start, and its argv contains `planNudgeNote`;
   - the binding is `StateActive` on the resumed handle's PID;
   - exactly one switch note;
   - the chain row is still running (`flowChainRow`, not halted).
   Then it makes the resumed round exit again without a marker and reconciles once more. Assert no further spec is added (the second nudge is spent). Do not assert the chain's final status after that second reconcile: on this route a spent nudge halts with the reader-undelivered reason, which the review accepts. Keep the helper change minimal; existing tests keep their marker-present behaviour.
   Deliverable: the new test. Done when it passes.
2. Mutation check (run, then restore; nothing is committed from it): in `headless.go:1004` drop `&& !readerPlanNudgeDue(...)`. The new test must fail, and the existing five must still pass. Name the failing test in the report. Confirm `git diff` shows no change to `headless.go` after restoring.
3. Run `go test ./internal/relevo/ -run 'PlanWriting|ReviewerWithoutSteps|Nudge' -count=1` until green, then `make check-affected` once. Commit with a subject such as "Pin the exit-without-marker plan nudge with a test". No issue numbers or history in comments or test names. The test uses the fake runner and fake session only (no harness, no network).

## Scope

Only `internal/relevo/nudge_plan_test.go` changes. `git diff --stat` must show that file alone. Do not touch `nudge.go`, `reader_deliverable.go` or `headless.go`, and do not reword the exit-path halt or the switch note.

## Report must include

The commit subject, `git diff --stat`, the focused test result, the `make check-affected` result, and the mutation result: the test that failed with line 1004 reverted.
