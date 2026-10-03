# Plan W5 of custom chains: cockpit chains view (#808) and Workflows section (#823)

## Preamble

**Order.** Rounds 1 to 3 build the chains view, and rounds 4 to 6 build Workflows. The two halves touch different files, but both edit `internal/ui/cmdline.go` and the `execLine` switch at `internal/ui/view_rounds.go:~267`. Run the rounds one after another, 1 → 6, to avoid merge conflicts. Round 6 saves this plan.

**Dependencies.**
- Round 1 is the read model that rounds 2 and 3 render.
- Round 4 is the shared workflow code that rounds 5 and 6 call.
- Rounds 4 to 6 do not need rounds 1 to 3.

**Facts from the code the builder should know.**
- The cockpit has no chains view. A chain shows in `:fleet` as one synthetic row, `Role: "chain"`, built by `viewChainRow` and `applyChains` in `internal/relevo/chain_status.go`.
- A chain's own member bindings are removed from `view.Report` by `applyChains`.
- `:actors`, `:candidates` and `:settings` are registered in `commands` (`internal/ui/cmdline.go:20`) and in the `execLine` switch (`internal/ui/view_rounds.go:~267-300`). The `:actors` view is `internal/ui/view_actors.go`.
- The Workflows section follows the Actors/Candidates pattern: it is a `:workflows` view, loaded off the update loop and guarded by `env.Actions != nil`.

**Spec gaps, each with a PROPOSED resolution.**

1. **Drilling into a member's round.**
   - Gap: `newRoundView`/`pointDetailAt` (`internal/ui/view_round.go:36`, `internal/ui/round_pane.go:162`) need the member's `BindingStatus` row in `env.Report`. `applyChains` removes member rows from the report.
   - Gap: `roundView.Update` and `Body` overwrite `pane.report = env.Report` on every call (`internal/ui/view_round.go:360` and `:506`). A row spliced in at open time would vanish on the next status refresh.
   - PROPOSED: the chains view carries the member rows it read. `roundView` gets an `extra []view.BindingStatus` field, which it merges into `pane.report` at both of those assignments.
2. **Server chains carry no workflow in the view.**
   - Gap: `remote.ChainView` has no workflow or engine state. `chainRowFromView` (`internal/relevo/chain_pull.go:527`) rebuilds the mirror row's `WorkflowJSON`/`StateJSON` through `workflow.FromLegacy`.
   - PROPOSED: read every chain, including `--server` ones, from the local row (the mirror for server chains). Steps, visits and outcomes come from `WorkflowJSON`/`StateJSON`. W4 will make server chains carry real workflows, and this view picks that up for free.
   - PROPOSED: for a server chain only, take the trace from `chainServerTrace` (`internal/relevo/chain_server_view.go`, a live `GetChain`). If the server is unreachable, fall back to the mirror's `Store.ChainEvents` and mark the chain `stale`. Do not guess.
3. **"Last outcome" of a step.** It is not stored per step as text.
   - PROPOSED: derive it from `State.Results[step]` (`Round`, `Status`, `Outcomes`) and `State.Visits[step]`. For a `check` step, use the red/green result and the repair target from the latest trace row for that step. Render `review r2 · verdict=changes` and `check · red → repair`.
4. **Fork children (W3 in flight).**
   - Fork children are chain rows with `db.ChainRow.Parent` set (`internal/db/chain.go:55`). Their member names are `<parent>.<child>-<actor>`.
   - PROPOSED: the read model groups by `Parent`, nests recursively with a depth cap of 8, and renders a child row indented. No fork logic. A child whose parent row is missing is shown as a root.
5. **Where it runs.**
   - PROPOSED: `local`, `placed <server>` (any member binding with `Link != nil`) or `server <name>` (`ChainRow.Server != ""`).
