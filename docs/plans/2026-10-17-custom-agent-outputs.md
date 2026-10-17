# Plan: #1254 -- a custom agent can be a chain reviewer, and relevo says how

## What the code says (read, not run)

- Outcomes live on the actor: `roles.Actor.Outputs` (`internal/roles/actors.go:53`). `FromActors` calls `EffectiveOutputs(actor.Agent, actor.Outputs)` (`internal/roles/actors_convert.go:62`); the registry copies `row.Outputs` into the role (`internal/roles/registry.go:~255`), and `WorkflowActors()` / `ActorInfo()` (`registry.go:~505-535`) hand them to `workflow.Validate`. `EffectiveOutputs` (`internal/roles/outputs_default.go:25`) falls back to the shipped table only when the agent name is shipped; a custom agent with no `outputs` yields nil, so rule 3 (`internal/workflow/validate_match.go`, `checkOutcomeMatch` ~line 66) reports `names undeclared outcome "verdict"` and `checkRunCoverage` reports `uncovered: done`.
- **Does `relevo config set actors.X.outputs '<json>'` already fix the issue's dry-run? By reading, yes; not executed.** `configSetRun` (`cmd/relevo/config_edit.go:95`) merges the key into the stored `actors` body; `ParseActors` accepts `outputs` via `Outputs.UnmarshalJSON` (`internal/workflow/outputs.go:60`); a non-empty declaration wins in `EffectiveOutputs`; a reader may declare at most one artifact (`verdict` + `findings:"artifact"` is fine). The default workflow's `verdict=pass|changes` edges then match and coverage is complete. The round prompt footer is also built from the actor's outputs (`workflow.Footer`), so the custom reviewer's round is told the `verdict` contract. **Step 1 confirms this in an env sandbox before any code change; if the dry-run still fails, halt and report the remaining problem lines.**
- `ParseAgents` / `ParseActors` use plain `json.Unmarshal`: unknown keys (`outcomes`, `role`) are ignored on load, which is why `config set` stored them silently. `config.Store.Put` -> `Validate` -> these parsers is the only write-time check. The same parsers run on load, on sync apply, `config import` and `config edit`, so strictness must not go into them.
- Precedent: `internal/roles/file.go:~306-380` (`rolesUnknownKeyWarnings`, `jsonshape.Keys`, `hasShapeKey`) already derives known keys from the struct types. `jsonshape.Keys` treats any `json.Marshaler` (`Entry`, `workflow.Outputs`) as a leaf, so nested keys of those are not policed -- correct.
- Doc home: README "Agents and actors" section (`README.md:~1889-1990`), "Actor entries" bullet list (`agent`, `candidates`, `tier`, `placement`, `check`; `scope` is described earlier ~1849). There is no `outputs` bullet anywhere in README.

## Behaviour

1. **Validation hint.** In rule 3, when a `run` step's `on` key names an outcome (`name=value`, `name>0`) the step's actor does not declare, the problem line becomes
   `actor "<actor>" declares no outcome "<name>"; declare it with: relevo config set actors.<actor>.outputs '<json>'`
   (still rule 3, same step prefix, so `step review: rule 3: actor ...`). It is emitted **once per step per outcome name**, not once per match (so `verdict=pass` and `verdict=changes` give one line). `<actor>` is the step's rendered actor name (`RenderParams(def, step.Run)`).
   - `<json>` is derived, not hard-coded: the first shipped agent (sorted by name) whose declared outputs contain `<name>` with a kind that fits the match (one-of containing the value; count for `>0`/`=0`) supplies its whole `Outputs`, marshalled with `Outputs.MarshalJSON` (wire form, keys sorted: `{"findings":"artifact","verdict":{"one-of":["pass","changes"]}}`). `verdict` -> reviewer; `findings>0` -> security-reviewer.
   - If no shipped agent fits, the line is `actor "<actor>" declares no outcome "<name>"; declare it in actors.<actor>.outputs` (no JSON).
   - Applies whether the actor declares nothing or declares other outcomes but not this one. Other rule-3 lines (`uncovered: ...`, `not a declared value`, artifact branching, bare keys) are unchanged.
