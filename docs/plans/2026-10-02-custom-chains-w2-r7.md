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

## Round 7: the new engine drives custom workflows end to end

**Behaviour.** A chain started with a non-default `--workflow` runs entirely on `workflow.Next`. Default chains stay on the old engine (`c.WorkflowJSON == ""` selects the old path; non-empty selects the new).

- **Start.**
  - Validate, cut the worktree, and create one member per used actor, all or none, with `Gate=""`, `Regate=0` and verify off.
  - Write the `chain_member` rows, store the parameter-applied definition and the start state (`workflow.Start`), and copy plans and task in.
  - Run the start actions in the same critical section.
  - Refuse when any writer member is placed remotely and some check step's command renders non-empty. The refusal names the server and the feature `check` (W4).
- **Members share one tree.** `ErrCWDTaken` is exempted only among members of the same chain:
  - `store.Tx.CreateChain` passes the sibling set to `prepareSave`;
  - later saves look up "same chain" through `chain_member`;
  - a non-member writer on the tree is still refused.
- **Close.**
  - The event's step and member come from the state's `Awaiting`. The member is the actor; the closing binding's actor comes from its `chain_member` row.
  - Outcomes come from Round 1's `chainParseOutcomes`.
  - Artifact keys: a writer gives `report`, `diff` and `commits`; a reader gives `output` plus its one declared artifact (the saved output path).
  - A `MissingArtifact` failure closes the step halted, with the engine's reason.
  - `status` comes from the report tail, with the non-done reason built by what is now `chain.BuilderHaltReason`, moved to `internal/relevo`.
  - A writer that declares outcomes gets them added to its builder-prompt block (send.go `builderPrompt` 45-64).
- **Actions.** Each runs in the same critical section:
  - `send`: `chainRenderSeed` (Round 5), then `chainSendMember` (`chain_send_remote.go:23-28`). `Awaiting.Round` is filled from the sent round.
  - `run_check`: `chainStartCheck` (Round 4). `tickChainChecks` is now registered in the daemon right after `tickChains` (`daemon.go:160`), and its end feeds `check_closed` through the same driver under the lock.
  - `finish`, `halt` and `stop`: a generalised `chainTerminal`, with one delivery on the first surviving member in `chain_member` order. The payload is `chain X <verb>: status S, at <step>, plans i/N.` plus the reason, the resume hint and the trace command.
- **Sweep, stop and done.**
  - `tickChains`/`chainSweep` (`chain_sweep.go:20-106`) walk `chain_member`.
  - `ChainStop` sends `stopped`.
  - `ChainDone` and the input sweep carry over.
- **Resume.**
  - `workflow.Resume` with `From`, or with `Closed` when the halted run step's member has a newer closed round (built by the close path).
  - A stopped run step whose round was opened is re-sent with its own staged prompt (`PromptPath(member, round)`).
  - `--param` on resume applies `WithParams` and re-validates, and creates missing members (Q2).
- **Trace rows.** `step` holds the step id and the event/action columns hold workflow JSON. `RenderTrace` prints a new row as `review r2  verdict=changes → correct`; a legacy row renders as today.
- **Status.** A row with `StateJSON` shows `<step> r<N> · plans i/N` (`check run <N>` while a check is awaited). `relevo show <chain> --workflow` prints the stored definition as indented JSON.
- **Triage e2e.** `TestChainTriageE2E` is added to the Makefile `e2e` filter. The fake harness learns a yes/no reader keyed on the seed's first line, `Should we build this?`. The answer is read from the task text, which carries `answer yes` or `answer no`.
  - The yes run goes triage → build → check (`true`) → done.
  - The no run halts with `triage said no`.

