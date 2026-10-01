# Plan: custom chains, slice W1 (`internal/workflow`, the pure engine)

## Preamble

**Base.** The throwaway tree is at `5fb13dc6`. The seed names main as `7d0257f4`, which is one commit later and touches only `internal/db/wire/owner`. Nothing W1 reads is affected. The builder branches from `7d0257f4`.

**The spec is not in the tree.** The spec says it "ships in W1's PR", but its file (`~/.cache/relevo-chains/inputs/custom-chains-design.md`) and the sketch exist only on the MasterMind's machine, and the builder runs on `zen`. **Default:** before round 1, the MasterMind commits the spec as `docs/specs/2026-10-01-custom-chains-design.md`, with the sketch beside it. No round touches `docs/specs`. Rounds cite the spec by section only in their plan docs, never in code (`scripts/check-comments.sh` fails on `§` and `#NNN`).

**Where the spec and the code disagree.** Each item has the default these rounds build to:

1. **The YAML library.** The seed suggests `sigs.k8s.io/yaml`, but it uses YAML 1.1 rules. Those rules read the key `on`, and the values `yes` and `no`, as booleans, so every step's `on:` map and the spec's own `one-of: [yes, no]` would break.
   - **Default:** `go.yaml.in/yaml/v3` v3.0.4, the maintained YAML 1.2 continuation of `gopkg.in/yaml.v3`. Under 1.2 only `true` and `false` are booleans. It is already in this machine's module cache.
   - **One decode path:** YAML is decoded into a generic value, map keys are turned into strings (because `true:`/`false:` keys under a `when` are booleans even in 1.2), and the result is re-encoded as JSON. That JSON goes through the same strict JSON decoder as a `.json` file, so both formats produce the same `Definition` by construction.
2. **Rule 5 rejects the shipped default as written.** The rule says every cycle needs a `budget`. The plans loop (`plans → build → check → review → plans`) has none. A finite list bounds it instead.
   - **Default:** a cycle that passes through a `for-each` counts as bounded.
   - A budget whose `per` resets on a step inside the same cycle does not bound that cycle.
   - A cycle made only of control steps (`for-each`, `when`) is always rejected.
3. **Params are used where the spec says they are not.** Spec 3.3 says nothing besides seeds, actors, checks, budgets and `when` takes a param, yet the default's halt reasons use `{{params.max_corrections}}`.
   - **Default:** halt reasons (on edges and in `then`) may also read params, and only params.
4. **There is no list-artifact declaration.** Rule 2 and section 4.3 let a `for-each` walk "a list artifact", but section 2 has no list kind.
   - **Default:** a step's artifacts are lists of keys (`map[string][]string`), and a single file is a list of one. A `for-each` may name `{{step.artifact}}` for any declared `artifact` output.
5. **There is no `stop` action.** Section 5 lists no stop action, although `stopped` is an event "as today".
   - **Default:** add `stop`, so the equivalence test can match today's `ActionStop`.
6. **`--no-gate` has no expression in the default.** Today, `Gate: none` goes straight to the reviewer.
   - **Default:** a `check` step whose command renders empty resolves to `green` inside `Next`, with no action and no log. The default stays exactly as written.
7. **Shipped seed names differ.** The spec's `shipped:` names are `repair`, `review`, `correct`, `scan` and `fix`. `internal/chain/seeds` has `reviewer`, `correction`, `security` and `fixes`, and has no repair template.
   - **Default:** W1 ships only the five names, as validation needs them. The templates move in W2.
8. **`fork` is incomplete in the spec.** No event carries the merge result (`joined` or `conflict`), and fork is W3's slice.
   - **Default:** W1 parses and validates `fork`, including rule 7 through an `Env` resolver and its `joined`/`conflict`/`halted` outcomes.
   - Entering a fork step in `Next` halts the chain with "fork steps are not run by this engine".
   - W3 adds the merge event and the execution.