2. **Shipped contract lives in `workflow`.** To let the validator derive the suggestion without a new `Env` field (five construction sites), move the shipped table from `roles` to `workflow`: `workflow.ShippedOutputs(agent) (Outputs, bool)` returning a fresh map each call (build the literal inside the function, so no copy helper is needed) and `workflow.SuggestOutputs(name, value string, count bool) (Outputs, bool)`. `roles.EffectiveOutputs` keeps its signature and calls `workflow.ShippedOutputs`. Contents of the table are unchanged.
3. **`config set` refuses unknown fields on `agents.<n>` / `actors.<n>`, on write only.**
   - New pure function in `internal/roles`: `UnknownKeys(section string, body []byte) []string` -> sorted paths like `morphic-reviewer.outcomes` (entry-level keys, plus keys inside `native.<kind>` rows and `scope`), known keys derived from `reflect.TypeOf(AgentEntry{})` / `Actor{}` via `jsonshape.Keys` + the same prefix rule as `hasShapeKey`. Returns nil for any section other than `agents`/`actors`. Plus `KnownFields(section string) []string` (entry-level names, sorted) for the message.
   - `configSetRun` computes the stored body and the updated body (whole-section replace and key-path forms, and `--local`), and refuses only keys present in the new body's `UnknownKeys` and **absent from the old body's** -- so an already-stored stray key never blocks an unrelated `config set`, and nothing is refused on load, import, edit or sync.
   - Refusal: code `codeConfigInvalid` (`fail(...)`), nothing written. Message names the path, the section's known fields, and for `agents` adds that outcomes are declared on the actor: e.g. `agents.morphic-reviewer.outcomes: unknown field (known: native, shape, source); outcomes are declared on the actor: relevo config set actors.<actor>.outputs '{...}'`.
   - Helper lives in a new `cmd/relevo/config_unknown.go` (config_edit.go is 507 lines; stay under 600 and keep `configSetRun` under 70 lines).
4. **README** gets an `outputs` bullet in "Actor entries" and one short paragraph with the reviewer example (below). No new help-registry row (no new verb or flag).

## Seams (files and declared scope)

Code:
- `internal/workflow/outputs_shipped.go` (new): `ShippedOutputs`, `SuggestOutputs`.
- `internal/workflow/validate_match.go`: `checkRunMatches` passes the rendered actor name; `checkOutcomeMatch` (`!ok` case) and the `>0` case in `checkRunMatch` (`!ok` only) emit the hint once per step+name. Dedupe state is a small set on `validator` (`validate.go` struct) or a local passed down -- builder's choice.
- `internal/roles/outputs_default.go`: drop `shippedAgentOutputs`; `EffectiveOutputs` calls `workflow.ShippedOutputs`; keep `copyOutputs` (used by `registry.go`).
- `internal/roles/unknown_keys.go` (new): `UnknownKeys`, `KnownFields`.
- `cmd/relevo/config_unknown.go` (new): the old/new diff and the refusal message.
- `cmd/relevo/config_edit.go`: call the refusal in both branches of `configSetRun` before `writeConfigSection` (next to `guardCandidatesDrop`).

Docs: `README.md` (Actor entries + paragraph), `docs/plans/2026-10-17-custom-agent-outputs.md` (this plan, verbatim).

Tests (new unless noted):
- `internal/workflow/validate_hint_test.go`: pins the hint (below).
- `internal/roles/unknown_keys_test.go`: pins `UnknownKeys`/`KnownFields`; and that `ParseAgents`/`ParseActors` still accept the stray keys.
- `internal/roles/actor_outputs_custom_test.go` (or extend `actor_info_test.go`): custom-agent actor with declared outputs through `FromActors`/registry -> `WorkflowActors()` -> `workflow.Validate(workflow.Default(), env)` with `Given{Plans:true}` and the other default actors supplied -> zero problems; the same without `outputs` -> the hint line for the review step.
- `cmd/relevo/config_unknown_test.go`: `config set` refusal through `run(args)` with `docsEnv(t)` as in `config_guard_test.go`. `config set` neither spawns a harness nor reaches the network, so this stays legal for CI; do not add a test that runs `chain`/`bind`/`send`.

Declared scope: nothing outside the files above. No change to `agents`/`actors` schema, no `outcomes` field on agents, no change to the validators' other messages, `config import/edit`, serve or sync code. `internal/serve/chains_test.go:1082` accepts "undeclared" or "rule 3" and keeps passing.

## Ordered steps

