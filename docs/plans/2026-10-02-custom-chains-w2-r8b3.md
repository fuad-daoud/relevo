# Plan 8, round 8b3: a for-each resets its budget scope only when it advances; then the remote e2e moves onto the engine

You halted 8b2 correctly. The five gaps are fixed and committed, and the local e2e runs on the engine. Your open question was the chain row's `Corrections` column at done: the engine shows 0 where the legacy path shows 1.

## MasterMind decision: it is an engine bug against the spec

- Spec section 3.3 says a `budget.per` that names a `for-each` resets "each time it advances".
- The engine also resets the scope when the `for-each` *empties* (routes `empty`). That wipes the last plan's correction count at the end of the chain.
- The legacy path keeps it, and so must the engine.
- No behaviour beyond the count changes: once the plans list is exhausted, nothing reads that step's budget again.

## Do

1. **The reset rule** (`internal/workflow/enter.go`). Entering a `for-each` resets the visits of the steps whose `per` names it ONLY when the `for-each` produces `next`. An `empty` leaves them as they were. Entering a non-`for-each` step that a `per` names still resets as today.
   - Tests in `internal/workflow`:
     - `TestBudgetPerForEachKeepsTheCountWhenEmptied`;
     - `TestBudgetPerForEachResetsOnNext` (the existing reset test may already cover this; if so, name it in the report and do not duplicate it).
   - The W1 equivalence test (`internal/workflow/equivalence_test.go`) must stay green.
2. **8b step 5, remainder.** Move `TestChainRemoteBuilderE2E` onto the engine with `Workflow: "default"`, exactly as the 8b section says. Every behavioural pin stays. A pin that still differs is a halt: report it, do not bend it.
3. **Mutation for the MasterMind** (run it, report it, restore it): reset on `empty` again. `TestBudgetPerForEachKeepsTheCountWhenEmptied` and `TestChainRemoteBuilderE2E` must fail.
4. **Gate:**
   - `make check` and `make e2e` green.
   - Never run `check-coverage.sh --write`; never touch the coverage baseline.
   - New commits.
5. Save this file as `docs/plans/2026-10-02-custom-chains-w2-r8b3.md` and commit it with the code.

## Halt if

- The rule change turns any `internal/workflow` test or the equivalence test red. That would mean the reset-on-empty mattered somewhere: report where.
