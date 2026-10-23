# Runner liveness, plan 7 of 7 (#1310, #1330, #1311)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

**Behaviour.** When plan 6's verdict says a live opencode builder is silent: the
first stall in a session aborts the process and resumes the same session once
(plan 4's helper, cause `provider stall`); a second incident in that session
(another stall, or an earlier transport resume; the budget is shared) switches
candidate like a gate: uncounted `switchBuilder`, outgoing process killed, the
candidate excluded for the rest of the round. Never a fresh run on the changed
tree.

**Seams.**
- `internal/relevo/silence.go`: `actOnSilence(ctx, rt, tx, b, entries, now)
  (store.Binding, bool, error)`: not-handled unless `silenceDue` and the verdict
  is silent. Count `sameSessionResumes(entries, b.Round, b.Builder.StreamSessionID)`
  (plan 4):
  - budget unused: append the `provider stall:` note (replacing plan 6's
    log-only call at the same site; keep one writer), then `stopProcess(ctx, rt,
    b, "provider stall")` (`headless.go:1524`). Leave the resume to the next
    tick's exit path (see below): the process is dead, the exit trailer is
    written, and the existing exit handling (`endRoundScope`, exit entry) runs
    before anything resumes. Return handled so this tick does nothing else.
  - budget used, or no session id: `b.RoundExcluded = appendUnique(b.RoundExcluded,
    b.BuilderCandidate)` then `switchBuilder(ctx, rt, tx, b, "provider stall: no
    stream event for N min", true, false)`.
- Exit path: in `transport_resume.go` (plan 4) extend the classifier so a
  `provider stall:` entry newer than `b.Builder.StartedAt` (the process that was
  aborted) counts as a cause, with `cause = "provider stall"`, using a stall
  prompt (same text family as `transportPrompt`, saying the model went silent and
  the step was aborted). A real transport error in the tail still wins as the
  cause text. Rename `transportResume` to a name that covers both
  (`sameSessionRecovery`) in the same commit.
- Call site: `headless.go` alive branch, replacing plan 6's `noteProviderStall`
  call with `actOnSilence` (same lines, still a few). The existing `gateOnLimit`
  scan for a stalled binding (`headless.go:~957`) stays after it and unchanged.
- Hook the stop so it is not read as a user stop: `stopProcess` only kills; it
  does not set `StopRequestedAt`, so the exit path's stop branch
  (`headless.go:~1077`) will not close the round. Confirm with a test, do not
  assume.
- Tests in `internal/relevo/silence_act_test.go`, with the fake runner:
  `TestFirstSilenceAbortsAndResumesSameSession` (tick 1: process killed and a
  stall note; tick 2: exit path resumes the same session, no switch,
  `RoundSwitches` unchanged), `TestSecondSilenceSwitchesCandidateUncounted`
  (budget used: switched, `RoundSwitches` unchanged, candidate in
  `RoundExcluded`), `TestSilenceAfterTransportResumeSwitches` (shared budget),
  `TestSilenceDoesNotAbortWhileCPUBusy`, `TestSilenceStopIsNotAUserStop`,
  `TestSilenceWithoutSessionIdSwitches`. Mutation check: allow a second resume;
  the second test must fail. No test in `cmd/relevo`.

**Steps.**
1. `actOnSilence` and the call-site swap. Done when the first two tests pass.
2. Classifier extension, stall prompt, rename. Done when the exit-path test and
   all plan 4 tests still pass.
3. Remaining tests. Done when `go test ./internal/relevo -run 'Silen|Transport|Nudge'`
   passes.
4. Run `make check-affected`.
5. Commit.

**Report must include:** the exact log notes for abort, resume and switch, one
status line showing `model silent for N min` before the abort, and any case
where the kill raced the trailer read (code `unknown` versus `137`).
