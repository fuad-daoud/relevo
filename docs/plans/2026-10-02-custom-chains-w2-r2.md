# Plan: custom chains W2 (wire the workflow engine in, delete the old `Next`)

Base is `78bd97fd`, which is `origin/main` with W1 merged. I read all of spec sections 1–11, all of `internal/workflow`, `internal/relevo/chain.go`, `chain_seed.go`, `gate.go`, `cmd/relevo/chain.go`, `internal/roles/actors*.go`, the migrations and the chain e2e. The rest of the chain driver, the config package and the server side I took from read-only surveys with exact line ranges. Line numbers below are at `78bd97fd`.

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

## Round 2: the `workflows` config section and its verbs

**Behaviour**
- `relevo.db` gains a `workflows` section, a `config_doc` row, so no migration is needed. Its body is `{"<name>": {"source": "<text as written>", "definition": <parsed JSON>}}`.
- The verbs:
  - `relevo config workflow add <file> [--replace] [--force]` validates fully against the current actors (`Given` nil) and stores the source plus the definition. `file:` seeds are read relative to the file and embedded into the stored definition. The source text is kept as written.
  - `rm <name>` removes a saved workflow.
  - `show <name> [--json]` prints the stored source; `--json` prints the definition.
  - `edit <name>` runs the spec 7.1 loop: open the source in `$EDITOR`, validate on save, and on failure reopen with `# <problem>` comment lines at the top. It ends when the workflow is valid, or when the file is unchanged or empty (no-op).
- `relevo config` lists workflows as `default  shipped` and `<name>  saved`.
- `export`/`import` carry the section.
- The section validator checks only parse, name, key match and that the source parses to the stored definition. It does not check actors, so removing an actor is never blocked by a saved workflow.
- A resolver `ResolveWorkflow(nameOrPath)` tries, in order: an existing file path (YAML or JSON, with `file:` seeds embedded), then a saved name, then a shipped name (`default`).

**Seams**
- `internal/config/config.go:23-39`: `Workflows Section = "workflows"`, appended to `Sections`, plus a `Validate` case (363-398) and a `loadWorkflows` called from `decodeDoc` (`document.go:13-64`). `config.go` is at 589 lines, so the loader and validator go in a new `internal/config/workflows.go`. `Loaded` gains `Workflows map[string]StoredWorkflow`.
- `cmd/relevo/config.go:39-77`: the dispatcher gains `workflow`. A new `cmd/relevo/config_workflow.go` follows the noun-verb shape of `config_server.go:23` and reuses `runEditor` (`config_edit.go:317-339`).
- Add rows to `cmd/relevo/registry_rows.go` and installers to `verbFlagSets` (the parity test is `TestRegistryFlagsMatchFlagSets`).
- `configShow` (`cmd/relevo/config.go:96`) adds the list.
- New `internal/relevo/workflow_resolve.go`: `ResolveWorkflow(rt, nameOrPath string) (workflow.Definition, string /*origin: file|saved|shipped*/, error)`, plus `EmbedFileSeeds(def, dir) (Definition, error)`, which reads the files. `internal/workflow` stays I/O-free.
- New `internal/relevo/workflow_edit.go`: the pure edit-loop core, `workflowEditRound(old, edited []byte, actors) (stored, problems, done)`, so the CLI keeps only the `$EDITOR` I/O.

**Steps**
1. Add the section with its validation, loader and export/import. Tests: `TestWorkflowsSectionValidate` (bad name, key mismatch, source/definition disagreement), `TestExportImportCarriesWorkflows`.
2. Add the resolver and the `file:` seed embedding. Tests: `TestResolveWorkflowOrder` (a file beats a saved name beats shipped), `TestEmbedFileSeedsRelative`, `TestEmbedFileSeedsRefusesEscape`.
3. Add the `add`/`rm`/`show` verbs. Tests, under temp XDG: `TestConfigWorkflowAddRefusesExistingWithoutReplace`, `TestConfigWorkflowAddRefusesShippedNameWithoutForce`, `TestConfigWorkflowShowKeepsComments`, `TestConfigWorkflowShowJSON`.
4. Add `edit` through the pure loop core. Tests: `TestWorkflowEditRoundPrefixesProblems`, `TestWorkflowEditRoundUnchangedIsNoop`. The CLI test sets `EDITOR` to a script under `t.TempDir()`; it spawns no harness.
5. List workflows in `relevo config`. Test: `TestConfigShowListsWorkflows`.
6. Save this section as `docs/plans/2026-10-02-custom-chains-w2-r2.md` and commit.

**Deleted:** nothing.

**Commands**
- Focused: `go test ./internal/config/ ./internal/relevo/ ./cmd/relevo/ -run 'Workflow|ConfigShow|Export|Registry' -count=1`
- Full: `make check`, then `make e2e`

**Mutation for the MasterMind:** let `add` overwrite an existing name without `--replace`. `TestConfigWorkflowAddRefusesExistingWithoutReplace` must fail.

**Halt if** the `config_doc` revision machinery (`revision.go:71-135`) cannot record a section it does not know at the baseline. Do not special-case it.

**Report includes** a `relevo config workflow show` sample, the registry rows added, and both command outputs.
