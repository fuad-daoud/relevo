# Correction plan — W1 round 4 fix-up: the walk cap must cover every non-progressing route

**Base:** `e16b0731` on `relevo/cc-w1` (round 4). One new commit on top. Never amend, rebase, reset, cherry-pick, merge or force-push — the chain's clients fetch increments from this branch.

**Input:** the round-4 review (`cc-w1-rev/004-reviewer/findings.md`) — one confirmed bug, everything else checks out. This plan answers that bug and nothing else.

## The bug, as confirmed

`enter`'s budget intercept (`internal/workflow/enter.go:41-46`) routes on with `walked` unchanged. The only cap checks live in `walkForEach`/`walkWhen` (`enter.go:142-155`, `188-197`), which the intercept never reaches, so an exhausted budget whose `then` target loops through other exhausted budgeted steps recurses `enter → route → enter` (`next.go:235`) without bound. The reviewer's definition — `c` on `done` → `d`; `d` and `e` each `Budget{Max: 0, Per: {Chain: true}, Then: the other}`, `on done: done` — passes `Validate` (`boundedCycles`, `graph.go:242-304`, marks every budgeted step bounded and skips `Per.Chain` in the resets-inside-its-cycle check), and `Next` on `c`'s `done` close dies with `fatal error: stack overflow` (`workflow.enter → workflow.route → …`). Reachable from a saveable workflow; not a recoverable panic.

## The fix, and the alternative not taken

Take the reviewer's first alternative: **the budget intercept consumes one unit of the transition-wide walk cap and checks that cap before routing on.** Do not touch `boundedCycles`/`validate*.go` in this round. The second alternative needs a new rule-5 criterion — a budget bounds a cycle only if its `then` leaves that cycle *and* its `per` names none of its steps; the reviewer's literal wording ("`Per.Chain` … contains no step that can … reset") read strictly would also reject a legitimate chain-scoped retry whose `then` halts, contradicting preamble item 2. The engine fix already turns the reported repro into a halt with the existing reason, so the validator question is flagged for the MasterMind in the report instead of smuggled into a correction.

**Behaviour after the fix.** Within one `Start`/`Next` transition, every entry into a step that routes on without emitting an action consumes one unit of the walk; when the units consumed in that transition exceed `len(def.Steps)+1`, the run halts with `capReason` naming the step. `for-each`/`when` already obey this (the `+1` at their call sites). The routes that did not are exactly two: the budget intercept, and `runCheck`'s empty-command route (`enter.go:129-137`, line 136). Consume **per entry, not per edge**: an entry that redirects costs one unit, like a `when` entry, so a path of distinct steps cannot trip the cap by itself and no legitimate redirect halts.

**Cases that must hold:**
- The reviewer's definition halts: `StatusHalted`, reason `control walk exceeded 4 steps at d` (entry order may land on `e`; either is fine, it must name the step), exactly one `ActionHalt`.
- A single exhausted budget whose `then` is a step still enters that step and runs it — no halt.
- An empty check that routes back into itself halts at the cap instead of recursing. This shape is not reachable through today's `Validate` (a budgetless, for-each-less cycle of run/check steps is already rejected), but `Next`/`Start` are exported and the existing cap test deliberately hands the engine an unvalidated definition; the engine's promise is that it halts, not crashes.
- Nothing else moves: guard identity, matching order, visits, iteration, results, halt formats, cap value (`len(def.Steps)+1`), reason string (`capReason`, `enter.go:222-225`).

## Seams

