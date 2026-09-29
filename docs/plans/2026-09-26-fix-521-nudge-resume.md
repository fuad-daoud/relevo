# Fix #521 -- a builder that ends its turn without a report is resumed once with a nudge

## 0. Rules for this round

- Before anything else: `git fetch origin && git merge --ff-only origin/main`.
  If the fast-forward fails, stop and report. (`main` now contains the stale
  builder token change, which added a stale-candidate branch to
  `reconcileHeadless` near the "lost to a daemon restart" block. Keep it intact.)
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it -- which is
  exactly the bug this round fixes. Your turn ends only after the report is
  written and the done marker exists.
- Comments: *why* only, no issue numbers, no `§`, no history. No new
  `.golangci.yml` exclusion or allow-list entry. New functions <= 70 lines;
  do not grow `reconcileHeadless` by more than ~10 lines -- the logic goes in
  a helper.
- `cmd/relevo` tests must not spawn a harness or reach the network. This round
  adds no `cmd/relevo` test.

## 1. System overview

A headless builder runs as `claude -p` / `agy -p` / `opencode run` and exits
when its turn ends. A builder that backgrounds its check and ends its turn to
"wait for the notification" exits 0 with no report and no done marker, and
nothing will ever wake it. Today `reconcileHeadless`
(`internal/relevo/headless.go`, the "Exited without a report." block,
~766-927) logs a `KindExit` entry and then switches candidate or halts
(NEEDS YOU). The work is usually almost finished.

New behaviour: when the builder exited with code `0`, wrote neither report
nor marker, announced a session on its stream (`b.Builder.StreamSessionID`),
and has not been nudged since the round's latest plan was sent, relevo resumes
**the same session once** with a fixed nudge prompt, using the existing
`resumeRound` (`headless.go` ~327-360, which calls `Harness.ResumeBuild` in
`internal/harness/resume.go`: claude `--resume`, agy `--conversation`,
opencode `--session --fork`; codex returns `ErrResumeUnsupported`). The
resume is recorded as a `KindSwitch` log entry, like the restart resume, and
is not counted against `max_switches`. If the resume cannot start, or the
resumed process again ends without a report, today's behaviour applies.

The server runs the same `reconcileHeadless`, so served rounds get this too.
The resume takes the slot the exited process just freed, so it cannot push
the server past its builder cap.

Out of scope: blocking tools in the builder's argv (`--disallowedTools`);
changing the agent definitions; the e2e fake harness (`internal/e2e`).

## 2. File structure

```
internal/relevo/nudge.go         NEW: nudgeResume helper, nudge prompt and note prefix constants
internal/relevo/headless.go      one call site in reconcileHeadless's exit-without-report block
internal/relevo/nudge_test.go    NEW: tests (reuse headless_test.go helpers; do not copy them)
docs/plans/2026-09-26-fix-521-nudge-resume.md   this plan (last step)
```

No other file changes. No store schema change, no new log kind.

## 3. Data and contracts

The note needs a prefix of its own, so the once-check cannot match the
restart resume, whose note is `"resumed session <id> builder (lost to a
daemon restart at ...)..."`:

```
const nudgeNotePrefix = "nudged builder"
note text: "nudged builder (ended its turn without a report): resumed session <id> of <token>: same candidate, not counted"

const nudgePrompt  // the message sent into the resumed session; include the two paths:
  "You ended your turn before writing the report, and nothing will wake you:
   this process exits when your turn ends. Finish now, in the foreground:
   run any pending check to completion and wait for it, write the report to
   <reportPath>, then create <donePath>. Do not start background tasks and do
   not end your turn before both files exist."

nudgeResume(ctx, rt Runtime, tx *store.Tx, b store.Binding,
            entries []store.LogEntry, codeText string, now time.Time)
    (next store.Binding, resumed bool, err error)
```

Preconditions checked inside `nudgeResume`, all must hold, else return
`(b, false, nil)` untouched:
- `codeText == "0"`
- `b.Builder.StreamSessionID != ""`
- no `KindSwitch` entry for `b.Round` whose `Note` starts with
  `nudgeNotePrefix` appears *after* the latest `KindPlan` entry
  (`DirToBuilder`) for `b.Round` in `entries`. (Once per sent plan: a resend
  of the same round may be nudged again.) `entries` must include the
  `KindExit` entry just appended; if the caller's slice predates it, that is
  fine, since the check only reads plan and switch entries.

Postconditions when `resumed == true`:
- `next` is `resumeRound`'s result: a new process is running in the same
  round, same candidate, `StreamSessionID` cleared by `startProcess`.
- `next.RoundStartedAt` equals `b.RoundStartedAt` (the nudge buys no time).
- `next.RoundSwitches` and `next.RoundExcluded` equal `b`'s.
- `next.State == store.StateActive`.
- One `KindSwitch` log entry was appended: `Round: b.Round`,
  `Direction: DirToPlanner`, `Confirmed: true`, `Usage: peekUsage(...)` as the
  restart path does, `Note` as above.

When `resumeRound` fails for any reason (`ErrResumeUnsupported`, a
`spawnFailure`, anything else): log at Warn with the error, and return
`(b, false, nil)` with the *original* `b` so today's path continues. Do not
fall back to a fresh relaunch -- that is what the switch path already does.
If `resumeRound` can leave a partially started process or partial `tx`
writes on failure, stop and report.