6. **Workflows validation sharing.**
   - Gap: `validateWorkflow`, `saveWorkflow`, `checkWorkflowName`, `addWorkflow` and the rm body live in package `main` (`cmd/relevo/config_workflow.go`), which `internal/ui` cannot import.
   - PROPOSED: round 4 moves the logic into `internal/relevo` and turns the CLI verbs into thin wrappers. Validation stays in one place, and the existing CLI tests are the regression guard.
7. **Workflows view and shipped workflows.** `workflow.Default()` is the only shipped workflow, and `ResolveWorkflow` has no source text for it. PROPOSED: the view of a shipped workflow shows its graph and a JSON rendering of the definition in place of source. Remove and edit are refused for a shipped workflow, and the notice says why.

**Rules for every round.**
- Comments say why only. No issue numbers, round numbers or history in code or tests.
- Functions are at most 70 lines and files at most 600. Remove any exclusion a touched file earns. Never add one.
- A `cmd/relevo` test must not spawn a harness or reach the network. Test the rule as a pure function in `internal/relevo`. The CLI test here only drives the flag and rendering through an in-memory runtime.
- Do not lower `testdata/coverage-baseline.txt`. No new package is added, so there is no regeneration.
- Verify with `make check` and `sh scripts/check-comments.sh`, run directly. Iterate with the focused `go test` command named in each round.

---

=== ROUND 1: chains read model and `relevo status --chains` ===

**Goal.** One pure read model of all chains for the cockpit and the CLI, with local, placed and `--server` chains read the same way.

**Seams.**
- New `internal/relevo/chains_doc.go`. Optionally split into `chains_doc_steps.go` to stay under 600 lines.
- Reuse `Store.Chains()` (`internal/store/chain.go:55`) and `Store.ChainMembers` (`:140`).
- Reuse `chainWorkflowDef` and `chainWorkflowState` (`internal/relevo/chain_engine.go:21,33`).
- Reuse `chainFactsOf`, `viewChainRow` and `ChainStatus` (`internal/relevo/chain_status.go`).
- Reuse `chainServerTrace` and `chainGetView` (`internal/relevo/chain_server_view.go`).
- Reuse `view.ChainFacts` and `view.ChainSegment` (`internal/view/chain.go`).
- `cmd/relevo/status.go:80-140` (`statusFlagSet`, `cmdStatus`).

**Steps.**
1. Define in `internal/relevo`:
   - `ChainsDoc{Chains []ChainEntry}`;
   - `ChainEntry`: name, parent, depth, status, reason, current step, `PlanPos/PlanTotal`, started-at, elapsed, where, stale flag, `Steps []ChainStep`, `Members []view.BindingStatus`, `Children []string`;
   - `ChainStep`: id, kind, actor, member binding name, visits, last outcome text, `InFlight`, round (from `Results[step].Round`; for the in-flight step, `Awaiting.Round`).
   Done when it compiles and the type is documented only where the name is not enough.
2. Add `ReadChains(ctx, rt) (ChainsDoc, error)`, built by pure helpers, with `rt` used only for I/O.
   - Walk `Store.Chains()`.
   - Build `Steps` in a stable order: definition order from a breadth-first walk from the start step, then unreachable steps sorted.
   - Resolve each step's member binding through `ChainMembers` (actor to binding) and the legacy-name rule in `internal/workflow/members.go`.
   - Derive `last outcome` as in gap 3.
   - Fill `Members` with each member's `view.BindingStatus`, the same way `ChainStatus` does (`buildReport` then match by name).
   - Compute elapsed from `CreatedAt`/`UpdatedAt` (a terminal chain uses `UpdatedAt`).
   - Compute `where` as in gap 5.
   - Sort: running and halted first, then stopped, then done; ties by name. Children follow their parent directly, whatever their own status.
   Done when the pure part is unit-tested over hand-built `db.ChainRow` fixtures using the fake-store helpers in `internal/relevo/chain_flow_helpers_test.go`.
