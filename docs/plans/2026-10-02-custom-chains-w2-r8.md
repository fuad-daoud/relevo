> MasterMind decisions (binding for every round):
> - All preamble defaults and Q1-Q7 answers are ACCEPTED, except preamble item 5.
> - Preamble item 5: the MasterMind chose the ALTERNATIVE. A placed (remote) writer's `check` step is answered from the gate record its served round already pulls back, with no server change. Slice 2's placed chain with a gate keeps working, and `TestChainRemoteBuilderE2E` keeps its gate and repair assertions (now a repair STEP). Concretely:
>   - the placed writer's served binding keeps `Gate = <the check command>` and `Regate = 0` (repair is the workflow's `repair` step);
>   - a `run_check` whose writer is placed starts no local run: it awaits the writer's newest closed round and feeds `check_closed` from that round's pulled gate record (green/red, log = the pulled gate log);
>   - a placed writer whose workflow has two different non-empty check commands is refused until W4, naming the server feature `check`; one distinct command is supported.
>   This replaces the refusal in Round 7 (`TestPlacedWriterWithCheckRefusedNamingFeature` becomes `TestPlacedWriterCheckAnsweredFromPulledGate` plus `TestPlacedWriterTwoCheckCommandsRefused`) and Round 8's move of the slice-2 e2e to NoGate (do not move it).
> Your round is the section after the preamble. Save the preamble plus your round's section to the docs/plans file your round names.

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

## Round 8: default chains move onto the new engine; existing rows convert; server chains map their settings

**Behaviour**
- Every new local chain, default workflow included, starts on the new engine with `<chain>-<actor>` members.
- `ConvertLegacyChains` runs once at daemon start, at both call sites (`cmd/relevo/wire.go:221`, `cmd/relevo/serve.go:335`), in one transaction under the lock. For every row with an empty `StateJSON`:
  - `FromLegacy` gives the definition and state; the builder actor comes from its binding.
  - `chain_member` rows are written from the four columns in builder, reviewer, planner, security order. A shared actor maps both bindings, and sends go to the first.
  - A running row keeps its awaited round, so it carries on without noticing.
  - `Gate`/`Regate` are cleared on its members; the next check is a step.
  - Old trace rows are kept.
- The old path in `chainApply` is no longer reached; Round 9 deletes it.
- **Server.**
  - `ServedChainPreflight` and `ServedChainCreate` (`internal/relevo/chain_served.go:101,142`) map `remote.ChainSettings` onto default params and create a new-engine chain.
  - `resumeOptionsFromWire` (`internal/serve/chains.go:~487-514`) maps onto `--param` equivalents.
  - `ServedChainView` (`chain_served.go:328`) fills the old wire fields from `LegacyView`.
  - The client's pull (`chainRowFromView`, `chain_pull.go:502-516`) sets `StateJSON`/`WorkflowJSON` through `FromLegacy`.
  - The client still sends `Settings`; nothing in `remote/chain.go` changes shape.
- `chainSendPending` (`chain_send_remote.go:84-138`) resolves the awaited binding from the state and `chain_member`, not from `AwaitingMember`.
- `refuseRunningChainMember` and the status, wait and stop surfaces read the state. The legacy columns are no longer read, but `status` and `reason` keep being written because they are general.
- **The placed-check refusal now covers default chains (preamble 5).** `TestChainRemoteBuilderE2E` moves to `NoGate: true`, unless the MasterMind chose the alternative before this round.
- **E2E vocabulary.**
  - `TestChainE2E`, `TestChainRemoteBuilderE2E` and `TestChainServerE2E` are updated to the new member names, step-id trace rows, `workflow.Event`/`Action` kinds and the state fields.
  - Every behavioural pin stays: the send sequence per member, the seed contents, the single delivery, the stream-read verdict, the plan advance and the correction reset.
  - The server e2e's served reader labels (`finishServedReader`, `chain_server_test.go:488-512`) follow the declared artifacts.