9. **The migration table is off by one and ignores phase.** This affects W2, but W1's equivalence translation meets it first.
   - **Visits:** while the planner is correcting, the workflow has already counted the visit. So `Visits[correct]` is `Corrections + 1` when the chain is at `correct`, and `Corrections` otherwise.
   - **Phase:** `reviewing` and `correcting` in the security phase map to `fix-review` and `fix-correct`.
   - **Building:** `building` maps to `build` when `Corrections == 0` and to `build-fix` when it is above 0. In the security phase it maps to `fix-build` and `fix-rebuild`.
10. **Budget state.** The spec stores `Visits[step]` "with the scope instance".
    - **Default:** plain counts (`map[string]int`). Entering a step resets the count of every step whose `per` names it. Under that rule the scope instance carries no information.
11. **Name rule.** The actor-name pattern is unexported in `internal/roles/actors.go:31` (`^[a-z][a-z0-9-]{0,31}$`), and W2 will make `roles` import `workflow` for actor outputs.
    - **Default:** `workflow` owns `ValidName` with the same pattern. W2 may point `roles` at it.

**Open questions (each has a default):**
- Matching order on a `run` close. The spec's "first match wins" is ambiguous when two exact keys both match. Default:
  1. A status other than `done` matches only a `status=<s>` edge, or else halts.
  2. With status `done`, check declared-outcome exact edges in sorted key order.
  3. Then count arms.
  4. Then `done` / `status=done`.
  5. Then `else`.
- Inputs that a reference may name. Default: `{{task}}` and `{{plans.all}}` require the input to be `required`, as rule 4 says. A `for-each` over `plans` also accepts `optional`.

**Commands for every round:**
- Focused: `go test ./internal/workflow/ -count=1 -cover`
- Full: `make check`, once, at the end of the round.

---

## Round 6 — the equivalence test, coverage baseline

**Files:**
- New: `internal/workflow/equivalence_test.go`, test-only, importing `internal/chain` (`chain` does not import `workflow`, so there is no cycle).
- Possibly a top-up of `*_test.go` files for coverage.
- Modified: `testdata/coverage-baseline.txt`.

**The translation (test helpers):**
- **Definition:** `Default()` with `WithParams` built from `chain.Settings`:

  | Param | From |
  |---|---|
  | `reviewer`, `planner`, `security` | the `Settings` actors (empty means the default) |
  | `scan` | `Security` |
  | `max_corrections` | `MaxCorrections` |
  | `regate` | `Regate` |
  | `gate` | `""` when the event's `Gate` is `none`; `"make check"` otherwise |

- **State:**
  - `At` follows preamble item 9 (by phase and corrections).
  - `Iter[plans] = {Index: Plan-1, Items: plan-1.md … plan-N.md}`.
  - `Visits[correct]` or `Visits[fix-correct]` is `Corrections`, plus 1 when the chain is at that step.
  - `Awaiting` takes the mapped step, the actor of that step, and the round.
  - Status is copied.
- **Events:**
  - **`builder_closed`:**
    - An outcome other than `done` becomes `step_closed` with that status and reason.
    - A `done` outcome becomes `step_closed(done)`, and then the translation answers the engine's actions until the next non-repair send or a terminal state:
      - each `run_check` gets a `check_closed` carrying the event's gate result;
      - each repair send gets `step_closed(done)`.
  - **`reviewer_closed`:** `step_closed`, with outcomes and halt reason from `ParseOutcomes` on a synthetic body carrying the verdict. With no verdict, the body has no block.
  - **`planner_closed`:** with `PlanPresent` false, the halt reason comes from `MissingArtifact`.
  - **`security_closed`:** through `ParseOutcomes` on a findings body.
  - **`needs_you` and `stopped`:** mapped directly.
  - **Events the old engine ignores:** they map to events the guard rejects.
