# Plan 8, round 8b2: close the five engine gaps 8b found, then finish 8b's steps 5 and 6

You halted 8b correctly at step 5. Steps 1–4 are committed as `bba897d5`. Your probe found five ways the engine differs from the legacy path. The MasterMind decides each one below.

**Base:** your branch HEAD (`bba897d5`). **Plan text:** `docs/plans/2026-10-02-custom-chains-w2-r8b.md`, the 8b section (`round-8b`). Its preamble and constraints bind: the gate, no `--write`, no exclusions.

## The five gaps: fix each with a named test

1. **Render `shipped:` seeds.** `chainRenderSeed` (`internal/relevo/chain_render.go`) renders a `shipped:<name>` seed through `workflow.RenderShipped` with the same view the legacy path builds, and only then resolves `{{…}}` references.
   - Test: `TestWorkflowSendRendersShippedSeed`. A `review` step's send carries the review template's text, not the literal `shipped:review`.
2. **A reader close routes on its declared outcomes.** In `chainEventFromCloseWF` (`internal/relevo/chain_close.go`), a reader's step `status` is NOT the saved summary's report outcome, because that reads `unstructured` once the block is stripped.
   - The status is the relevo `status:` value from the same block-carrying message the outcomes are parsed from (`done | halted | blocked | deferred`). It is `done` when that message carries outcomes but no status line.
   - A missing declared outcome still closes the step halted, through `ParseOutcomes`' reason.
   - Tests: `TestWorkflowReaderCloseRoutesOnVerdictNotSummaryOutcome` and `TestWorkflowReaderHaltedStatusHalts`.
3. **Parse the closed round, not the binding's current round.** `chainParseOutcomes` (`internal/relevo/chain_seed.go`) reads the stream and the output of the round `chainCloseWF.Round` carries (`reconcile.go`), not `b.Round`, which the close has already advanced.
   - Test: `TestWorkflowParseOutcomesReadsTheClosedRound`.
4. **The `Plan` column at done.** When the `for-each` is exhausted, `workflow.LegacyView` (`internal/workflow/legacy.go`) must report `Plan = Items`, the last plan, as the legacy path leaves it.
   - Index −1 means "not yet entered" only while the step has never produced `next`. Tell the two apart from the state (`Results["plans"]` present, or `Iter.Index == -1` with `Items > 0` after an `empty`). Keep the distinction inside `internal/workflow`, as a field on `Iter` if needed (e.g. `Done bool`).
   - Tests: `TestLegacyViewPlanAtDoneIsTheLastPlan` and `TestLegacyViewPlanBeforeStartIsOne`.
5. **Capture the cumulative plan diff on the engine path.** When the engine's `for-each` advances to a new plan, record that plan's start commit. That is the legacy `PlanStartCommit` rule in `internal/relevo/chain.go`: a send that starts a plan records the round's baseline head. Then `chainPlanDiff` captures the plan diff when the plan's review step is seeded, and the reviewer seed names the copy again.
   - Do it in the engine adapter (`chain_engine.go`), on the send that follows a `next` from the plans `for-each`.
   - Test: `TestWorkflowReviewerSeedNamesThePlanCumulativeDiff`.

## Then finish 8b

- **Step 5:** move the local and remote e2e onto the engine with `Workflow: "default"`, exactly as 8b's section says. Every behavioural pin stays. If a pin still differs after the five fixes, halt and report the difference. Do not bend the pin.
- **Step 6:** save the 8b section and this file as `docs/plans/2026-10-02-custom-chains-w2-r8b.md` and `…-r8b2.md`.

## Mutations for the MasterMind (run each, report each, restore each)

- In `chainRenderSeed`, skip `RenderShipped`. `TestWorkflowSendRendersShippedSeed` and `TestChainE2E` must fail.
- Make `LegacyView` clamp the exhausted index to 1 again. `TestLegacyViewPlanAtDoneIsTheLastPlan` must fail.

## Gate

- `make check` and `make e2e` green.
- Never run `check-coverage.sh --write`; never touch the coverage baseline.
- New commits only.

## Halt if

- A fix needs the legacy `chain.Next` or any change in `internal/chain`.
- An e2e pin still differs after the five fixes.
- Gap 2 cannot get the reader's status without changing the saved-output format.