- The equivalence test stays green (it still drives the old `Next` in `internal/chain`).

**Steps**
1. Add `ConvertLegacyChains` and call it at both sites:
   - `TestConvertLegacyChainsEveryStepThenContinues`: a row at each old step converts, then the next close advances it as the default workflow would.
   - `TestConvertLegacyRunningKeepsAwaitedRound`
   - `TestConvertLegacyIsIdempotent`
   - `TestConvertLegacySharedActorMapsBothBindings`
2. Switch new local default chains to the new start, and remove the `c.WorkflowJSON == ""` old-path dispatch from every caller except the code Round 9 deletes. Tests: `TestDefaultChainStartsOnWorkflowEngine`, `TestDefaultChainMemberNamesPerActor`.
3. Map the server settings and fill the legacy wire view: `TestServedChainCreateMapsSettingsToDefaultParams`, `TestServedChainViewFillsLegacyFields`, `TestChainPullSetsStateFromView`.
4. Move the pending send to the state: `TestChainSendPendingResolvesAwaitedFromState`.
5. Rewrite the e2e vocabulary, keep the pins, and apply preamble 5. All of `make e2e` passes.
6. Update every `internal/relevo` chain test that asserts the old vocabulary. Any test whose *behaviour* assertion would have to change is a halt, not an edit.
7. Save this section as `docs/plans/2026-10-02-custom-chains-w2-r8.md` and commit.

**Deleted (closed list)**
1. Member names `<chain>-rev`, `<chain>-plan` and `<chain>-sec` for new chains; converted rows keep theirs.
2. A chain builder member's own gate and repair round (`chainApply` red-gate intercept, `chain.go:255-281`). Repair is now the `repair` step.
3. With a gate set, a chain whose builder is placed remotely: refused until W4 (preamble 5, unless the alternative is chosen).
4. The old trace vocabulary for new rows: `builder_closed`, `reviewer_closed` and the rest.
5. `relevo chain --json`'s `phase` and member `part` fields, replaced by `step` and member `actor`, and the start and resume text lines that print phase and plan (`cmd/relevo/chain.go:345-390`).
6. Reads of `chains.phase`, `step`, `plan`, `plans`, `corrections`, `awaiting_member` and `awaiting_round`. The columns stay in the schema, unread.

**Commands**
- Focused: `go test ./internal/relevo/ ./internal/serve/ ./cmd/relevo/ -run 'Convert|DefaultChain|Served|ChainPull|SendPending|Chain' -count=1`, then `go test ./internal/e2e/ -run 'TestChainE2E|TestChainRemoteBuilderE2E|TestChainServerE2E|TestChainTriageE2E' -count=1`
- Full: `make check`, then `make e2e`

**Mutations for the MasterMind**
- In `ConvertLegacyChains`, set `Awaiting.Round` to 0. `TestConvertLegacyRunningKeepsAwaitedRound` must fail.
- Map `--max-corrections` onto `regate` in the served settings mapping. `TestServedChainCreateMapsSettingsToDefaultParams` must fail.

**Halt if**
- any e2e behavioural pin, not just its vocabulary, would have to change;
- `chainRequestMatches` (`internal/serve/chains_view.go:68-87`) needs a type change;
- a converted running row would re-send a round it already sent.

**Report includes**
- every changed e2e assertion, as old → new;
- every halt-reason rewording a user can now see, matching the equivalence test's `reworded` list;
- the preamble 5 choice taken;
- both command outputs.

---

## What each round's report must include

Every round's report includes:
- the focused and full command outputs (`make check`, `make e2e`);
- `git diff --stat`, compared to the round's declared seams;
- every new test, by name;
- the mutation it ran itself, if any;
- every deviation from this plan, with its reason;
- any coverage-baseline line it changed.

A round that hits a halt condition stops and reports. It does not bend a test.