3. Server chains.
   - Local row first.
   - If `chainOnServer(c)` and `rt.Remote != nil`, try `chainGetView` for the trace and live status.
   - On error keep the mirror's data and set `stale` with the error text. Never fail the whole doc for one unreachable server.
   Done when a fake `Remote.GetChain` test passes and an erroring fake yields a stale entry.
4. Add `relevo status --chains`.
   - Add `chains *bool` to `statusFlagValues`.
   - It is mutually exclusive with `--line` and `--name` and with a positional target. It returns `codeUsage` on a clash, like the existing `--line` check at `cmd/relevo/status.go:101`.
   - Text output is one line per chain: nested children indented two spaces, then `name  status  step · plans i/N  elapsed  where`, with the halt reason on a following line.
   - `--json` prints `ChainsDoc` (no member rows).
   - The statusline must not change.
   - The sync step before reading is the existing `SyncRemoteUnlessDaemon` block, kept.
   Done when `status --chains` works against a temp runtime.
5. Update the flag registry and help so the registry parity test passes. Run `grep -rn "statusFlagSet" cmd/relevo/*.go` for every consumer, including the contract/docs tests.

**Tests.**
- `internal/relevo/chains_doc_test.go`:
  - `TestReadChainsSortsHaltedAndRunningFirst`. Mutation: invert the status rank, and it fails.
  - `TestReadChainsNestsChildrenUnderParent`, with a missing-parent child as a root and the depth cap. Mutation: ignore `Parent`.
  - `TestReadChainsStepOutcomeText`: `review r2 · verdict=changes` and `check · red → repair`. Mutation: drop the repair target.
  - `TestReadChainsInFlightStep`. Mutation: always false.
  - `TestReadChainsServerChainStaleOnUnreachable`. Mutation: return the error instead.
  - `TestReadChainsWherePlacedAndServer`. Mutation: always `local`.
- `cmd/relevo/status_chains_test.go`, with no harness and no network:
  - `TestStatusChainsRejectsLineAndName`. Mutation: remove the clash check.
  - `TestStatusChainsJSONRoundTrips`. Mutation: print the text form under `--json`.

**Run.**
- Focused: `go test ./internal/relevo -run 'ReadChains|ChainsDoc' && go test ./cmd/relevo -run 'StatusChains|Registry|Contract'`.
- Final: `make check && sh scripts/check-comments.sh`.

**Report must include.** The `ChainsDoc` fields, the step-order rule, the stale rule, and what a done chain with no `StateJSON` shows (it must not panic).

=== ROUND 2: cockpit `:chains` list (level 1) ===

**Goal.** A `:chains` view listing every chain, running and halted first, with fork children nested.

**Seams.**
- `internal/ui/cmdline.go:20` `commands`: add `{"chains", "", "chains and their steps", false}`.
- `internal/ui/view_rounds.go:~267` `execLine` switch: add `case "chains"` guarded like `candidates`/`actors`, calling `newChainsView(env)`.
- New `internal/ui/view_chains.go`, modelled on `internal/ui/view_actors.go`: `View` interface (`Crumbs`, `Context`, `Keys`, `Capturing`, `Update`, `Body`) and `HelpKeys`. The cursor and `top` paging come from the `actorsView` pattern.
- Add a `Chains(ctx) (relevo.ChainsDoc, error)` read to the `Actions` interface (`internal/ui/actions.go:27`), implemented on `mastermindActions` by calling `relevo.ReadChains`. Extend `fakeActions` (`internal/ui/actions_test.go:40`).
- Poll: refresh on the shell tick the way `fleetView` does (check `internal/ui/live.go` and `internal/ui/fetch.go`), so a running chain moves.
- Reuse `candLine`/`fit`/`pad` and the styles in `internal/ui/styles.go`.

