# Plan 7, finishing round B: resume and the triage e2e

This finishes the rest of plan 7's NOT DONE list. **Base:** your branch HEAD, after finishing round A. **Plan text:** `docs/plans/2026-10-02-custom-chains-w2-r7.md`; its preamble and constraints bind.

## Do

1. **Resume from a newer manual round** (plan step 5). When the halted `run` step's member has a newer closed round, resume feeds it as that step's `step_closed` (`workflow.Resume` with `Closed`, built by the close path). A stopped run step is re-sent with its own staged prompt. Tests:
   - `TestWorkflowResumeTakesNewerManualRound`
   - `TestWorkflowResumeResendsStoppedPrompt`
   - `TestWorkflowResumeUnknownStepListsSteps`
2. **Resume with `--param`** creates members missing for a branch the param turns on (preamble Q2). Test: `TestWorkflowResumeParamCreatesMissingMember`.
3. **The triage e2e** (plan step 7).
   - The fake harness in `internal/e2e/headless_test.go` learns a yes/no reader, keyed on the seed's first line `Should we build this?`. It answers from the task text, which carries `answer yes` or `answer no`.
   - `TestChainTriageE2E` in `internal/e2e/chain_triage_test.go` covers both edges. The yes run goes triage, then build, then check (`true`), then done. The no run halts with `triage said no`.
   - Add `TestChainTriageE2E` to the Makefile `e2e` filter.
4. `TestRenderTraceLegacyRowUnchanged`: a legacy row renders exactly as before.
5. **A placed writer's check with no pulled gate record yet** (finishing round A reported this gap). `chainFlowPullCheck` saves the chain awaiting the check when the writer's gate record has not been pulled. Nothing then answers it. `tickChainChecks` (or the chain pull, after it installs a builder round) must re-try that pending placed check and feed `check_closed` once the record arrives. Test: `TestPlacedWriterCheckAnsweredWhenTheGateRecordArrivesLater`. The chain awaits; the pull installs the round with its gate record; the next tick routes green or red.
6. **The mutation for the MasterMind** (run it, report it, restore it): make resume ignore a newer manual round. `TestWorkflowResumeTakesNewerManualRound` must fail.
7. **The gate:**
   - `make check` and `make e2e` green, with the triage e2e included.
   - Never run `check-coverage.sh --write`. Never touch the coverage baseline.
8. Save this file as `docs/plans/2026-10-02-custom-chains-w2-r7c.md` and commit it with the code, as new commits.

## Halt if

- The fake harness cannot run a reader keyed on a seed line without changing other e2es' behaviour.
- Any existing e2e changes outcome.
