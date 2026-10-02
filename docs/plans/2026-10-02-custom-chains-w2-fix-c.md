# W2 review fixes, round C: the check step's lifecycle

**Base:** your branch HEAD, after fix rounds A and B. Constraints as in every W2 round.

## Fix

1. **Reviewer bug 5: `relevo stop` and `relevo done` never kill an in-flight check.**
   - `chainStopWorkflowDirect` (`chain_verbs.go:121`) only writes the stopped state, and `ChainDone` leaves the check process alone.
   - `chainAdvanceOneCheck` (`chain_check.go:135`) returns at the first unsettled run, so after a stop and resume during a long check, run 2 starts beside the orphaned run 1 on the same tree.
   - **Fix:**
     - stop and done kill the chain's running check (its scope, as the gate runner's own stop does) and record that run as stopped;
     - a resume never starts a check while an earlier run of that chain is still alive;
     - `done` never releases the worktree under a live check.
   - Tests: `TestWorkflowStopKillsTheRunningCheck`, `TestWorkflowDoneKillsTheRunningCheck` and `TestWorkflowResumeDoesNotRunTwoChecksAtOnce`.
2. **Security S3: the check spawns before its tracking row exists.**
   - In `chainStartCheck` (`chain_check.go:49` vs `:58`), write the `ChainCheckRow` first, as "starting", then spawn and fill in the PID.
   - If the spawn fails, mark the row failed.
   - If the row write fails after a spawn, kill the process.
   - Test: `TestChainStartCheckKillsTheProcessWhenItsRowCannotBeWritten` (inject a `ChainCheckPut` failure).
3. **Security S2: `--gate` / `--no-gate` on resume is silently ignored when the writer is placed on a server.**
   - The base code had `resumeRemoteGateRefusal`; W2 dropped it.
   - Restore an equivalent refusal for a chain with a workflow: a resume that changes the gate (by flag, by `--param gate=…`, or the wire's `gate` field) on a chain whose writer member is placed remotely is refused as a usage error. The error says the server has no route to change a served binding's gate yet; W4 adds it.
   - Test: `TestWorkflowResumeRefusesAGateChangeOnAPlacedWriter` (local resume, plus the served resume through `resumeOptionsFromWire`).
4. **Minor: the check tick walks every chain on each tick.** Guard `chainAdvanceOneCheck`'s caller so terminal chains, and chains with no awaited check, are skipped before any `ChainCheck` lookup.
   - Test: `TestCheckTickSkipsTerminalChains`.
5. **Minor: the repeated-red comparison crosses plans.** `chain_engine.go:250` compares against `Results[step]`, which persists across plans. Compare only within the current repair loop: the previous red must belong to the same for-each item, by `Iter` position. A plan's first red is never a "repeat".
   - Test: `TestRepeatedRedDoesNotCrossPlans`.

## Mutations for the MasterMind (run each, report each, restore each)

- Stop no longer kills the check: `TestWorkflowStopKillsTheRunningCheck` must fail.
- Drop the placed-writer gate refusal: `TestWorkflowResumeRefusesAGateChangeOnAPlacedWriter` must fail.

## Gate

- `make check` and `make e2e` green.
- Save this file as `docs/plans/2026-10-02-custom-chains-w2-fix-c.md` and commit it with the code.
