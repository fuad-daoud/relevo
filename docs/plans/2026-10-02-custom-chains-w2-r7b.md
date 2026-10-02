# Plan 7, finishing round A: members, sweep and stop, and the missing close/check tests

You halted plan 7 honestly after committing the new-engine core (`5d0c9212`, check green). This round finishes part of what your report listed as NOT DONE. A second finishing round will do the rest, so do only this list.

**Base:** your branch HEAD. **Plan text:** `docs/plans/2026-10-02-custom-chains-w2-r7.md`, in your tree. Its preamble, decisions and constraints all still bind.

## Do

1. **The `ErrCWDTaken` same-chain exemption** (plan step 1, seam `internal/store/lifecycle.go` `assertCWDFree`, plus the preflights in `bind.go` and `add.go`).
   - Members of the same chain may share a tree.
   - A non-member writer on that tree is still refused.
   - Tests: `TestCWDExemptOnlyAmongSameChain`, and `TestWorkflowChainStartCreatesMembersAllOrNone` (a forced failure on the third member leaves no row, no member and no worktree).
2. **Placed writers** (plan step 1, with the MasterMind's preamble decision):
   - `TestPlacedWriterCheckAnsweredFromPulledGate`, for `chainFlowPullCheck`;
   - `TestPlacedWriterTwoCheckCommandsRefused`, which names the server feature `check`.
3. **Workflow-aware sweep and stop** (plan step 4). `chainSweep` and `ChainStop` walk the members in `chain_member`, not the four legacy columns, for a chain with a workflow; legacy rows stay as today. Tests:
   - `TestWorkflowSweepHaltsOnGoneMember`
   - `TestWorkflowStopSendsStopped`
   - `TestWorkflowTerminalDeliversOnce`
4. **The missing close and check tests** (plan steps 2–3):
   - `TestWorkflowSendRendersSeedPaths`
   - `TestWorkflowCloseMissingArtifactHalts`
   - `TestWorkflowEmptyCheckRoutesGreenWithoutRun`
   - `TestWorkflowCheckLogReferenceable` (`{{check.log}}` resolves after the close)
5. **The mutation for the MasterMind** (run it, report it, restore it): in `assertCWDFree`, drop the same-chain condition so that any member is exempt. `TestCWDExemptOnlyAmongSameChain` must fail.
6. **The gate:**
   - `make check` and `make e2e` green.
   - `internal/relevo` coverage must rise back above its floor.
   - Never run `check-coverage.sh --write`. Never touch `testdata/coverage-baseline.txt`.
7. Save this file as `docs/plans/2026-10-02-custom-chains-w2-r7b.md` and commit it with the code, as new commits.

## Halt if

- Any old-path chain test or e2e changes outcome.
- The exemption cannot be limited to same-chain members.
- An item needs more than its named seam.