## 4. Pseudocode

```
reconcileHeadless, "Exited without a report." block, placement:
  ... AppendLog(exitEntry)                       (unchanged)
  ... stop-requested close                       (unchanged)
  ... lost-to-restart branch + stale branch      (unchanged)
  ... escape halt                                (unchanged)
  ... gateOnLimit (a usage limit wins over a nudge)   (unchanged)
  ... isDenial halt                              (unchanged)
  NEW:
  if next, resumed, err := nudgeResume(ctx, rt, tx, b, entries, codeText, now); err != nil:
      return next, err
  else if resumed:
      slog.Info("headless builder nudged to finish", binding, round, session)
      return next, nil
  if !switchable: halt                           (unchanged)
  switch                                         (unchanged)

nudgeResume:
  if preconditions fail: return b, false, nil
  sess  := b.Builder.StreamSessionID
  keep  := b.RoundStartedAt
  prior := peekUsage(ctx, rt, b, now)
  prompt := nudge text with ReportPath/DonePath for (b.Name, b.Round)
  next, err := resumeRound(ctx, rt, tx, b, sess, prompt)
  if err: slog.Warn(...); return b, false, nil
  next.RoundStartedAt = keep
  next.State = StateActive
  AppendLog(KindSwitch entry)       -- error here is returned (as the restart path does)
  return next, true, nil
```

Confirm by reading that `b.Builder.PID/StartedAt` are already zeroed before
this point and that `resumeRound` → `startProcess` records a new stream
segment (so the jsonl keeps both processes' output); if either is not so,
stop and report.

## 5. Tests (`internal/relevo/nudge_test.go`)

Model every test on `TestReconcileHeadlessExitWithoutReportLogsAndSwitches`
(`headless_test.go` ~1376) and the restart-resume tests near it; reuse their
setup helpers and fake runner. Use a claude candidate unless stated.

1. `TestExitZeroWithoutReportResumesSessionOnce`: exit code 0, session `S1`
   announced, no report/marker. Want: one new process whose argv contains
   `--resume S1` and whose prompt contains the report path; one `KindSwitch`
   entry with the `nudged builder` prefix; `RoundSwitches` unchanged;
   `RoundExcluded` unchanged; `RoundStartedAt` unchanged; `State` Active;
   no halt.
2. `TestSecondExitAfterNudgeSwitchesAsBefore`: continue from test 1's state,
   the resumed process also exits 0 without a report (with a session
   announced). Want: today's behaviour -- a counted switch to the next
   candidate (or the halt, matching what the setup yields), no second nudge.
3. `TestNonZeroExitIsNotNudged`: exit code 1 (and `unknown` not lost to a
   restart). Want: today's behaviour, no nudge entry.
4. `TestExitWithoutSessionIsNotNudged`: no session announced. Want: today's
   behaviour.
5. `TestCodexExitIsNotNudged`: codex candidate (`ErrResumeUnsupported`).
   Want: today's behaviour, no nudge entry, no extra process.
6. `TestResendAllowsAnotherNudge`: after a nudge, a new `KindPlan` entry for
   the same round (a resend), then an exit 0 without a report. Want: nudged
   again.
7. `TestLimitExitIsGatedNotNudged`: exit 0 whose log tail matches the
   candidate's limit pattern. Want: gate + switch as today, no nudge.

Mutation checks: (a) remove the `codeText == "0"` condition -- test 3 must
fail; (b) make the once-check always false -- test 2 must fail; (c) drop
`next.RoundStartedAt = keep` -- test 1 must fail. Restore after each.

## 6. Error handling

- A failed resume is recoverable: Warn and fall through to today's path.
- A failed `AppendLog` after a successful resume is returned, as the restart
  path returns it; the tick retries.
- Never nudge on a usage limit, a permission denial, an escape, or a stop
  request: those branches come first and return.

## 7. Working efficiently

- Read in one batch: `headless.go` 240-360 and 700-930, `internal/harness/resume.go`,
  `internal/store/log.go` 40-60, `switch.go` 80-180, `headless_test.go`
  1370-1440 and the restart-resume tests (grep `resumed session`).
- One edit call per file.
- Focused loop: `go build ./... && go test ./internal/relevo -run 'Nudge|ExitWithout|Exit|Resume' -count=1`.
- Full check once, at the end, in the foreground: `make check`.

## 8. Ordered steps

1. Fast-forward to origin/main (§0).
2. `nudge.go` with constants and `nudgeResume` (§3). Build passes.
3. The call site in `reconcileHeadless` (§4). Existing `internal/relevo`
   tests pass unchanged -- if an existing test now fails because it expected
   an exit-0 switch and the fixture announces a session, do not change its
   assertion: stop and report which test and why.
4. Tests (§5). Focused tests pass.
5. Mutation checks (§5). Report each failing test name.
6. `make check` in the foreground. `git diff --stat` shows only §2 files.
   Commit: `feat(headless): resume a builder that ended its turn without a report, once, with a nudge (#521)`.
7. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-fix-521-nudge-resume.md` and commit it.

Report: the diff stat, the mutation checks' failing test names, any existing
test that touched the new path, and the `make check` result.
