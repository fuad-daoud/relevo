# Plan: fix #1262 -- a chain resume closes the awaited member's round when no process is left for it, whatever the member's state

Tree read: the plan-1262 worktree (main 016cb0a4). Nothing was built or run; every claim below is from reading the code. The attempt this plan resumes had left no edits (clean `git status`).

## Decision 1 -- where the fix goes: `closeDeadMemberRound`, NOT the halt

Do not close the round in the halt path (`switchBuilder`'s resolve-failure arm, `internal/relevo/switch.go` ~248-256, `haltAndSettle`). Why:

- The halt is written by `reconcileHeadless` (`headless.go` ~889-897: `gatedBuilder` -> `switchBuilder(..., closeOld=true)`) on a binding that still has a pid. `resolveRole` fails *before* the `closeOld` kill (`switch.go` ~262), so the halt leaves the process and the open round in place by design.
- That is exactly what the non-chain recovery relies on. The check at `headless.go` ~889 runs every tick and does not look at `State`, so a binding halted NEEDS YOU on an all-gated actor is re-tried each tick; once `relevo gate --clear` lifts the gate, `resolveRole` succeeds and `switchBuilder` restarts the SAME round (`startRound` on the same prompt, `switch.go` ~363). That flow needs the round open and `RoundOpen(...)` true -- reconcileHeadless's `!roundOpen` branch (`headless.go` ~816-838) would otherwise kill the process as a stray and go idle.
- The no-pid arms (`queue.go` Admit, `abandonSession` + `haltAndSettle`) already document "round open, no process: nothing to observe until the human acts" (`headless.go` ~880-886); a human stop/send is that flow, so closing there changes a documented contract for every caller of `haltAndSettle`.

So the close belongs to the one actor that is a human command and already owns "close a dead round": the chain resume.

## Decision 2 -- why the member was not `needs_you`

Reading the code, the member WAS `needs_you` when the chain halted: the chain's halt text comes from `chainSweepFlowHalt(..., mb.Halt)` (`chain_sweep.go` ~101-102), taken only when `mb.State == StateNeedsYou`, and the "builder rate-limited: ...; cannot switch: ..." text is the member's own `Halt` (`haltBindingKind`, `reconcile.go` 286-311, sets `StateNeedsYou`). Nothing on the member re-activates it between the halt and the resume (the `StateActive` writers are rebind, send, nudge, round close and a successful switch; none run for a halted chain). So the issue's "member is left active" is not what the code produces; the member was NEEDS YOU, and what makes `memberRoundDead` (`stop.go` ~333-347) false is the PID/liveness half: the halt kept `Builder.PID` (the all-gated arm skips `abandonSession` when PID != 0 and never reaches the kill), and `Runner.Alive` still answered true at the time of the first resume, or the member's state was something other than NEEDS YOU for a reason this read cannot see. Step 1 settles which with a failing test; the fix below covers both (any state, and a pid whose process is gone), so the plan does not depend on the answer, but the report must state it.

## Behaviour

`closeDeadMemberRound` (`internal/relevo/stop.go` ~290-312) closes the awaited member's open round through `Stop` when the round has no process left, for any member state, in two cases:

- never launched / process cleared: `Builder.PID == 0`;
- launched but gone: `Builder.PID != 0` and `Runner.Alive` answers false.

It still leaves the round to the resume's own `RoundOpenError` when:

- a process is alive (`Runner.Alive` true), or `Runner == nil` with a pid, or the liveness probe errors (an OS hiccup is not evidence of death; `reconcileHeadless` already treats it as alive -- this tightens today's `err != nil` -> dead);
- the round is queued on a server (`QueuedAt` non-zero: nothing to kill, the daemon's admit owns it);
- the binding is remote (`Builder.Remote()`): keep today's rule for it exactly (NEEDS YOU and no pid), since `Stop` on a remote goes to the server;
- the member is `done` or `paused` (`Stop` refuses both, so the predicate must skip them rather than let `Stop` fail the resume);
- the log shows no open round (`roundOpenIn`, unchanged).

The resume then proceeds as in the existing dead-round case: round closed "without an output", the step re-sent on the same actor, or on a swapped one (`--reviewer-actor` / `--planner-actor` / reader-actor swap goes through `chainResumeLocked`'s refusal at `chain_resume.go` ~232, which sees a closed round).

## Seams

- `internal/relevo/stop.go`: `memberRoundDead` (~333-347) -- rewrite the predicate per Behaviour; rename if the state check is gone (e.g. `memberRoundProcessless`); update the doc comments on it and on `closeDeadMemberRound` (~290-297), which currently say "NEEDS YOU". Keep `stop.go` under 600 lines (341 now) and both functions under 70.
- `internal/relevo/chain_resume_wf.go` ~45-49: the call site and its comment ("its process gone and no report") stay; reword only if the predicate's name changes.
- `internal/relevo/chain_resume.go` ~232 (`resumeOpenRoundRefusal`): unchanged; the swap path keeps relying on it.
- Tests: new file `internal/relevo/chain_resume_gated_test.go` (keeps `chain_resume_test.go`, 1209 lines, from growing). Reuse `chainRuntime`, `startedChain` (`chain_start_test.go` 53/65), `chainBuilderClose` (`chain_test.go` 129), `chainBinding`, `chainLog`, `chainStoredRow`, `tickChains`, `haltBinding`/`clearProcess` as in `TestChainResumeClosesADeadMemberRound` (`chain_resume_test.go` ~836); the swap fixture is `testChainResumeSwapsReaderActor` (`chain_resume_swap_test.go` ~30, actor `"assistant"`). `fakeRunner` (`fake_test.go` ~966): a started pid is alive by default; `aliveErr` and the scripted `alive` map make it dead or erroring. CI has no harness and no network: these tests use the fake runner and the store only, and spawn no subcommand.
- One existing fixture will change: `TestChainResumeSwapRefusesAnOpenMemberRound` (`chain_resume_swap_test.go` ~166) fakes an "open round" by bumping `Round` and appending a prompt on a member with NO process. That is a round with nothing running, which the new rule correctly closes. Give the fixture a genuinely running round instead (start a real round on `shop-rev` through the fake runner so its pid is alive) -- the test's intent is "a running round is refused". If more than this one existing test fails on the new rule, halt and report rather than bend them.

## Steps

1. Reproduce first. Add the gated-halt test in the new file: start a chain, close the builder green so `shop-rev` has an open round with a live fake process, record a rate-limit gate on the reviewer's provider with no other candidate for its actor (as the issue: an actor whose only candidate is gated), run the member's reconcile/`tickChains` so `switchBuilder` halts, then `ChainResume` with no flags. Record, in the test's comments only where non-obvious and in the report, the member's `State`, `Builder.PID` and `Runner.Alive` at the halt. Done when: the test fails on the current tree with `*RoundOpenError`, and the report can state which of PID-kept-and-alive / state-not-NEEDS-YOU holds.
2. Rewrite `memberRoundDead` / its doc comments in `stop.go` per Behaviour. Done when: step 1's test passes and the existing `TestChainResumeClosesADeadMemberRound` still passes untouched.
3. Add the remaining store-backed tests (same file), each named for what it pins: (a) halted on an all-gated actor resumes with no flags and no manual stop, the reviewer round advancing past the halted one and the chain running again; (b) the same with `ResumeOptions{ReviewerActor: "assistant"}` and the member's role swapped; (c) a member with a live process (fake runner alive, state active and, in a second case, NEEDS YOU) is still refused with `*RoundOpenError`, member and round untouched, chain row unchanged (reuse `assertResumeOpenRoundRefused`); (d) a pid whose process is dead and an `aliveErr` case -- dead closes, probe error refuses; (e) `done`/`paused` member and a queued (`QueuedAt` set) member are left to the refusal and do not fail the resume with `Stop`'s own error. Done when: `go test ./internal/relevo -run 'ChainResume' -count=1` is green.
4. Fix the one swap fixture named above. Done when: `go test ./internal/relevo -run 'ChainResumeSwap' -count=1` is green and the fixture asserts a running round.
5. Mutation-test and record the results in the report (restore after each): (i) make the predicate ignore liveness (always dead) -> test 3(c) and 3(d) must fail by name; (ii) put back the `State == NeedsYou` requirement -> test 3(a)/(b) must fail if step 1 showed the member was not NEEDS YOU, otherwise (e) is the case that pins state-independence -- state which; (iii) drop the queued/done/paused skips -> 3(e) fails. A mutation no test notices is a missing test: add one.
6. `gofmt -l .`, then `make check`, once, at the end (focused iteration with the `-run` commands above; whole package once: `go test ./internal/relevo -count=1`). `make check` enforces golangci-lint, `scripts/check-comments.sh` (no history, no issue numbers in comments or test names), `scripts/check-filesize.sh`. `stop.go` must still be <= 600 lines; add no exclusion. No code moves between packages, so `testdata/coverage-baseline.txt` is not regenerated; say so in the report.
7. Last step: save this plan verbatim as `docs/plans/2026-10-17-chain-gated-halt-closes-round.md` and commit it with the code in the same commit set (new commits only; nothing is amended or rebased).

## Declared scope (files touched)

1. `internal/relevo/stop.go` -- `memberRoundDead` and the two doc comments
2. `internal/relevo/chain_resume_wf.go` -- comment/rename at the call site only, if the name changes
3. `internal/relevo/chain_resume_gated_test.go` -- new
4. `internal/relevo/chain_resume_swap_test.go` -- the one fixture
5. `docs/plans/2026-10-17-chain-gated-halt-closes-round.md` -- this plan

Nothing is deleted. Anything else in `git diff --stat` is out of scope.

## What the report must include

- The step 1 finding: the member's state, pid and liveness at the halt, and so which of the two explanations in Decision 2 was true.
- The mutation results from step 5, test name per mutation.
- `make check` output summary (and that `make e2e` was not run / was), and that the coverage baseline was untouched.
- Any existing test other than the named swap fixture that the new rule broke (halt, do not bend).
- A note that closing at the halt was rejected and why (Decision 1), so a reviewer does not re-open it.

## Round 3 amendment

Round 2's rule was "pid evidence, state-independent": `Builder.PID == 0` **and**
`State == needs_you`, **or** `Builder.PID != 0` **and** `Runner.Alive` false with
no error. The issue was then reproduced live against that rule (env sandbox,
main `016cb0a4`, with debug prints in `closeDeadMemberRound`) and it still
refused:

```
DBG awaited="gh3-rev" ok=true
DBG state=needs_you pid=4132257 dead=false
relevo: conflict: gh3-rev: round 1 is still open; relevo stop gh3-rev ends it
```

The awaited member was **NEEDS YOU with its pid kept and its process alive** --
the one shape round 2 ruled out. Round 2 read a resume as asking to proceed
*past* the open round. The reading this amendment adopts is that a resume *is*
the `relevo stop gh3-rev` the refusal names: a halted member's round is the
human's to end, and a resume is that human asking, so the resume ends it. The
runner exited minutes later, which is why a later manual stop answered "runner
already gone" -- the case round 2's rule could not reach.

**The final rule, as shipped.** `resumeMayCloseRound`
(`internal/relevo/stop.go`, renamed from `memberRoundProcessless`) admits the
awaited member's open round when

- the member is `needs_you` -- whatever its pid and whether its process is
  running; **or**
- `Builder.PID != 0` **and** `Runner.Alive` answers false with no error, in any
  state.

The first arm supersedes round 2's pidless half: NEEDS YOU is now read as a
state rather than as a pid, and `Stop` kills a live process on the way, so the
close is the same write a stop makes. The second arm is round 2's dead-pid rule,
unchanged.

**Unchanged from round 2**, each still pinned by a test: a pid of 0 in a state
that is not `needs_you` is a refusal -- which is what the four store-only
`cmd/relevo` CLI fixtures (`TestChainResumeReportlessRoundIsNotInternal`,
`TestStopOnAHaltedChainWithAnOpenMemberRoundIsNotNothingToStop`,
`TestChainResumeOpenMemberRoundIsAConflict`,
`TestChainResumeOpenRoundIsAConflict`) keep passing on, untouched; a live
process on a member that is **not** `needs_you` is a refusal; a probe that
errors is an OS hiccup, not evidence of death; a remote binding keeps round 2's
rule verbatim (NEEDS YOU and no pid), since `Stop` on a remote goes to the
server; `done`, `paused` and `queued` stay left to the resume's own refusal.

**The test set** (`internal/relevo/chain_resume_gated_test.go`):

- `gatedReviewerHalt` no longer scripts the pid dead. It now asserts the halt's
  own premise -- NEEDS YOU, pid kept, pid answers alive -- which is the live
  reproduction above.
- `TestChainResumeHaltedOnAllGatedActorResumes` and its swap twin resume with no
  manual stop, and assert through the fake runner's kill record that the halted
  round's old pid was killed on the way.
- `TestChainResumeRefusesLiveProcess/state needs_you` **flips**: it is now
  `TestChainResumeClosesTheHaltedMemberRoundWithALiveProcess`. `state active`
  with a live process stays a refusal.
- `TestChainResumeProcessDeathAndProbeError` scripts the pid dead itself in the
  two close cases so each keeps testing the pid arm, and moves its probe-error
  case to an `active` member, since past the state arm the probe is the only
  evidence and a NEEDS YOU member never reaches it.

**Mutations, with the test each one fails by name:**

| # | mutation | failing test |
|---|---|---|
| a | drop the `needs_you` arm | `TestChainResumeHaltedOnAllGatedActorResumes` |
| b | close any live process regardless of state (`err == nil`) | `TestChainResumeRefusesLiveProcess/state_active` |
| c | drop the dead-pid arm | `TestChainResumeProcessDeathAndProbeError/dead_process_closes_in_a_state_that_is_not_needs_you` |
| d | drop the PID-0 guard | `TestChainResumeRefusesAPidlessMemberOutsideNeedsYou/state_active` |

Everything else in this document stands as written above; this section is the
only amendment.