**Steps.**
1. Add the `Actions.Chains` read plus the `mastermindActions` implementation and the fake. Done when `go build ./...` passes.
2. Add `chainsView` with a `chainsMsg` load command (off the update loop, like `settingsDocCmd`), loading, error and empty states, and a refresh on the shell's tick. Done when `:chains` opens.
3. Render columns: NAME (indented two spaces per depth), STATUS, STEP, PLANS `i/N`, AGE, WHERE. A halted chain shows its reason as a dim second line. The cursor row is bold, as in `candLine`.
   Keys: `j/k` (and arrows) move, `enter` expands (round 3), `esc` back, `t` trace (round 3). Add `f` to jump to `:fleet` only if the pattern already has it. Otherwise leave it out.
   Done when the golden shows the right words at 132 columns and degrades at 100 and 80.
4. Register the `chains` command in the help and key lists. Update the help overlay golden if the help lists commands.

**Tests (golden/model, via `goldenModel` in `internal/ui/golden_test.go`).**
- `chains-132.golden` and `chains-100.golden`: a running chain, a halted chain with a reason, and a fork parent with two nested children (one done, one halted). Regenerate with `go test ./internal/ui -run Golden -update`, then read the diff by eye.
- `TestChainsViewSortsAndNests` (model test on `Body` lines). Mutation: sort by name only.
- `TestChainsViewWithoutActionsRefused` (the notice). Mutation: remove the guard.
- `TestChainsViewServerChainShowsStale`. Mutation: drop the stale marker.
- Update the cmdline and help goldens that list the commands.

**Real-screen check (required before the round is done).** Follow `.claude/skills/capturing-tui-screens/SKILL.md`.
- Build the binary outside PATH.
- Run it at 132x34, 100x30 and 80x24 against real chain rows. Use fixtures in a temp `XDG_STATE_HOME`, not the user's real state.
- Press `:chains`, `j`/`k` and `esc` only. Never press action keys.
- Save the PNGs and describe what they show in the report.

**Run.** Focused: `go test ./internal/ui -run 'Chains|Golden|Cmdline|Help'`. Final: `make check && sh scripts/check-comments.sh`.

**Report must include.** The columns at each width, the PNG paths, and any golden that changed besides the new ones.

=== ROUND 3: expand to steps (level 2), trace, drill into a member's round (level 3) ===

**Goal.** `enter` on a chain shows its steps, with the step in flight highlighted. `t` shows the trace. `enter` on a step opens the existing binding detail for that step's member at that step's round, and `esc` returns to the steps.

**Seams.**
- `internal/ui/view_chains.go` from round 2, plus a new `internal/ui/view_chain_steps.go`.
- `internal/ui/view_round.go:36` `newRoundView`, and `roundView`'s `pane.report = env.Report` at `:360` and `:506`.
- `internal/ui/round_pane.go:162` `pointDetailAt`, where `r.Chain != nil` handling shows how a chain row differs from a member row.
- Trace text: `relevo.RenderTrace` (`internal/relevo/chain_trace.go:122`). Add a `ChainTrace`-backed read to the `Actions` seam, or reuse the `ChainTraceDoc` already in `ChainEntry` if round 1 carried it. Keep one source.
- Push/pop: `push`, `pop` (`internal/ui/view.go`). The stack gives "back returns to the chain" for free.

**Steps.**
1. Add `chainStepsView`, a value View pushed by `enter` on a chain row.
   - It lists the steps in order.
   - Columns: STEP, KIND, ACTOR, VISITS, LAST OUTCOME.
   - The step in flight is highlighted with the cursor-row accent. A step never reached is faint.
   - Fork children listed under the parent row open the same view for the child, and nest the same way.
   Done when a golden shows a halted chain with `check · red → repair`.
2. Add the trace key.
   - `t` pushes a read-only scroll view of the trace, reusing the viewport pattern from `view_log.go`.
   - Text is sanitised with `sanitizeText` (`internal/ui/detail.go`).
   - A server chain's trace comes through the same read.
   Done when a trace golden passes.
