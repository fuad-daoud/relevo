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

## Round 4 — State, Event, Action, `Start`, `Next`

**Files:**
- New: `internal/workflow/state.go` (the types and their JSON Encode/Decode), `internal/workflow/next.go` (`Next` and close routing), `internal/workflow/enter.go` (entering a step, budgets, the control walk, targets), and tests `next_test.go` and `state_test.go`.

**Types:**
- `Status`: `running`, `halted`, `stopped`, `done`.
- `State{Status, Reason, At, Awaiting, Visits map[string]int, Iter map[string]Iter, Results map[string]Result}`.
  - `Awaiting{Step, Member, Round, Run, Children}`.
  - `Iter{Index, Items}`.
  - `Result{Round, Status, Outcomes map[string]string, Artifacts map[string][]string}`.
- `Inputs{Plans []string, Task string}`.
- `Event{Kind, Step, Member, Round, Run, Status, Outcomes, Artifacts, Result, Log, Child, Reason}`.
  - Kinds: `step_closed`, `check_closed`, `child_ended`, `needs_you`, `stopped`.
- `Action{Kind, Step, Actor, Seed, Command, Reason}`.
  - Kinds: `send`, `run_check`, `fork`, `merge`, `finish`, `halt`, `stop`.
- `EncodeEvent`/`DecodeEvent` and `EncodeAction`/`DecodeAction` mirror `internal/chain/chain.go:391-451`: an unknown kind is an error.

**Behaviour:**
- **`Start(def, in Inputs) (State, []Action)`:**
  - Sets `Iter` for every `for-each` whose source is the `plans` input, with `Items` from `in.Plans` and `Index` at -1 (not yet entered).
  - Then enters `def.Start`.
- **`Next(def, s, e) (State, []Action)` guard:** a terminal state, or an event that does not match `Awaiting`, returns `s` unchanged and no actions.
  - A `run` is matched by step, member and round.
  - A `check` is matched by step and run.
  - `needs_you` and `stopped` match the same identity as the awaited kind.
- **Entering a step:**
  1. Reset `Visits` for every step whose `per` names this one. For a `for-each`, that happens on every entry, which advances or empties it.
  2. If the step has a budget, increment its visit count. When the count passes `max` (params rendered), go to `then` without running the step.
  3. Act by kind:
     - `run`: a `send`, with the actor taken from `RenderParams(run)` and the seed raw. Set `Awaiting{Step, Member: actor}` with `Round` zeroed for the caller to fill, as `internal/chain/chain.go:364-368` does.
     - `check`: render the command. An empty command routes as `green` with no action. Otherwise emit `run_check` and await with `Run` zeroed.
     - `for-each`: advance the index. With an item, route `next`. Past the end, route `empty` and reset: the index goes back to -1 for an input source, and an artifact source is re-read from `Results` on its next entry.
     - `when`: route on the bool param.
     - `fork`: halt with "fork steps are not run by this engine".
- **Targets:**
  - `done` finishes.
  - `{halt}` halts with its reason, params rendered.
  - A step id is entered.
  - `At` is the step whose edge or budget produced a terminal state.
- **Control walk:** walking control steps is capped at `len(def.Steps)+1` per transition, because a valid workflow cannot revisit a control step without running or checking something. Past the cap the chain halts, and the reason names the step.
- **Routing a `step_closed`:**
  - Record `Results[step]`, then match per the preamble's order.
  - A status other than `done` with no matching edge halts with `<step> <status>: <reason>`. When the reason is empty, it is `<step> <status>`.
  - A close with status `done` that matches nothing also halts, with a reason naming the step and its outcomes. Validation makes this unreachable.
- **Routing a `check_closed`:** record the outcome `result` and the artifact `log`, then route.
- **`needs_you`** halts with the event's reason. **`stopped`** sets status `stopped` and emits `stop`.
- **`child_ended`** is ignored until W3, because no state ever awaits children.

**Steps:**
1. Write the types and their Encode/Decode, with round-trip tests.
2. Write `Start` and the entering logic for each kind, and the targets.
3. Write `Next` routing for each event kind, the guard, budgets and the cap.
4. Run `make check`, save `docs/plans/2026-10-01-custom-chains-w1-r4.md`, and make a new commit.

**Named tests:**
- **Start:** `TestStartSendsTheBuilderTheFirstPlan`, `TestStartWithNoPlansGoesToScan`.
- **Run step:**
  - `TestNextRunDoneFollowsDone`
  - `TestNextRunOneOfFollowsTheValue`
  - `TestNextRunCountZeroAndAboveZero`
  - `TestNextRunUnmatchedStatusHaltsWithTheRunnersReason`
  - `TestNextRunMatchedStatusIsFollowed`
- **Check step:** `TestNextCheckGreenAndRed`, `TestNextEmptyCheckIsGreenWithoutAnAction`.
- **Control steps:** `TestNextForEachNextAndEmpty`, `TestNextWhenTrueAndFalse`.
- **Fork:** `TestNextForkHalts`.
- **Targets:** `TestNextHaltTargetRendersParams`, `TestNextDoneFinishes`.
- **Budgets:**
  - `TestNextBudgetPastMaxGoesToThen`
  - `TestNextBudgetMaxZeroGoesStraightToThen`
  - `TestNextBudgetResetsWhenForEachAdvances`
  - `TestNextBudgetResetsWhenAPerStepIsEntered`
  - `TestNextBudgetPerChainNeverResets`
- **Guard:**
  - `TestNextIgnoresAnotherStepMemberOrRound`
  - `TestNextIgnoresAnotherCheckRun`
  - `TestNextReplayedCloseDoesNotAdvanceTwice`
  - `TestNextTerminalIsInert`
- **Other:**
  - `TestNextSendZeroesTheAwaitingRound`
  - `TestNextControlWalkCapHalts` (built on an invalid definition with a `when → when` loop, without calling `Validate`)
  - `TestNextNeedsYouHalts`
  - `TestNextStoppedStops`
  - `TestStateEventActionRoundTripThroughJSON`
  - `TestDecodeRejectsAnUnknownKind`

**Mutation:** skip the `Visits` reset when a `for-each` advances. `TestNextBudgetResetsWhenForEachAdvances` must fail.

**Halt if:**
- Any function would exceed 70 lines, or gocognit 30, without splitting by concern.
- The default cannot reach a terminal state on any path.

**Report:**
- the halt-reason formats;
- the cap value and why it is that value;
- coverage.
