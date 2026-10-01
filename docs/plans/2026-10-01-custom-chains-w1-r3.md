> MasterMind: base is branch `relevo/custom-chains-design`, which already carries the spec (`docs/specs/2026-10-01-custom-chains-design.md`, including section 11, the clarifications this preamble raised) and the sketch -- preamble item 'the spec is not in the tree' is resolved; do not touch docs/specs. Every preamble default is ACCEPTED. Your round is the section after the preamble. Save the preamble plus your round's section to the docs/plans file your round names.

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

## Round 3 — `Validate(def, env) []Problem`

**Files:**
- New: `internal/workflow/validate.go` (the format rules, coverage, refs), `internal/workflow/graph.go` (reachability, dominators, cycles), and `validate_test.go`, `graph_test.go`.
- Optional: split out `validate_refs.go` if `validate.go` gets near 600 lines.

**Types:**
- `Env`:
  - `Actors map[string]ActorInfo{Shape (reader | writer), Outputs}`
  - `Given *Given{Plans, Task bool}`. When `Given` is nil, rule 6 is skipped, as at save time.
  - `Seeds []string`
  - `Workflow func(name string) (Definition, bool)`
- `Problem{Step, Rule, Detail}`, with `String()` giving `step <id>: <rule>: <detail>`. A workflow-level problem has an empty `Step`.
- Each rule is a stable exported constant.

**What `Validate` checks.** It reports one `Problem` per failure, never stops at the first, and returns problems in a deterministic order (sorted by step, then rule).
- **Format:**
  - `name`, step ids and output keys pass `ValidName`. Param names match `^[a-z][a-z0-9_]*$`.
  - Reserved step ids are refused: `done`, `chain`, `params`, `task` and `else`.
  - Reference syntax is valid.
  - A `seed` is inline, `file:<relative path>` (no `..`, not absolute), or `shipped:<name>` with the name in `env.Seeds`.
- **Rule 1:** `start` and every target, including budget `then`, exist, and every step is reachable from `start`.
- **Rule 2:**
  - A step has exactly one kind.
  - A `run`, after `RenderParams`, names an actor in `env.Actors`.
  - A `for-each` names `plans` with `inputs.plans` not `none`, or `{{step.artifact}}` naming a declared artifact.
  - A `run`, `check`, `when` and `budget.max` read params only.
- **Rule 3:** match keys come from the step kind.

  | Kind | Valid matches |
  |---|---|
  | `run` | `done`, `status=<done\|halted\|blocked\|deferred>`, `key=value` for a one-of, `key=0` / `key>0` for a count, `else`. Matching on an artifact is refused by its own rule. |
  | `check` | `green`, `red`, `result=…`, `else` |
  | `for-each` | `next`, `empty`, `else` |
  | `when` | `true`, `false`, `else` |
  | `fork` | `joined`, `conflict`, `halted`, `else` |

  Coverage:
  - Every one-of value and both count arms are covered, or there is an `else`.
  - A `run` with no declared outcomes covers `done` or `else`.
  - A `check` covers `green` and `red`; a `for-each`, `next` and `empty`; a `when`, `true` and `false`; a `fork`, `joined` and `conflict`.
  - `halted` and status values other than `done` may stay uncovered: they halt.
- **Rule 4:** every reference in a seed, and in a halt reason (params only), resolves.

  | Reference | Valid when |
  |---|---|
  | `params.x` | `x` is declared |
  | `task`, `plans.all` | the input is `required` |
  | `chain.diff`, `chain.base`, `chain.branch` | always |
  | `<step>.<attr>` | the step strictly dominates the referencing step, and the attribute fits its kind and shape |

  The attribute must be one of: a declared artifact; `report` or `diff` for a writer; `output` for a reader; `log` for a check; `conflict` for a fork; `current` for a for-each.
- **Rule 5:**
  - No cycle made only of control steps.
  - Every cycle contains a `run` or a `check`.
  - Every cycle passes through a `for-each` or a budgeted step. A budgeted step does not bound a cycle that also contains one of its `per` steps.
  - A budget's `per` names existing steps or `chain`; its `then` is a valid target; its `max` is a non-negative int or an int param.
- **Rule 6:** with `env.Given` set, each input meets its mode: `required` was given, and `none` was not.
- **Rule 7:** each fork child resolves through `env.Workflow` and validates under the same rules with the child's own inputs.
  - An `Each` child gets plans; a `Children` entry gets the inputs it carries.
  - Problems are reported as `step <fork>: fork-child: <child workflow>: <problem>`.
  - A workflow that forks itself is refused through a visited set.
- **Rule 8:** a `when` is exactly one `{{params.x}}`, and `x` is a bool param.

**Graph (`graph.go`):** the edges are each step's `On` targets plus its budget `then`. Halt and `done` targets are sinks.
- `reachable`
- `dominators`: iterative, over the reachable set
- `boundedCycles` (rule 5): first, take the graph minus every `for-each` and every budgeted step; any cycle left is unbounded. Then, for each budgeted step S, check whether S can reach one of its own `per` steps and get back to S without passing another bounding step.

**Steps:**
1. Write the graph helpers and their tests.
2. Write `Env`, `Problem` and the format rules plus rules 1–3.
3. Write rule 4 (references and dominance), then rules 5–8.
4. Build a test fixture `shippedEnv()` in `validate_test.go` (round 6 reuses it):

   | Actor | Shape | Outputs |
   |---|---|---|
   | `builder` | writer | none |
   | `reviewer` | reader | `verdict` one-of `pass`, `changes`; `findings` artifact |
   | `lite-planner` | reader | `plan` artifact |
   | `planner` | reader | `plan` artifact |
   | `security` | reader | `findings` count; `report` artifact |

   **Done when:** `TestValidateDefaultIsClean` returns no problems, and the `triage-first` workflow is clean with a `yes-no` actor added.
5. Run `make check`, save `docs/plans/2026-10-01-custom-chains-w1-r3.md`, and make a new commit.

**Named tests:** one table case per rule, each asserting the `Rule` and the `Step`, in `TestValidateRules`. The cases:
- the start is missing
- a target is missing
- a step is unreachable
- a step has two kinds
- a step has no kind
- a run names an unknown actor
- a for-each has a bad source
- a match is undeclared
- a match is on an artifact
- a one-of value is uncovered
- a count arm is uncovered
- a reference is not dominated
- a reference has a bad attribute
- a reference names a `none` input
- a control-only cycle
- a cycle with no budget
- a budget reset inside its own cycle
- a budget `per` that is unknown
- a budget `then` that is unknown
- the inputs disagree with what was given
- a fork child does not resolve
- a fork child is invalid
- a fork recurses into itself
- a `when` on a non-bool param
- a `when` on a runtime reference
- a seed from an unknown shipped name
- a `file:` seed that escapes the workflow's directory

Also:
- `TestValidateReportsEveryFailureNotTheFirst`
- `TestValidateDefaultIsClean`
- `TestValidateTriageFirstIsClean`
- `TestDominatorsOfTheDefault` (the `correct` step dominates `build-fix`, and `plans` dominates `build`)

**Mutation:** remove the `for-each` exemption from rule 5. `TestValidateDefaultIsClean` must fail.

**Halt if:** the shipped default cannot pass these rules without changing its YAML. That would be a spec error, and it is reported rather than bent.

**Report:**
- every rule constant with its spec rule number;
- that the default validates clean;
- coverage.