3. Add `roundView.extra`, merged into `pane.report` at both assignments.
   - Write one helper, `mergeRows(env.Report, extra)`.
   - Do not duplicate the row loop.
   Done when the model test shows a member round survives a status refresh.
4. On `enter` on a step, call `newRoundView` with an `Env` whose `Report` has the step's member rows appended, and `round = step.Round` (0 means the default).
   - A step never run has no member round, so the view shows the notice "no round yet" and does not push.
   - A check step has a run log and no member round. Drill to the writer member's round that produced it, or show the notice "check runs have no round" if no such round is derivable. Pick the first and say which you picked in the report.
   Done when the golden shows the existing detail pane (prompt, report, diff, log, transcript tabs) under the crumbs `chains › <chain> › <step>`.
5. Crumbs and keys.
   - Update `Crumbs()` for both new views.
   - Update the footer and help keys. `esc` pops one level.

**Tests.**
- `chain-steps-132.golden`: a running chain with a fork parent and its two children. The parent's own steps are listed, and the children are rows that open their own steps.
- `chain-steps-halted-132.golden`: a halted chain.
- `chain-drill-132.golden`: the member-round detail.
- `chain-trace-132.golden`.
- `TestChainDrillSurvivesStatusRefresh`. Mutation: restore the unconditional `pane.report = env.Report`.
- `TestChainDrillOpensStepRound`, with the round number from `Results`. Mutation: always round 0.
- `TestChainDrillNoRoundYetNotices`. Mutation: push anyway.
- `TestChainEscReturnsToChain`. Mutation: `esc` pops two views.
- `TestChainsServerChainViaFakeGetChain`: the trace is read through a faked `GetChain` and the chain still opens when unreachable (stale).

**Real-screen check.** Repeat round 2's method for all three levels at 132x34 and 100x30. Include a halted chain and a fork with children. Drill into a member's round and confirm the tabs load, with no stuck "loading…". Press `esc` back to the steps and back to the list. Save the PNGs.

**Run.** Focused: `go test ./internal/ui -run 'Chain|Round|Golden'`. Final: `make check && sh scripts/check-comments.sh`.

**Report must include.** The check-step drill choice, the PNG paths, and the files that changed under `internal/ui/testdata`.

=== ROUND 4: move the workflow verbs' logic into `internal/relevo` ===

**Goal.** The cockpit and the CLI call one implementation. No behaviour change. This is a pure refactor.

**Seams.**
- `cmd/relevo/config_workflow.go`: `addWorkflow` (`:106`), `checkWorkflowName` (`:134`), `validateWorkflow` (`:151`), `saveWorkflow` (`:174`), the rm body (`:198-238`), the show body (`:253-290`), and `editWorkflowLoop` (`:330`) with `reopenWith` (`:393`).
- Already shared: `relevo.WorkflowEditRound` and `WorkflowEditReopen` (`internal/relevo/workflow_edit.go`), `relevo.ResolveWorkflow` (`internal/relevo/workflow_resolve.go`), `relevo.EmbedFileSeeds`.
- `config.StoredWorkflow`, `config.EncodeWorkflows` (`internal/config/workflows.go`).
- `rt.RoleRegistry().WorkflowActors()`, `rt.Config.As("cli", message).Put(config.Workflows, body)`.
- `ui` writes go through `mastermindActions.ApplyConfig` and `ConfigEdit` (`internal/relevo/configedit.go`). Check whether `ConfigEdit{Sections}` can carry the workflows body, so the cockpit's write goes through the same audited path. If it can, use it. If not, call the same put with the actor `ui`, and say which in the report.