- `internal/workflow/enter.go`: `enter` (34-62) intercept block (41-46) — consume one unit, check the cap, then `route` with the consumed value; `runCheck` (129-137) — same at line 136; comments at 31-33 and 129-130 updated to the new invariant.
- `internal/workflow/next_test.go`: new tests beside `budgetDef` (283) and the budget tests (290-361); the empty-check test sits at 178; the cap test at 441.
- New doc: `docs/plans/2026-10-01-custom-chains-w1-r4-fixup.md` (this plan's body, in the same commit). The r4 plan doc itself is not touched.
- Not touched: `state.go`, `next.go`, `graph.go`, `validate*.go`, `shipped/`, `Default()`.

## Steps

1. Add `TestNextBudgetRedirectLoopHalts` (the reviewer's three-step definition; assert `StatusHalted`, reason has prefix `control walk exceeded` and names `d` or `e`, and `only` is one `ActionHalt`). Deliverable: the test. Know it worked: `go test ./internal/workflow/ -run TestNextBudgetRedirectLoopHalts -count=1` dies with `fatal error: stack overflow` before the fix — that is the reproduction; capture the first frames.
2. Add `TestNextBudgetPastMaxRedirectsToAStep` (a definition with `q` on done → `a`; `a` `Max: 1, Per: {Chain: true}, Then: StepTarget("b")`; `b` a run step; seed `Visits["a"] = 1`; assert the action is a `send` to `b`, not a halt) and `TestNextEmptyCheckRouteConsumesTheWalk` (`tdef` with one check whose command is `{{params.gate}}`, `gate` an empty string param, `on green` back to itself, no `Validate`; assert halted at the cap). Deliverable: both tests. Know it worked: the first passes before and after; the second dies with the stack overflow before step 4.
3. Fix `enter`'s intercept: consume one unit, halt with `capReason` when the consumed count exceeds `len(def.Steps)+1`, else route on with it. Deliverable: no recursion past the cap. Know it worked: step 1 and 2's first test pass; `TestNextBudgetPastMaxGoesToThen`, `TestNextBudgetMaxZeroGoesStraightToThen`, `TestNextBudgetResetsWhenForEachAdvances`, `TestNextBudgetResetsWhenAPerStepIsEntered`, `TestNextBudgetPerChainNeverResets`, `TestNextControlWalkCapHalts` still pass.
4. Fix `runCheck`'s empty route the same way. Deliverable: the sibling route consumes too. Know it worked: `TestNextEmptyCheckRouteConsumesTheWalk` and `TestNextEmptyCheckIsGreenWithoutAnAction` pass.
5. Mutation: revert only step 3's consumption (route with `walked` again) and run step 1's test — it must die with the stack overflow; restore. Same for step 4 if cheap. Record command and outcome.
6. Focused `go test ./internal/workflow/ -count=1 -cover`; then `make check` once, green. Save the fixup doc; one new commit (e.g. `fix(workflow): the budget intercept walks the control cap`).

## Halt if

- A legitimate single redirect (step 2's first test) halts — the accounting is wrong; halt and report, do not widen the cap.
- The fix requires touching `next.go`'s `route`, `graph.go`, `validate*.go`, the cap value, or a reason string — that is beyond the chosen alternative; halt and report.
- Any function would exceed 70 lines or gocognit 30 without splitting, or `make check` cannot be green.
- The tests say the reviewer's definition still recurses after the fix.

## Deleted (closed list)

1. Nothing — no file, function, type, constant, test, action kind, status, reason string, cap value or default step is removed. If the builder believes something must be deleted to make the fix work, halt.

## Report must include

- the reproduction before the fix (command, the `fatal error: stack overflow` line, first frames) and the halt observed after (status, `At`, reason, the single action) for the reviewer's definition;
- the three new tests by name, plus the existing budget/empty-check/cap tests re-run;
- the mutation: what was reverted, the command, the stack overflow, the restore;
- focused coverage and `make check` (both legs);
- the choice: `boundedCycles`/`Validate` untouched; the definition remains `Validate`-accepted and now halts at the cap; the residual rule-5 question (a budget whose `then` stays inside its own cycle) flagged for the MasterMind with the criterion it would need;
- the fixup doc path, the new commit, base `e16b0731`, and that no history was rewritten;
- the relevo block.
