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

## Round 2 — actor output declarations, the prompt footer, the parse of outcomes

**Files:**
- New: `internal/reporttail/blockvalue.go` and `blockvalue_test.go`.
- Modified: `internal/chain/verdict.go`.
- New: `internal/workflow/outputs.go`, `internal/workflow/outcomes.go`, and tests `outputs_test.go` and `outcomes_test.go`.

**Types and functions:**
- **Move the block reader.**
  - `blockValue` and `blockKey` move from `internal/chain/verdict.go:44-82` into `reporttail` as `BlockValue(body []byte, key string) (string, bool)`, with their behaviour unchanged.
  - `chain.ParseVerdict` and `chain.ParseFindings` call `BlockValue`.
  - `internal/chain/verdict_test.go` passes unchanged. That unchanged pass is the proof this is a move, not a fork.
- **Output declarations.**
  - `OutputKind` is `one-of | count | artifact`.
  - `Output{Kind, Values}` decodes from `{one-of: [...]}`, `count` or `artifact`.
  - `Outputs map[string]Output` has the methods `Outcomes()` (the non-artifact keys, sorted) and `Artifacts()`.
  - Strict parsing: a `one-of` must have at least one value, with no duplicates.
- `Footer(o Outputs, artifactPaths map[string]string) string`.
  - Each artifact gets a line naming the file to write: its path from `artifactPaths`, or `<name>.md` when the map has none.
  - Then one fenced `relevo` block with one line per outcome, sorted: `answer: yes   # or: no` and `findings: 0   # a count`.
  - With no outcomes there is no block.
- `ParseOutcomes(o Outputs, bodies ...[]byte) (map[string]string, string)`.
  - The caller passes the final message first, then the stream's assistant texts, newest first.
  - Each outcome key takes the first body that gives a valid value.
  - The second result is the halt reason, or empty. It names the first failing key in sorted order: missing (`verdict: no relevo block carries it`) or invalid (`verdict: "maybe" is not one of pass, changes`; `findings: "-1" is not a count`).
- `MissingArtifact(o Outputs, sizes map[string]int64) string` names the first declared artifact that is absent or empty (`plan: not written or empty`), or returns empty.

**Steps:**
1. Make the `reporttail` move and its tests. **Done when:** `go test ./internal/reporttail/ ./internal/chain/ -count=1` is green, with `verdict_test.go` untouched.
2. Write the output types and their JSON/YAML parsing (a `Definition` does not use them yet; `Env` uses them in round 3).
3. Write `Footer`, `ParseOutcomes` and `MissingArtifact`.
4. Run `make check`, save `docs/plans/2026-10-01-custom-chains-w1-r2.md`, and make a new commit.

**Named tests:**
- `TestBlockValueReadsTheLastBlockCarryingTheKey`
- `TestBlockValueMissesWithoutABlock`
- `TestOutputParsesEachKind`
- `TestOutputRejectsAnEmptyOneOf`
- `TestFooterOneOf`
- `TestFooterCount`
- `TestFooterArtifact`
- `TestFooterWithNoOutcomesHasNoBlock`
- `TestParseOutcomesPresent`
- `TestParseOutcomesMissing`
- `TestParseOutcomesOutOfRange`
- `TestParseOutcomesCountRejectsNegativeAndNonInteger`
- `TestParseOutcomesRecapAfterTheBlockIsHarmless`
- `TestParseOutcomesFallsBackToAnOlderBody`
- `TestMissingArtifactNamesTheEmptyOne`

**Mutation:** in `ParseOutcomes`, accept any value for a `one-of`. `TestParseOutcomesOutOfRange` must fail.

**Halt if:** `verdict_test.go` needs any edit to pass after the move. That would mean the move changed behaviour.

**Report:** confirm `verdict_test.go` is untouched, and give the coverage of `reporttail` (baseline 96.9) and of `workflow`.