**Steps.**
1. Create `internal/relevo/workflow_ops.go` with these functions (names are proposals):
   - `WorkflowList(rt) ([]WorkflowSummary, error)`: shipped first, then saved by name, each with `Origin` (`shipped|saved`), description, inputs (plans/task) and params.
   - `WorkflowSource(rt, name) (text string, shipped bool, err error)`.
   - `WorkflowAdd(rt, path, replace, force) (name string, err error)`.
   - `WorkflowRemove(rt, name) error`, refusing a shipped name.
   - `WorkflowSave(rt, name, source, def, message) error`.
   - `WorkflowValidateProblems(rt, def) []string`, returning the problems and not a joined error, so both callers can show them inline or join them.
   Errors are typed (sentinel errors) so the CLI maps them to its exit codes (`codeConflict`, `codeConfigInvalid`, `codeConfigPathNotSet`) and the cockpit maps them to text.
   Done when it compiles with no behaviour change.
2. Make `cmd/relevo/config_workflow.go` call them. The exit codes and messages stay byte-identical. Keep only flag parsing and printing, plus `runEditor` for the CLI loop. The CLI edit loop keeps calling `WorkflowEditRound`.
   Done when `go test ./cmd/relevo -run Workflow` passes unchanged.
3. Add `WorkflowGraph(def workflow.Definition) []GraphRow`, returning each step in the same order as round 1's step order, with kind, actor and edges (`on` targets, budget `then`, `done`/`halt`). This is the data for the step-graph view. It may reuse `stepEdges`/`kindOf` by moving a small exported helper into `internal/workflow/graph.go`.
   Done when it has a unit test, including a `fork` and a `for-each`.

**Tests.**
- Existing `cmd/relevo/config_workflow_test.go` stays green. It is the regression guard.
- New in `internal/relevo/workflow_ops_test.go`:
  - `TestWorkflowAddRefusesExistingWithoutReplace`. Mutation: ignore `replace`.
  - `TestWorkflowAddRefusesShippedNameWithoutForce`. Mutation: ignore `force`.
  - `TestWorkflowRemoveRefusesShipped`. Mutation: allow it.
  - `TestWorkflowListMarksShippedAndSaved`. Mutation: all `saved`.
  - `TestWorkflowGraphEdges`. Mutation: drop the budget `then` edge.
  - `TestWorkflowValidateProblemsMatchesCLIText`. Mutation: change the problem order.

**Run.** Focused: `go test ./internal/relevo -run Workflow && go test ./cmd/relevo -run 'Workflow|ConfigWorkflow'`. Final: `make check && sh scripts/check-comments.sh`.

**Report must include.** The function list, the typed errors and the exit-code mapping, and confirmation that `git diff --stat` for `cmd/relevo` is deletions plus wrappers only.

=== ROUND 5: cockpit Workflows section: list, view, remove, add from a file ===

**Goal.** A `:workflows` view that lists workflows, views one as its step graph with the source one key away, removes a saved one, and adds from a file.

**Seams.**
- `internal/ui/cmdline.go:20` `commands`: add `{"workflows", "", "shipped and saved workflows", false}`.
- `internal/ui/view_rounds.go` `execLine`: add `case "workflows"`, guarded by `env.Actions != nil` like `actors`.
- New `internal/ui/view_workflows.go` and `internal/ui/workflow_form.go`, modelled on `internal/ui/view_actors.go`, `internal/ui/actor_form.go` and `internal/ui/confirm.go`. Reuse the existing text-input form and confirm patterns (`form.go`, `form_rows.go`, `confirm.go`).
- `Actions` interface (`internal/ui/actions.go:27`): add `Workflows() ([]relevo.WorkflowSummary, error)`, `WorkflowSource(name) (string, bool, error)`, `WorkflowGraph(name) ([]relevo.GraphRow, error)`, `WorkflowAdd(ctx, path string, replace bool) Result` and `WorkflowRemove(ctx, name) Result`. Implement them on `mastermindActions` by calling round 4's functions. Extend `fakeActions`.
- Refresh after a write: follow `ApplyConfig`, which reloads this adapter's runtime. Mirror that.

