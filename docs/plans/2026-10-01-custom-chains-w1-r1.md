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

## Round 1 — definition types, YAML/JSON parsing, references, params, the shipped default

**Files (all new):**
- `internal/workflow/definition.go`
- `internal/workflow/parse.go`
- `internal/workflow/refs.go`
- `internal/workflow/params.go`
- `internal/workflow/shipped.go`
- `internal/workflow/shipped/default.yaml`
- Tests: `parse_test.go`, `refs_test.go`, `params_test.go`, `shipped_test.go`
- Modified: `go.mod` and `go.sum`, adding `go.yaml.in/yaml/v3 v3.0.4`.

**Types (in `definition.go`; the package comment goes here, 1–3 lines):**
- `Definition{Name, Description, Inputs, Params, Start, Steps}`.
  - `Inputs{Plans, Task InputMode}`, where `InputMode` is `none | optional | required`. An absent input reads as `none`.
  - `Params map[string]Param`. A `Param` keeps its kind (bool, int or string) and its value.
  - `Steps map[string]Step`.
- `Step` keeps every field that was present, so `Validate` can report "more than one kind" by step name; parsing never decides semantics.
  - Kind fields: `Run`, `Check`, `ForEach`, `When` (strings) and `Fork *Fork`.
  - Other fields: `Seed`, `Budget *Budget`, `On map[string]Target`.
  - `Step.Kinds()` lists the kind keys that are present.
- `Target`: a step id, `done`, or a halt with a `Reason`. It decodes from a string or from `{halt: "…"}`.
- `Budget{Max, Per, Then}`.
  - `Max` is an int or a param reference string.
  - `Per` is a step id, a list of step ids, or `chain`.
  - `Then` is a `Target`.
- `Fork`, with exactly one of two forms:
  - `Each` (a list reference) plus `Workflow`;
  - `Children []ForkChild{Workflow, Plans, Task}`.

**Functions:**
- In `parse.go`:
  - `Parse(data []byte) (Definition, error)`: JSON when the first non-space byte is `{`, YAML otherwise.
  - `ParseJSON` and `ParseYAML`.
  - Decoding is strict: unknown fields and wrong types are errors that name the path.
- `MarshalJSON` on `Target`, `Budget`/`Per`, `Param` and `Step`. Marshal followed by Parse must return an equal `Definition`, because JSON is the stored form.
- In `refs.go`:
  - `Ref{Root, Attr}`.
  - `Refs(template string) ([]Ref, error)` finds every `{{…}}`, trimming spaces. An unterminated or empty reference is an error.
  - `IsSingleRef(template)` is the "exactly one reference" test from section 3.3.
- In `params.go`:
  - `RenderParams(def, s string) string` replaces `{{params.x}}` inline and leaves every other reference untouched.
  - `WithParams(def, map[string]string) (Definition, error)` parses each value into the default's kind. An unknown key is an error that lists the params the workflow takes, sorted, as W2's usage error needs.
  - `ValidName(string) error` uses the actor pattern.
- In `shipped.go`:
  - `default.yaml` is embedded with `go:embed`.
  - `Default() Definition` parses it and panics only on a build defect. A test pins that it parses.
  - `ShippedSeeds() []string` returns `repair`, `review`, `correct`, `scan` and `fix`.

**Steps:**
1. Run `go get go.yaml.in/yaml/v3@v3.0.4`, then `go mod tidy`. **Done when:** `go.mod` gains exactly one direct requirement.
2. Write the types and the strict JSON decoding. **Done when:** `TestParseJSONReadsEveryField` passes.
3. Add the YAML path: generic decode, key-to-string conversion, re-encode, the same JSON decoder. **Done when:** `TestParseYAMLAndJSONGiveTheSameDefinition` passes.
4. Add the marshal side. **Done when:** `TestDefinitionRoundTripsThroughJSON` passes on `Default()` and on the `triage-first` workflow.
5. Write `Refs`, `IsSingleRef`, `RenderParams`, `WithParams` and `ValidName`, each with its tests.
6. Copy spec section 3.1 verbatim into `shipped/default.yaml` and wire up `Default()`. **Done when:** `TestDefaultIsTheSpecDefault` passes: 15 steps, the listed params with their kinds, and `start: plans`.
7. Run `make check`. Save this round's section to `docs/plans/2026-10-01-custom-chains-w1-r1.md` and commit it together with the code, as a new commit.

**Named tests:**
- `TestParseYAMLAndJSONGiveTheSameDefinition`
- `TestParseKeepsOnYesNoAsStrings`: the keys `on`, and the values `yes` and `no`, stay strings.
- `TestParseReadsTrueFalseKeysUnderWhen`
- `TestParseRejectsAnUnknownField`
- `TestParseRejectsAWrongType`
- `TestParseKeepsEveryKindKeyOfAStep`: a step with both `run` and `check` parses, and both fields are kept.
- `TestTargetDecodesStepDoneAndHalt`
- `TestBudgetPerAcceptsStepListAndChain`
- `TestDefinitionRoundTripsThroughJSON`
- `TestRefsFindsEveryReference`
- `TestRefsRejectsAnUnterminatedReference`
- `TestRenderParamsLeavesOtherReferences`
- `TestWithParamsParsesToTheDefaultsKind`
- `TestWithParamsRejectsAnUnknownParamListingTheKnownOnes`
- `TestDefaultIsTheSpecDefault`

**Mutation:** in the YAML path, drop the key-to-string conversion. `TestParseReadsTrueFalseKeysUnderWhen` must fail.

**Halt if:**
- `go.yaml.in/yaml/v3` cannot be fetched.
- The spec's default YAML fails to parse under YAML 1.2.
- The round would need a second new dependency.

**Report:** the dependency and why, the `go.mod` diff, and the package's coverage.
