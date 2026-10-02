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

## Round 3: the schema, the store, and the pure legacy converter

**Behaviour.** Nothing the user sees changes. This round adds:
- the columns and tables;
- a store API for members and checks;
- `workflow.FromLegacy` (legacy columns to default definition plus state, implementing spec 11 item 9);
- `workflow.LegacyView` (state back to the old phase/step/plan/plans/corrections, for wire compatibility).

**Seams**
- **`internal/db/migrations/020_custom_chains.sql`.** First confirm 020 is free on `origin/main`. Its contents, with a header citing `README.md` as 019 does:
  - `ALTER TABLE chains ADD COLUMN workflow TEXT NOT NULL DEFAULT ''`, likewise `state`, and `parent`.
  - `CREATE TABLE IF NOT EXISTS chain_member(chain_id TEXT NOT NULL, binding TEXT NOT NULL, actor TEXT NOT NULL, seq INTEGER NOT NULL, PRIMARY KEY (chain_id, binding))`, plus an index on `binding`.
  - `CREATE TABLE IF NOT EXISTS chain_check(chain_id TEXT NOT NULL, run INTEGER NOT NULL, step TEXT NOT NULL, visit INTEGER NOT NULL, command TEXT NOT NULL, pid INTEGER NOT NULL DEFAULT 0, started_at INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL DEFAULT 0, result TEXT NOT NULL DEFAULT '', exit_code INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0, log TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, PRIMARY KEY (chain_id, run))`.
  - No foreign keys.
  - Update `internal/db/migrations/SCOPES.md` for both tables.
- **`internal/db/chain.go`.**
  - `ChainRow` (19-52) gains `WorkflowJSON`, `StateJSON` and `Parent`; update `scanChain`, `insertChain` and `updateChain`, gated on the applied version as `hasChains` is (96).
  - New `internal/db/chain_member.go`: `ChainMemberRow`, `(*Tx) ChainMembersPut`, `(*DB) ChainMembers(id)`.
  - New `internal/db/chain_check.go`: `ChainCheckRow` with put/get/next-run.
  - `ChainGetByMember` (146) looks in `chain_member` first, then the four legacy columns.
  - `ChainDelete` (284) deletes the member and check rows.
- **`internal/store/chain.go`.** The `Tx`/`Store` twins of the above. `CreateChain` (91-109) takes the member rows and writes them in the same transaction.
- **New `internal/workflow/legacy.go`** (pure):
  - `type Legacy struct{ Status, Reason, Phase, Step string; Plan, Plans, Corrections, AwaitingRound int; PlanPaths []string; Settings LegacySettings; Builder string }`, where `LegacySettings` mirrors `chain.Settings`' JSON so `internal/workflow` does not import `internal/chain`.
  - `FromLegacy(l Legacy) (Definition, State, error)`: params from settings, `At` by phase and corrections, `Iter[plans]`, `Visits[correct|fix-correct]` (corrections, +1 while at a correct step), and `Awaiting{Step, actor, round}`.
  - `LegacyView(def Definition, s State) LegacyFields`.
  - Move the logic of `equivState`/`equivStepAt`/`equivActorOfStep` (`internal/workflow/equivalence_test.go:395-470`) into it, and have the equivalence test call `FromLegacy`, so the mapping that is proven is the one that ships.

**Steps**
1. Add the migration and the db columns and tables. Tests: `TestMigration020AddsChainColumns`, `TestChainMemberLookupPrefersTable`, `TestChainGetByMemberFallsBackToLegacyColumns`, `TestChainDeleteRemovesMembersAndChecks`, `TestChainCheckNextRunIsMonotonic`.
2. Add the store twins, and member rows in `CreateChain`. Tests: `TestCreateChainWritesMemberRowsAtomically`; the existing `internal/store/chain_test.go` stays green.
3. Add `FromLegacy` and `LegacyView`. Tests: one table case per old step, in each phase, with corrections both 0 and >0 (`TestFromLegacyEveryStep`); `TestLegacyViewInvertsFromLegacy`; `TestFromLegacyCarriesAwaitedRound`. `TestEquivalenceWithChainNext` must stay green using `FromLegacy`.
4. Save this section as `docs/plans/2026-10-02-custom-chains-w2-r3.md` and commit.

**Deleted:** `equivState`, `equivStepAt` and `equivActorOfStep` from `equivalence_test.go`. They move to production as `FromLegacy`; no behaviour is lost.

**Commands**
- Focused: `go test ./internal/db/ ./internal/store/ ./internal/workflow/ -count=1`
- Full: `make check`, then `make e2e`

**Mutation for the MasterMind:** in `FromLegacy`, drop the `+1` on `Visits` while at a correct step. `TestFromLegacyEveryStep`, and an equivalence case on the correction budget, must fail.

**Halt if**
- 020 is taken on `origin/main`;
- Turso refuses `ALTER TABLE … ADD COLUMN` with these defaults;
- `FromLegacy` cannot reproduce an equivalence case without changing the equivalence test's assertions.

**Report includes** the migration text, the `SCOPES.md` diff, and the converter's mapping table as implemented.

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