**Steps.**
1. List.
   - Columns: NAME, ORIGIN (`shipped`/`saved`), DESCRIPTION, INPUTS (`plans`/`task`), PARAMS.
   - Loading, error and empty states as in `actorsView`.
   - Keys: `j/k` move, `enter` view, `a` add, `d` remove, `e` edit (round 6), `esc` back.
   Done when the golden passes.
2. View.
   - `enter` pushes a view with the step graph: one row per step with kind, actor and edges (`on` → target, budget → target, `done`, `halt: reason`).
   - `s` toggles to the source text, scrollable. A shipped workflow shows the JSON of its definition (gap 7).
   - Source text is sanitised with `sanitizeText`.
   - Done when both golden states render.
3. Remove.
   - `d` on a saved workflow opens a confirm (the `confirm.go` pattern). `y` calls `WorkflowRemove`.
   - `d` on a shipped one gives the notice "shipped workflows cannot be removed". It opens no confirm.
   Done when the golden shows both the confirm and the notice.
4. Add from a file.
   - `a` opens a one-field form for a path (YAML or JSON), with `~` expanded.
   - On submit it validates through `WorkflowAdd`. Problems appear inline under the field, one per line, and the form stays open.
   - An existing name returns a typed conflict, which shows a confirm "replace <name>? y/n". `y` re-calls with `replace=true`.
   - A shipped name is refused with the shipped message, and `--force` is not offered in the cockpit.
   Done when the goldens show an inline problem list and the replace confirm.

**Tests (golden/model).**
- `workflows-132.golden` and `workflows-100.golden`.
- `workflow-view-132.golden` (graph) and `workflow-source-132.golden`.
- `workflow-add-invalid-132.golden`: an inline validation error.
- `workflow-replace-confirm-132.golden` and `workflow-remove-confirm-132.golden`.
- `TestWorkflowsRemoveRefusesShipped`. Mutation: open the confirm.
- `TestWorkflowsAddExistingAsksReplace`. Mutation: replace silently.
- `TestWorkflowsAddInvalidKeepsFormOpen`. Mutation: close the form on a problem.
- `TestWorkflowsViewToggleSource`. Mutation: ignore `s`.
- `TestWorkflowsWithoutActionsRefused`. Mutation: remove the guard.
- All these tests run against `fakeActions` plus temp-dir files. No harness is spawned and nothing touches the network.

**Real-screen check.** Run the binary with a temp `XDG_CONFIG_HOME` and `XDG_STATE_HOME`. Seed one saved workflow with `relevo config workflow add`, then walk list → view → `s` → `esc` → `d` (cancel with `n`) → `a` with a bad file and then a good one. Capture at 132x34 and 100x30. Remove the temp workflow afterwards. Never touch the real config.

**Run.** Focused: `go test ./internal/ui -run 'Workflow|Golden|Cmdline'`. Final: `make check && sh scripts/check-comments.sh`.

**Report must include.** The write path used (`ApplyConfig` or direct, per round 4), the PNG paths, and any `commands` or help golden that changed.

=== ROUND 6: edit in `$EDITOR`, the fake-editor loop, and saving this plan ===

**Goal.** `e` on a saved workflow runs the same loop as `relevo config workflow edit <name>`: validate on save, reopen with the problems listed as comments until the workflow is valid or the user quits without changes. The round also saves this plan.

**Seams.**
- `internal/relevo/workflow_edit.go`: `WorkflowEditRound`, `WorkflowEditReopen`, `WorkflowEditProblemMarker`, `StripWorkflowEditProblems`.
- The editor command: `mastermindActions.AgentEditor` (`internal/ui/open.go:18`), used with `tea.ExecProcess` as in `internal/ui/view_agent.go:320-324` and `internal/ui/round_head.go:171`.
- The CLI loop: `editWorkflowLoop` (`cmd/relevo/config_workflow.go:330`) shows the loop semantics to match. `prev` is the buffer last written, and an unchanged or empty buffer is the quit.
- `WorkflowSave` and `WorkflowSource` from round 4.
- `internal/ui/view_workflows.go` from round 5.