- **Member map (for comparing sends):**

  | Step | Member | Seed |
  |---|---|---|
  | `build`, `build-fix`, `fix-build`, `fix-rebuild` | builder | none (old) ↔ a single-reference seed (new) |
  | `review`, `fix-review` | reviewer | `SeedReviewer` ↔ `shipped:review` |
  | `correct`, `fix-correct` | planner | `SeedCorrection` ↔ `shipped:correct` |
  | `fix-plan` | planner | `SeedFixes` ↔ `shipped:fix` |
  | `scan` | security | `SeedSecurity` ↔ `shipped:scan` |

  Repair sends are internal to a translated builder close and are not compared.

**Steps:**
1. Write `TestEquivalenceWithChainNext`. It is a table with one case per `TestNext*` function in `internal/chain/chain_test.go`, using the same state and event. That covers 25 functions; cases with several sub-cases (budget halt at 3 and at 0; verdict `""` and `"maybe"`; the three ignored events; the three terminal statuses) get one row each.
   - Each row runs `chain.Next` live, then the translated replay.
   - It asserts the same send per member and seed, the same terminal status, and the same no-op.
   - A halt reason must equal the old reason, or the new reason listed in the case's `reworded` field.
2. Write `TestEquivalenceCoversEveryChainNextCase`. It reads `../chain/chain_test.go`, collects each `func TestNext…` name, and fails on any name the table lacks.
3. Write `TestEquivalenceScenarios`, which drives both engines from `Start` with whole event scripts. It compares the send sequence per member and the terminal status. Scenarios:
   - two plans pass, no security;
   - two plans, security clean;
   - security findings, then fix, fix-review changes, a correction, then pass;
   - corrections exhausted on plan 2, after plan 1 used two (the per-plan reset);
   - a red gate with regate 1 and with regate 0;
   - a builder blocked mid-chain;
   - `needs_you` during review.

   The old engine has no start, so its script begins at its first builder send.
4. Top up tests until `internal/workflow` reaches at least 90% statement coverage, with no test written only to touch lines that pins nothing.
5. **Coverage baseline.** Run `make check-test`, then `go env GOVERSION GOOS GOARCH`.
   - **If that matches the baseline header (`go1.27 linux/amd64`):**
     1. Run `sh scripts/check-coverage.sh --write`.
     2. In the diff, keep the added `internal/workflow` line and any rises. Restore every line that dropped, because no round lowers a baseline.
     3. Report both.
   - **If it does not match:** do not run `--write`, because a changed header fails CI's `RELEVO_REQUIRE_COVERAGE` leg. Insert the measured `internal/workflow` line by hand, in sorted position, and say so.
6. Run `make check`, save `docs/plans/2026-10-01-custom-chains-w1-r6.md`, and make a new commit.

**Named tests:**
- `TestEquivalenceWithChainNext`
- `TestEquivalenceCoversEveryChainNextCase`
- `TestEquivalenceScenarios`

**Mutation:** in `enter.go`, do not reset `Visits[correct]` when `plans` advances. `TestEquivalenceScenarios` (the case "corrections exhausted on plan 2") must fail.

**Halt if:**
- Any case differs in sends or terminal status. That is a real non-equivalence: report the case and the two outcomes, and change neither engine nor the default to force agreement.
- A difference needs a halt reason the default cannot produce.

**Report:**
- the full table of reworded halt reasons, old → new: at least the builder halted, blocked and unstructured cases, "reviewer gave no verdict" in both forms, "planner wrote no plan" in both phases, and "security gave no finding count";
- how the coverage baseline was written (`--write`, or inserted by hand) and the resulting number;
- `git diff --stat main...HEAD` for the whole slice: only `internal/workflow/**`, `internal/reporttail/blockvalue*.go`, `internal/chain/verdict.go`, `go.mod`, `go.sum`, `testdata/coverage-baseline.txt` and the six `docs/plans` files.

---

**Every round's report must include:**
- the focused test output and the `make check` result;
- the mutation, applied and then reverted, with the named failing test;
- `git diff --stat` against the round's declared files;
- any halt, with the step it happened at.
