# Plan: custom chains W2 (wire the workflow engine in, delete the old `Next`)

## Preamble

### Where the seed and the code disagree (each with the default this plan follows)

1. **`chain_event` already has a `step` column.** Migration 016 created it to hold the old step word.
   - Default: add no column. New rows write the workflow step id into the existing `step` and `''` into `phase`.
   - Old and new rows are told apart by their event encoding: a `workflow.DecodeEvent` success means a new row.
   - Old rows keep today's rendering, which is better than "show no step".
2. **There are no shipped actors.** relevo ships *agents* (`internal/roles/actors_shipped.go:18-24`). `builder`, `reviewer` and `researcher` are built-in roles (`internal/harness/harness.go:35-54`). `planner` and `lite-planner` are starter actors that `config init` seeds, both on agent `architect` (`internal/setup/setup.go:42-46,125-133`). `security` is only a policy default name (`internal/policy/policy.go:236-241`).
   - Default: an actor that declares no `outputs` inherits its **shipped agent's** default declaration:

     | Agent | Default outputs |
     |---|---|
     | `reviewer` | `verdict` one-of [pass, changes], `findings` artifact |
     | `security-reviewer` | `findings` count, `report` artifact |
     | `architect` | `plan` artifact |
     | `plan-executor` | none |
     | `researcher` | none |

   - With this default, the user's `lite-planner` and `security` actors work with no config migration.
   - An actor on a user agent that declares nothing fails validation at chain start, with the rule and the step named.
3. **What a reader's artifact is.** Today a reader writes no file: relevo saves its block-carrying final message under the agent's output label (`ActorOutput`, `internal/relevo/actoroutput.go:14`).
   - Default: a reader declares **at most one** artifact, and that artifact *is* its saved final message, whatever its key (`security` declares `report`, and its file keeps the label `findings`).
   - A reader's footer renders only its outcomes. `workflow.Footer`'s "Write x to x.md" lines are used only for a writer.
   - In W2 a writer may declare outcomes only. A writer-declared artifact is refused when the actor is validated.
4. **"Lint PATH" does not exist.** The gate runner has no PATH handling. A check uses the gate's scope (`scopeGate`), its timeout (`gateTimeoutFor`/policy) and its restart-loss re-run. Nothing PATH-related is invented.
5. **`TestChainRemoteBuilderE2E` (slice 2) runs a remote builder with `Gate: "make check"` and a repair round** (`internal/e2e/chain_remote_test.go:115-175`).
   - The seed's rule ("refuse to start a placed chain whose workflow has a check step") makes that chain unstartable from Round 8 on.
   - Default, following the seed: the refusal applies only when a check step's command renders **non-empty**, so `--no-gate` still runs placed. In Round 8 the slice-2 e2e moves to `NoGate: true`, and its gate and repair assertions are listed as deleted behaviour, to come back in W4.
   - **Alternative for the MasterMind to choose before Round 8:** answer a placed writer's check from the gate record its served round already pulls back. That keeps slice 2 whole with no server change. If you want it, amend Round 8 step 6; nothing earlier changes.
6. **"The e2e suites pass unchanged" cannot hold literally.**
   - They assert the old member names (`-rev`, `-plan`, `-sec`), the old event kinds (`chain.EventBuilderClosed`…), and `row.Phase`/`row.Plan`/`row.Corrections`.
   - They point the planner at `researcher` and security at `reviewer`, neither of which declares the outputs the default workflow needs.
   - Default: Round 8 rewrites their *vocabulary* while keeping every behavioural pin: the send sequence, the seed contents, the single delivery, the recap/stream fallback and the plan advance. The report lists each changed assertion.
7. **Migrations are SQL-only** (`internal/db/migrate.go:16-17`), so existing rows cannot be converted inside migration 020.
   - Default: 020 is purely additive.
   - The row conversion is a Go one-time step, `ConvertLegacyChains`, run where `MigrateToActors` runs: `cmd/relevo/wire.go:221` and `cmd/relevo/serve.go:335`.
   - The same pure converter turns a pulled server mirror into a state.
8. **Membership needs a table.** Today membership is four indexed columns (`builder`/`reviewer`/`planner`/`security`). Default: migration 020 adds `chain_member` and `chain_check` tables.
   - `migrations/README.md` forbids a cascading foreign key, so neither table has one; `ChainDelete` deletes their rows in code.
9. **Migration number.** HEAD is `origin/main`, whose highest file is `019_chain_event_plan.sql`, so the new one is `020_custom_chains.sql`. The builder re-checks `git ls-tree origin/main internal/db/migrations/` at the start of Round 3 and halts if 020 is taken.

### Open questions and the defaults this plan uses

