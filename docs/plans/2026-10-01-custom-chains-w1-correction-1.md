# W1 correction: the equivalence test must see the repair path

Base: branch `relevo/cc-w1-m1` (W1's six rounds, plus current main merged in, plus the coverage baseline fixed to add only internal/workflow). Re-check line numbers against it. New commits only.

## What the MasterMind found

The equivalence test (`internal/workflow/equivalence_test.go`) is blind to the default workflow's check/repair routing. In `shipped/default.yaml`, swapping the `check` step's edges to `{ green: repair, red: review }` leaves every test in the package green:

- `equivReplay` answers each repair send with `step_closed(done)` and keeps looping.
- The comparison (around line 923) drops repair sends.

So a green check that wrongly runs repairs until the budget is spent still reaches the reviewer, and the test passes.

## The fix (test-only, in equivalence_test.go)

In `equivReplay`'s builder-close loop, count the repair sends (`repair` / `fix-repair`) and run_check actions one old builder close produces. Assert today's exact semantics, which an old close implies:

| old `Gate` | expected repair sends | expected run_check actions |
|---|---|---|
| green | 0 | 1 |
| none | 0 | 0 (the empty check routes green inside Next) |
| red | exactly `Settings.Regate` (the old close only reports red after the budget is spent) | `Regate + 1` |

A mismatch fails the case with a message naming the case, the gate, and the counts expected versus got. Keep dropping repair sends from the per-member send comparison, because they have no old counterpart. The count assertion is what pins them.

Add `TestDefaultCheckRoutesGreenToReviewAndRedToRepair` in shipped_test.go: on `Default()`, the `check` step's `on` maps `green` to `review` and `red` to `repair`, and `fix-check` maps `green` to `fix-review` and `red` to `fix-repair`. That pins the YAML directly.

## Mutation (run it, report it, restore it)

In `shipped/default.yaml`, swap the `check` step's edges to `{ green: repair, red: review }`. The equivalence test (the green-gate cases) and `TestDefaultCheckRoutesGreenToReviewAndRedToRepair` must both fail. Report the exact failing names.

## Then

`make check` and `make e2e` green. Save this plan as `docs/plans/2026-10-01-custom-chains-w1-correction-1.md`, and commit it with the change.

## Halt if

- Making this green needs a change to non-test code. That would mean the engine's repair semantics differ from today's, which is a real finding to report, not to paper over.
- Any existing case's expectation has to change.