**Steps.**
1. Put the loop state in a pure, testable step type in `internal/ui` (a small struct plus one transition function) holding `name`, `saved config.StoredWorkflow`, `prev []byte` and the temp path. The transition takes the edited bytes and returns one of three outcomes: saved, done (no change or abort), or reopen with `WorkflowEditReopen(problems, edited)`. It calls `relevo.WorkflowEditRound` and does not re-implement validation.
   Done when it has a table-style model test with no editor.
2. Run it in the cockpit.
   - `e` writes `prev` to a `0600` temp file, builds the command through `AgentEditor`, and runs it under `tea.ExecProcess`.
   - The exec callback reads the file, runs the transition, and then does one of three things. On saved, it calls `WorkflowSave`, reloads the list and shows a notice with the config version. On reopen, it rewrites the file and runs the editor again, and the problems stay visible in the file, as in the CLI. On done, it shows a notice ("no changes" or "aborted").
   - A non-zero editor exit means "nothing changed" with a notice.
   - A name change in the edited source is refused with the same `# workflow name … does not match` problem line as the CLI. Take that line from a shared helper from round 4, not a copy.
   - The temp file is removed on every exit path.
   Done when the loop works in the model tests.
3. Shipped workflow: `e` shows the notice "shipped workflows cannot be edited. Copy it with `relevo config workflow show`, then add it under a new name."
4. Save this whole plan document to `docs/plans/2026-10-02-custom-chains-w5.md`, byte for byte. This is the last step.
5. Update the spec's section 7.1 last bullet, "The cockpit's Workflows settings section (#823) is built on these verbs", only if a statement in it is now false. Otherwise leave the spec alone.

**Tests.**
- `TestWorkflowEditLoopSavesValid` (fake editor script via `t.Setenv("VISUAL", script)` where the script writes a valid workflow into the file). Mutation: skip `WorkflowSave`.
- `TestWorkflowEditLoopReopensWithProblems`: the first pass writes an invalid file, and the second pass sees the marker plus the problem lines at the top. Mutation: reopen with the bare file, no problems.
- `TestWorkflowEditLoopQuitUnchangedEnds`. Mutation: loop forever, caught by a bound of N passes in the test.
- `TestWorkflowEditLoopRefusesRename`. Mutation: allow the rename.
- `TestWorkflowEditLoopNeverStoresProblemBlock`: the saved source has no marker line. Mutation: skip `StripWorkflowEditProblems`.
- `TestWorkflowEditShippedRefused`. Mutation: open the editor.
- `workflow-edit-problems-132.golden`: the view after a reopen, showing the notice.
- The fake editor is a shell script in `t.TempDir()`. No terminal, harness or network is involved. Because `tea.ExecProcess` needs a real TTY, drive the transition function directly in the model test and test the exec wiring only up to the `tea.Cmd` it returns.

**Real-screen check.** Run the binary in tmux per the capture skill with `VISUAL` set to a small script that appends a bad step. Confirm the cockpit suspends, the editor shows the problem comments on the reopen, and the cockpit redraws after a good save. Use a temp `XDG_CONFIG_HOME`. Save the PNGs.

**Run.** Focused: `go test ./internal/ui -run 'WorkflowEdit|Workflow'`. Final: `make check && sh scripts/check-comments.sh`, then confirm `git diff --stat` shows `docs/plans/2026-10-02-custom-chains-w5.md`.

**Report must include.**
- The fake-editor approach.
- The PNG paths.
- A statement that no coverage baseline was lowered.
- The exact `git diff --stat` against the declared scope: `internal/relevo`, `internal/ui` (and `testdata`), `cmd/relevo` (status and `config_workflow`), `internal/workflow` (the graph helper only), and `docs/plans`.