- **Q1. Which actor's member keeps the name `<chain>`?** The workflow's only writer actor. With no writer, every member is `<chain>-<actor>`, and the name `<chain>` is still reserved. With two or more writers, the one named by param `builder` (when the workflow has that param) keeps it; otherwise the first writer in sorted actor order.
- **Q2. Which actors get members?** Members are the actors of the `run` steps reachable from `start` once `when` steps are resolved on the start params. With `scan=false`, no security member exists, as today. A resume that turns a branch on (for example `--param scan=true`) creates the missing members, generalising `chainCreateSecurityMember`.
- **Q3. Chain-name cap.** `store.MaxAgentNameLen − 1 − len(longest member actor)`. With `lite-planner` that is 19, down from 27. This is a visible change.
- **Q4. `--server` until W4.**
  - The client keeps sending `remote.ChainSettings`, unchanged, so `chainRequestMatches`' `!=` comparison still compiles.
  - It refuses `--workflow`, `--param`, `--task`/`--task-file` and `--from` together with `--server`, as a usage error naming the server feature `workflow` that W4 adds.
  - The server (`ServedChainCreate`/`ServedChainPreflight`) maps `Settings` onto the default workflow's params and, from Round 8, runs the new engine.
  - `ServedChainView` fills the old wire fields (phase, step, plan, plans, corrections) from the state through `workflow.LegacyView`.
  - The client's pull converts those fields back into a state with `workflow.FromLegacy`, so an old-binary server keeps working too.
  - Known gap, left to W4: a pre-W2 client reading a W2 server's trace cannot decode workflow events.
- **Q5. `--force` versus `--replace`.** `config workflow add` takes both: `--replace` overwrites a saved name, and `--force` allows a saved name to shadow a shipped one (`default`), as actors do.
- **Q6. Coverage.** W2 adds no new package.
  - A round that moves code between packages runs `sh scripts/check-coverage.sh --write`, then reverts every baseline line of a package it did not touch, and lists each changed line in its report.
  - No baseline is lowered to get green: a touched package that drops more than one point gets tests instead.
  - `internal/chain` has no baseline line, and none is added.
- **Q7. The `task` input.** It is stored as a chain input file, `rt.Store.ChainTaskPath(chain)` beside the plan copies. `{{task}}` renders it inline.

### Constraints every round obeys (also restated in each round's own section)

- **Commands:** `make check` and `make e2e` are both green at each round's end, with the focused command named in the round first. `make e2e`'s `-run` filter (Makefile:69) must list any new e2e.
- **Size:** files ≤ 600 lines, functions ≤ 70 lines. `internal/relevo/chain.go` has 4 lines to spare, `chain_start.go` 36 and `chain_resume.go` 42, so new code goes in new files.
- **Comments:** they say why. No issue numbers, `§`, "W2", "round N" or "used to" in code or tests. `scripts/check-comments.sh` enforces this, and no chain or workflow file is on its allow list.
- **Exclusions:** a new lint, comment or filesize exclusion is never added to get green.
- **CLI tests:** a `cmd/relevo` test never runs a subcommand that spawns a harness or reaches the network. Rules are tested as pure functions in `internal/relevo`.
- **Temp dirs only:** config and state tests use `t.TempDir()` XDG roots, as `TestMain` already arranges.
- **History:** each round commits as **new commits**, never amending or rebasing a pushed commit. Its last step saves the round's section to `docs/plans/2026-10-02-custom-chains-w2-rN.md` and commits it with the code.

---

## Round 1: actors declare outputs, and the footer and close parse come from the declaration

