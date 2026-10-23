# Runner liveness, plan 4 of 7 (#1310, #1330, #1311)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

**Behaviour.** An opencode builder that exits without a report and whose last
harness error is transport-class (plan 3) is resumed in the SAME session on the
same candidate, once per session, uncounted against `max_switches`, and the
round log says so. If the resumed session fails again the exit takes today's
path (until-cleared exclusion and counted switch, `headless.go:~1287`). Never a
fresh run on the changed tree. A limit, a permission denial, an escape halt, a
stop, an oom kill, a daemon-restart loss, an outage and a clean-turn nudge keep
their precedence.

**Seams.**
- `internal/relevo/resume_same.go` (new): 
  - `sameSessionNotePrefix = "resumed same session"` and
    `sameSessionResumes(entries, round, sess) int`: counts this round's
    `store.KindSwitch` entries with that prefix naming `sess` (the note carries
    `session <id>`), so a replacement candidate's new session gets its own one.
  - `resumeSameSession(ctx, rt, tx, b, entries, cause string, prompt string, now)
    (store.Binding, bool, error)`: refuses (returns `b, false, nil`) when
    `b.Builder.StreamSessionID == ""`, when `sameSessionResumes >= 1`, or when
    `resumeRound` fails (warn and fall through, as `nudgeResume` does). On
    success mirrors `nudgeResume`'s tail: keep `RoundStartedAt`, `StateActive`,
    `clearHaltFields`, `Progress` fresh with `ResumedAt` (plan 2), and append one
    `KindSwitch` entry with `Usage: recordUsage(...)` of the outgoing segment
    (same call as `nudge.go:122`) and a note
    `resumed same session <id> of <candidate> after <cause>: same candidate, not counted`.
  - Extract the shared post-resume block from `nudgeResume` (`nudge.go:121-148`)
    into one helper both use, so the two cannot drift; `nudgeResume`'s behaviour
    and tests stay as they are.
  - `transportPrompt(rt, b)`: a fixed message: the previous turn was cut off by
    a connection error, continue from the tree as it stands, do not redo finished
    work, finish in the foreground (re-use the background-work-died sentence from
    the nudge by sharing the constant), write the report to `%s`, then create
    `%s`. Reader shape: use the reader continuation wording
    (`readerContinuationPromptFormat`) with one extra sentence about the
    connection error.
- `internal/relevo/transport_resume.go` (new): `transportResume(ctx, rt, tx, b,
  entries, now) (store.Binding, bool, error)`: returns not-handled unless
  `b.Builder.Kind == "opencode"`; reads `currentBuilderTransportText(rt, b,
  availability.LimitScanLines)`; `MatchTransport` false means not handled; else
  `resumeSameSession(..., cause = "transport error: "+line)`. Log `slog.Info`
  `headless builder resumed after transport error`.
- `internal/relevo/headless.go`, exit path: one call, immediately before the
  `gateOnOutage` block (`headless.go:~1275`), after the `!switchable` halt, so
  a limit (`gateOnLimit` earlier at `:1224`), a denial, an escape halt and the
  nudge have already had their turn and an outage is still left to
  `gateOnOutage` (plan 3's `MatchTransport` already returns false for outage
  lines). Shape of the call: `if next, ok, err := transportResume(...); err != nil
  { return next, err } else if ok { return next, nil }`. No other line of
  `headless.go` changes.
- Tests: new `transport_resume_test.go` in `internal/relevo`, driving the exit
  path with the fake runner the existing nudge tests use
  (`grep -n nudgeResume internal/relevo/*_test.go` for the fixture), pure of the
  CLI; no test in `cmd/relevo` (CI has no harness and no network).

**Steps.**
1. `resume_same.go` with the shared post-resume helper; refactor `nudgeResume`
   onto it. Done when all existing nudge tests pass unchanged.
2. `transport_resume.go` and the one call site in `headless.go`. Done when the
   new tests pass.
3. Tests, one name per behaviour: `TestTransportErrorResumesSameSessionOnce`
   (code 1, last event ECONNRESET, no report: session id kept, no switch,
   `RoundSwitches` unchanged, a log note with the session id),
   `TestSecondTransportErrorSwitchesCandidate` (the resumed process fails again
   with the same session id: counted switch and exclusion as today),
   `TestTransportResumeKeepsLimitPrecedence` (a `provider.rate-limit` tail gates,
   does not resume), `TestTransportResumeKeepsOutagePrecedence` (503 tail goes to
   `gateOnOutage`), `TestTransportResumeNeedsSessionId` (empty `StreamSessionID`
   falls through to the switch), `TestTransportResumeSkipsOtherHarnesses`,
   `TestNewCandidateAfterSwitchGetsItsOwnResume` (a different session id after a
   switch is allowed one). Mutation check: remove the `>= 1` cap; the second
   test must fail.
4. Run `make check-affected`.
5. Commit.

**Report must include:** the exact log note; whether `StreamSessionID` is set
again after a resume before a second failure can occur (if a resumed opencode
process does not re-announce its session, say so: the second failure then
switches, which is the safe direction); the line count of `headless.go` before
and after.