1. **Confirm the premise in a sandbox.** `scripts/relevo-sandbox.sh create 1254` (see `docs/env-sandbox.md`; `export HOME=$SB/home` first so nothing touches the real home), build the branch binary into it, run the issue's two `config set` commands plus the `chain ... --dry-run` (expect the six problems), then `config set actors.morphic-reviewer.outputs '{"verdict":{"one-of":["pass","changes"]},"findings":"artifact"}'` and the dry-run again. Done when the second dry-run prints no problems; state both results in the report. Halt if it still fails.
2. **Move the shipped table.** Add `outputs_shipped.go`, rewire `outputs_default.go`. Done when `go test ./internal/roles ./internal/workflow` is unchanged-green.
3. **Hint in rule 3.** Edit `validate_match.go`; write `validate_hint_test.go`. Done when the focused test passes and the old tests in `internal/workflow` still pass.
4. **Unknown-key function.** `internal/roles/unknown_keys.go` + test. Done when the test passes.
5. **`config set` refusal.** `config_unknown.go`, wire into `configSetRun`, write the cmd test and the roles/registry test from the list. Done when `go test ./cmd/relevo -run 'ConfigSet'` and the roles test pass.
6. **README.** Add the bullet and paragraph. Done when the example JSON in it equals what `SuggestOutputs("verdict", ...)` marshals (aside from key order) and `make check` comment/format scripts pass.
7. **Mutation checks** (record in the report): (a) emit the hint per match instead of per name -> the one-line test fails; (b) hard-code no suggestion / break `SuggestOutputs` for `verdict` -> the JSON test fails; (c) make the refusal ignore the old body -> the pre-seeded-stray-key test fails; (d) make `config set` skip the check on the key-path branch -> the `agents.X.outcomes` key-path test fails.
8. **Full check:** `make check` once. Do not lower or add lint/comment/filesize exclusions. Moving the table between packages shifts coverage; CI's job compares against `testdata/coverage-baseline.txt`: if `RELEVO_REQUIRE_COVERAGE=1 make check` shows a drop beyond one point, regenerate with `sh scripts/check-coverage.sh --write` and say so in the report; never lower it by hand.
9. **Save and commit.** Copy this plan verbatim to `docs/plans/2026-10-17-custom-agent-outputs.md` and commit it **with the code** as new commits (never amend or rebase anything already pushed). Commit message ends with the attribution line from the round prompt.

Focused loop between steps: `go test ./internal/workflow ./internal/roles ./cmd/relevo -run 'Hint|UnknownKeys|ConfigSet|CustomOutputs'` (fix every reported error before rerunning). Full: `make check`.

## Test pins (what, not how)

- Workflow: Default workflow, reviewer actor present with no outputs -> exactly one `step review: rule 3:` hint line and one for `fix-review`, each naming the actor, `verdict`, and the `relevo config set actors.<actor>.outputs '{...}'` command whose JSON parses back (`workflow.ParseOutputs`) to the reviewer contract; an actor declaring only `findings` still gets the hint for `verdict`; an unknown outcome no shipped agent has gets the no-JSON form; no old `names undeclared outcome` line remains for the covered cases.
- Roles/registry: custom-agent actor + declared `verdict`/`findings` -> default workflow validates clean; stray `outcomes`/`role` on agents and stray keys on actors still load.
- Config set: `config set agents.x.outcomes '["verdict"]'` and `config set agents.x '{...,"role":"reviewer"}'` and `config set actors.x.role '"reviewer"'` are refused with `config_invalid` naming the known fields, and the stored section is byte-identical afterwards; a valid `config set actors.x.outputs ...` succeeds; with a stray key already stored (seed it via the store/`config import`, which is not refused), an unrelated `config set` on another field succeeds.

## README text to add (substance)

In "Actor entries": `outputs` -- the outcomes the actor's rounds report, `{"<name>": "artifact" | "count" | {"one-of": [...]}}`. A shipped agent has them built in; an actor over a custom agent declares none unless you set them, and a chain step that branches on an outcome the actor does not declare is refused at `chain --dry-run`.

Paragraph: a custom agent can stand in for `reviewer` in a chain by declaring the reviewer contract on its actor -- `relevo config set actors.morphic-reviewer.outputs '{"verdict":{"one-of":["pass","changes"]},"findings":"artifact"}'` -- and by ending its output with a fenced `relevo` block holding `verdict: pass` or `verdict: changes`, which relevo's round prompt already tells it to write; then `--reviewer-actor morphic-reviewer`. Outcomes are declared on actors, never on agents; `outcomes` or `role` on an agent is refused.

## Report must include

- Step 1 result: the dry-run output before and after setting `outputs`.
- `git diff --stat` against the declared scope above (flag any file outside it).
- The exact new problem line for the issue's scenario, and the refusal message for `agents.X.outcomes`.
- Mutation results (a)-(d), each with the failing test's name.
- `make check` result; whether coverage was regenerated.
- Anything where the code contradicted this plan (halt instead of improvising).