**Behaviour**
- The `actors` section gains `outputs` per actor. An undeclared actor inherits its shipped agent's default (preamble 2).
- A chain reader's prompt footer is `workflow.Footer` of its outcomes.
- The close parses the declared outcomes with `workflow.ParseOutcomes` (final body first, then the stream's assistant texts, newest first) and artifacts with `MissingArtifact`.
- The old engine still runs; a small adapter fills today's `chain.Event` from the parsed values.
- A local chain start and resume validate the default workflow, with the chain's params, through `workflow.Validate` against the registry, and refuse on any problem.

**Seams**
- `internal/workflow/outputs.go`: `Outputs` gains `MarshalJSON`/`UnmarshalJSON`, in the wire form `{"verdict":{"one-of":[...]},"plan":"artifact","findings":"count"}`. Decoding goes through `decodeOutputs`.
- `internal/roles/actors.go:48-60`: `Actor` gains `Outputs workflow.Outputs \`json:"outputs,omitempty"\``. `validateActor` (191-230) checks each key with `workflow.ValidName`.
- `internal/roles/actors_convert.go` `rowFor` (39-78): applies the shape rules (a reader has ≤ 1 artifact; a writer has no artifact) and carries the outputs.
- `internal/roles/file.go` `Row` (48-83) gains `Outputs`.
- `internal/roles/registry.go` `Role` (54-84) gains `Outputs`. Add `(*Registry) ActorInfo(name) (workflow.ActorInfo, bool)` and `(*Registry) WorkflowActors() map[string]workflow.ActorInfo`. Shape maps from `harness.ShapeBuilder` to writer and from consult to reader.
- New `internal/roles/outputs_default.go`: the agent default table, and `EffectiveOutputs`.
- `internal/relevo/chain_send.go:161-178` `chainMemberBlock`: replaced by a footer generated from the member's actor (via `BindingRole(b)` and the registry).
- `internal/relevo/send.go:902-914` (`roundPrompt`, `chainReaderPromptFor`): unchanged except for the block source.
- `internal/relevo/chain_seed.go:94-129` (`chainReaderVerdict`, `chainReaderFindings`): replaced by one `chainParseOutcomes(rt, b, body, outputs) (map[string]string, string)`, which tries the body first and then the stream newest first, through `transcript.Texts`.
- `internal/relevo/chain.go:179-190` (`chainEventFromClose`): reads the reviewer's `verdict` and the security member's `findings` from that map, and the planner's presence through `MissingArtifact` with the report file size. Keep it within the file's 600 lines: move helpers to a new `chain_outputs.go`.
- New `internal/relevo/chain_validate.go`: `chainValidateDefault(rt, set chain.Settings, given workflow.Given) error`, called from `chainResolveStart` (`chain_start.go:150-210`) and from `ChainResume` before the lock.
- `internal/chain/verdict.go`: `ParseVerdict` and `ParseFindings` are deleted, with their test file.

**Steps**
1. Add the `Outputs` JSON round-trip in `internal/workflow`. Test: `TestOutputsJSONRoundTrip` (each kind, plus an invalid kind refused).
2. Add the actor field, its validation, the agent defaults and the registry accessors. Tests: `TestActorOutputsParseAndValidate`, `TestReaderDeclaresAtMostOneArtifact`, `TestWriterArtifactRefused`, `TestUndeclaredActorInheritsAgentDefault`, `TestRegistryActorInfoShape`. Check that `relevo config export`/`import` round-trip an actor with outputs: `TestExportImportCarriesActorOutputs` in `internal/config`.
3. Replace `chainMemberBlock` with the generated footer. Test: `TestChainReaderFooterFromDeclaration`, which checks the reviewer footer is `verdict: pass   # or: changes` and the security footer is `findings: 0   # a count`.
4. Replace the verdict and findings parse with `chainParseOutcomes`. Tests: `TestChainParseOutcomesBodyThenStream`, `TestChainParseOutcomesRecapAfterBlock`, `TestChainParseOutcomesMissingAndOutOfRange`. The existing reviewer/security chain tests in `internal/relevo/chain_test.go` must stay green unchanged.
5. Validate the default workflow at local start and resume.
   - Test: `TestChainStartRefusesActorWithoutDeclaredOutputs`, a pure test in `internal/relevo`.
   - Update `TestChainE2E`'s actor choices (`internal/e2e/chain_test.go:113-115`): PlannerActor becomes an actor on agent `architect`, and SecurityActor an actor on `security-reviewer`. Define them in `writeChainCandidatesAndPolicy` (426+) if they are absent. Member names stay `-rev`/`-plan`/`-sec` in this round.
6. Delete `chain.ParseVerdict` and `chain.ParseFindings`, and their tests in `internal/chain/verdict_test.go` (delete the file).
7. Save this section as `docs/plans/2026-10-02-custom-chains-w2-r1.md` and commit everything as new commits.

**Deleted (closed list)**
1. The hard-coded footers `"verdict: pass        # or: changes"` and `"findings: 0"` (`chain_send.go:173-176`).
2. `chain.ParseVerdict` and `chain.ParseFindings` (`internal/chain/verdict.go`), with `verdict_test.go`.
3. `chainReaderVerdict` and `chainReaderFindings` (`chain_seed.go:94-129`).
4. Starting or resuming a chain whose planner or security actor declares no matching outputs: now refused.

**Commands**
- Focused: `go test ./internal/workflow/ ./internal/roles/ ./internal/config/ ./internal/relevo/ -run 'Output|Actor|Footer|ParseOutcomes|ChainStart|ChainResume' -count=1`
- Full: `make check`, then `make e2e`

**Mutation for the MasterMind:** in `chainParseOutcomes`, drop the stream fallback (use the body only). `TestChainParseOutcomesRecapAfterBlock` and `TestChainE2E` must fail.

**Halt if**
- the registry cannot reach the actor's outputs without a config-schema change beyond the `actors` section;
- any existing `internal/relevo` chain test needs its *assertions* changed, rather than its fixtures, to pass.

**Report includes**
- the agent-default table as built;
- every e2e fixture change;
- confirmation that `config export` carries `outputs`;
- both command outputs.