**Seams.** These are new files; `chain.go` stays under 600 lines with one dispatch line.
- `internal/relevo/chain_engine.go`: `chainAdvance(ctx, rt, tx, c, ev workflow.Event) error` and `chainRunActions`.
- `chain_close.go`: `chainEventFromCloseWF`.
- `chain_start_wf.go`: `chainCreateWorkflow`.
- `chain_resume_wf.go`.
- `chain_terminal_wf.go`.
- `chain.go` `chainApply` (218): one early dispatch when `len(c.WorkflowJSON) > 0`.
- `reconcile.go:437-442,682`: pass what the new close needs.
- `internal/store/lifecycle.go:305-329` `assertCWDFree`, and the preflights at `bind.go:676-686` and `add.go:388-394`: the same-chain exemption.
- `internal/relevo/chain_trace.go:34-45,87-103`: `ChainTraceEvent` gains `Step`, `Flow *workflow.Event` and `FlowAction *workflow.Action`.
- `internal/relevo/chain_status.go:30-87` and `internal/view/chain.go:12-52`: `ChainFacts` gains `StepAt`, `Round`, `PlanPos`, `PlanTotal` and `Check bool`.
- `internal/relevo/show.go:27-48` and `cmd/relevo/show.go:57-148`: `ShowWorkflow`.
- e2e: `internal/e2e/headless_test.go` `fakeHarnessScript` (356-524), and a new `internal/e2e/chain_triage_test.go`.

**Steps**
1. Start and members:
   - `TestWorkflowChainStartCreatesMembersAllOrNone` (a forced failure of the third member leaves no row, no member and no worktree)
   - `TestWorkflowChainMembersRunNoGate`
   - `TestCWDExemptOnlyAmongSameChain`
   - `TestPlacedWriterWithCheckRefusedNamingFeature`
   - `TestPlacedWriterWithEmptyCheckAllowed`
2. The close and the send action:
   - `TestWorkflowCloseParsesDeclaredOutcomes`
   - `TestWorkflowCloseMissingArtifactHalts`
   - `TestWorkflowCloseReplayedIsIgnored` (no second trace row)
   - `TestWorkflowSendRendersSeedPaths`
3. The check action and its tick:
   - `TestWorkflowCheckGreenRoutesGreen`
   - `TestWorkflowCheckRedRoutesRed`
   - `TestWorkflowEmptyCheckRoutesGreenWithoutRun`
   - `TestWorkflowCheckLogReferenceable` (`{{check.log}}` resolves after the close)
4. Terminal, sweep and stop:
   - `TestWorkflowTerminalDeliversOnce`
   - `TestWorkflowSweepHaltsOnGoneMember`
   - `TestWorkflowStopSendsStopped`
5. Resume:
   - `TestWorkflowResumeFromStep`
   - `TestWorkflowResumeUnknownStepListsSteps`
   - `TestWorkflowResumeTakesNewerManualRound`
   - `TestWorkflowResumeResendsStoppedPrompt`
   - `TestWorkflowResumeParamCreatesMissingMember`
6. Trace, status and show:
   - `TestRenderTraceWorkflowRow`
   - `TestRenderTraceLegacyRowUnchanged`
   - `TestChainSegmentStepRoundPlans`
   - `TestShowWorkflowPrintsStoredDefinition`
7. Remove Round 6's "non-default needs `--dry-run`" refusal (test `TestChainFlagsCustomWorkflowNeedsDryRun` becomes `TestChainFlagsCustomWorkflowStarts`). Add the triage e2e and the Makefile filter entry. `make e2e` runs `TestChainTriageE2E`, which covers both edges.
8. Save this section as `docs/plans/2026-10-02-custom-chains-w2-r7.md` and commit.

**Deleted (closed list)**
1. Round 6's temporary refusal of a non-default `--workflow` without `--dry-run`.
2. For new-engine chains only: the builder member's own gate and regate. The old path keeps them until Round 8.

**Commands**
- Focused: `go test ./internal/relevo/ ./internal/store/ ./internal/view/ ./cmd/relevo/ -run 'Workflow|CWDExempt|Placed|RenderTrace|ChainSegment|ShowWorkflow|ChainFlags' -count=1`, then `go test ./internal/e2e/ -run TestChainTriageE2E -count=1`
- Full: `make check`, then `make e2e`

**Mutation for the MasterMind:** in `chainAdvance`, skip the `Awaiting` match and pass every close to `Next` with `Awaiting` rewritten to the closing round. `TestWorkflowCloseReplayedIsIgnored` must fail.

**Halt if**
- any old-path chain test or e2e changes outcome;
- the `ErrCWDTaken` exemption cannot be limited to same-chain members;
- a check's end cannot reach the driver under the lock without a second critical section.

**Report includes**
- the triage e2e trace, as printed by `show --trace`;
- the new status line;
- the new terminal payload wording;
- both command outputs.
