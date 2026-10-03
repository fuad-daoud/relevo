# Plan W3: `fork` sub-chains (#754), as six builder rounds

## Preamble

**Base:** origin/main, with W1 (`internal/workflow`) and W2 (`internal/relevo/chain_*.go`) merged. Spec 4.5 of `docs/specs/2026-10-01-custom-chains-design.md` is approved and is built as written. Where the code or the spec is silent, a gap is listed below and marked PROPOSED.

**Order and dependencies:**

| Round | Title | Depends on |
|---|---|---|
| 1 | Engine: the `fork` step kind | none (pure) |
| 2 | Child chain rows and start | 1 |
| 3 | Child end, the join and the merge | 1, 2 |
| 4 | Resume, stop, sweep | 3 |
| 5 | Surfaces: status, statusline, trace | 2 (rows); needs 3 for join rows |
| 6 | e2e and the plan file | 1-5 |

Rounds run in sequence, so 5 may run after 4. Each round ends with a green `make check`. The e2e is not part of `make check`, so round 6 runs `make e2e` itself.

**What the code shows today:**
- `workflow.Step.Fork`, `Fork{Each, Workflow, Children}` and `ForkChild{Workflow, Plans, Task}` already parse, marshal and validate. This covers rule 7 (`validate.go:392-433`) and the `fork.each` template refs (`validate_refs.go:33`).
- `Awaiting.Children`, `EventChildEnded`, `Event.Child`, `ActionFork` and `ActionMerge` are declared in `state.go` but nothing uses them.
- `enter.go:66` halts a fork with "fork steps are not run by this engine". `next_test.go:~244` pins that string.
- `db.ChainRow.Parent` is stored and loaded (`db/chain.go:55`, `:138`, `:340`) and migration 021 added the column. Nothing writes it yet.
- `chainRunAction` (`chain_engine.go:147`) returns an error for `ActionFork`.
- `chainTerminalWF` (`chain_terminal_wf.go:21`) is the one place every end passes through. `chainApply` (`chain.go:140`) is the one place a member close reaches the engine.

**Spec gaps, with proposed resolutions:**

1. **`each` source.** Spec 4.5 shows `each: plans.current-stage`. Validation (`checkForEachSource`) knows only `plans` or a single `{{step.artifact}}` list reference. PROPOSED: a fork's `each` uses the same two forms, and each item becomes one child run with `Plans: [item]` (an artifact item is a round-file key, as `for-each` items are). `current-stage` is not built. Validate `fork.each` with the existing `checkForEachSource`.
2. **Child keys and names.** The spec says `<parent>.<child>-<actor>` but does not define `<child>`. PROPOSED:
   - The engine names children `"1".."N"` (declaration order for `children`, item order for `each`). `Awaiting.Children` holds these keys.
   - The caller turns key `k` into the chain name `<parent>.<k>`. Its members are `<parent>.<k>` (the builder) and `<parent>.<k>-<actor>`. This keeps W2's rule that the member suffix follows `chainMemberNames`.
   - Store binding names allow `.`: `actorNameRe` is `^[a-z0-9][a-z0-9._-]{0,63}$` (`store/paths.go:27`). The workflow `namePattern` forbids `.`, but that applies only to workflow and actor names.
   - The chain-name cap (`chainNameCap`) is checked for every child name at fork time. For `each` the key is at most 2 digits.
3. **`halted` is not required.** Spec 4.5 lists three outcomes but rule 3 only requires `joined` and `conflict`. PROPOSED: `halted` is an optional edge. An unwired `halted` halts the parent with the first halted child's reason. A wired `halted: { halt: ... }` does the same with the author's wording. A `halted` edge may also name a step, but then the fork is not re-openable by resuming a child (round 4).
4. **A new event, `merge_closed`.** Spec section 5 lists events `step_closed`, `check_closed`, `child_ended`, `needs_you` and `stopped`, and an action `merge`, but no event that carries the merge result back. PROPOSED: add `EventMergeClosed` carrying `Result` (`joined | conflict`) and `Artifacts{"conflict": [key]}`. The fork step awaits `child_ended` events first, then the one `merge_closed`.
3. **Conflict semantics.** `git.Client.Merge` (`internal/git/sync.go:71`) aborts the merge on a conflict. Spec 4.5 requires the tree to keep the conflict. PROPOSED: add `git.Client.MergeKeep(ctx, dir, ref) (paths []string, err error)`, which leaves the in-progress merge and returns `ErrMergeConflict` with the unmerged paths. relevo merges children in key order and stops at the first conflicting child. The conflict file (`{{<fork>.conflict}}`) lists the conflicted paths. It also lists the branches not yet merged (the stopped child's later siblings), because the author's merge round must merge them itself. A non-conflict git failure halts the parent.
4. **Fork needs a writer on the parent.** The merge target is the parent writer's tree and branch. PROPOSED: a new validation rule refuses a fork in a workflow with no writer `run` step. Start refuses a fork on a placed (`--server` or remote) writer, with a message naming W4. Section 12.5 only supports one placed writer check, and merging into a remote tree is not designed.
5. **A child's input given.** Rule 7 already validates the child with `Given{Plans: ...}`. PROPOSED: `each` and `ForkChild.Plans` hand the child its plan copies, and `ForkChild.Task` its task text, through the existing `chainCopyPlans` and `writeChainTask`.
6. **Creating children under the lock.** `chainRunAction` runs inside the caller's critical section, and `chainCreateWorkflow` (`chain_start_wf.go:303`) takes its own `rt.Store.WithLock`. Calling it from an action would deadlock or re-enter. PROPOSED: round 2 extracts the persist and first-send parts of `chainCreateWorkflow` into variants that take the caller's `tx`, and round 2's step 1 verifies the lock is not re-entrant before building on it. If the builder finds children cannot be created in the parent's tx, halt and report; do not work around it. Any further re-reading of this is `STOP` territory, not improvisation.
7. **Clean-up.** Children's bindings and worktrees are not removed when the parent ends. They end like any chain's members (`relevo done` / `unbind`). Automatic removal is out of scope for W3.

**Style rules for every round** (CLAUDE.md): no history in comments; comments say why; functions ≤70 lines; files ≤600. These files are close to the limit, so new code goes in new files: `validate.go` (485), `chain_start_wf.go` (487), `chain_engine.go` (410), `chain_resume_wf.go` (406). No new exclusions in `.golangci.yml`, `scripts/check-comments.sh` or `scripts/check-filesize.sh`. No coverage baseline lowered; regenerate `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` only if a package moved code, and say so in the report.

**CI constraint:** `cmd/relevo` tests must not run a subcommand that spawns a harness or reaches the network. All rounds test as pure functions in `internal/workflow` or `internal/relevo` (the fakes in `chain_flow_helpers_test.go`). Only round 6 uses the fake-harness e2e in `internal/e2e`.

---

=== ROUND 1: Engine — the `fork` step kind (pure, `internal/workflow`) ===

**Goal:** a fork step starts children, waits for all of them, asks for a merge, and routes `joined | conflict | halted`. Nothing else in the repo changes except the one deleted test and `enter.go:66`.

**Seams:**
- `internal/workflow/state.go`: `Awaiting` (line 34), `EventKind` consts (line 62), `Event` (line 76), `Action` (line 130), `known()` (lines 186, 195), `EncodeEvent`/`DecodeEvent`.
- `internal/workflow/enter.go`: `Start`/`seedPlans` (lines 10-29), `enter` (line 66, the fork case).
- `internal/workflow/next.go`: `Next`, `awaits` (line 30), `matchesCheck` (line 52), `matchCheck` (the model for `matchFork`).
- `internal/workflow/validate.go`, `validate_match.go:22`, `validate_refs.go:33`. A new file, `validate_fork.go`, holds the new rule.
- New file `internal/workflow/fork.go` holds the fork transitions.

**Steps:**
1. **State and events** (`state.go`).
   - `Awaiting` gains `Ended map[string]ChildEnd` and `Merging bool`.
   - New type `ChildEnd{Status, Reason string}`.
   - New `ChildSpec{Key, Workflow, Task string; Plans []string}`, carried by `Action.Children []ChildSpec`.
   - New `EventMergeClosed`. Add it to `known()`.
   - `Event.Child` and `Event.Status` carry `child_ended` (`done | halted | stopped`) and `Reason`. `merge_closed` uses `Result` and `Artifacts`.
   - Done when `EncodeEvent`/`DecodeEvent` round-trip a `merge_closed` and a `child_ended`, and `go build ./internal/workflow` passes.
2. **Entering a fork** (`enter.go`, `fork.go`).
   - Replace the `fork` case with `enterFork`.
   - Build `ChildSpec`s: `children` yields keys `"1".."N"` in order; `each` yields one child per item with `Plans: [item]`.
   - Each-source items come from the plans input (seeded in `seedPlans` into `Iter[forkID]` for a fork whose `Each == "plans"`) or from `Results[root].Artifacts[attr]`, as `forEachIter` does.
   - Set `Awaiting{Step: id, Children: keys}`. Emit one `ActionFork{Step, Children}`.
   - An empty `each` list is not a child set: it routes as a clean `joined` without an action (resolution: no children, nothing to merge). Budgets and `resetVisits` apply to a fork as to any step.
   - Done when `Start` and `Next` on a two-child fork return that one action and that awaiting.
3. **Awaiting and replay guard** (`next.go`, `fork.go`).
   - Extend `awaits`: `child_ended` matches when `Awaiting.Step == e.Step`, the child is in `Children`, it is not in `Ended`, and `Merging` is false. `merge_closed` matches when `Merging` is true and the step matches.
   - `matchesCheck` gains a guard so a state holding `Children` never matches a `check_closed`. `needs_you` and `stopped` on a fork state match on `Step` alone.
   - Done when a duplicate `child_ended` and a `child_ended` for a stranger return the state unchanged with no actions.
4. **`child_ended`** (`fork.go`).
   - Record `Ended[child]`.
   - While any child is still open, return no action and keep `Awaiting`.
   - When all have ended: if any child's status is not `done`, take the `halted` outcome. Record `Results[fork]` with outcome `result=halted`. Route `halted` from `on` if present (match `result=halted`, then bare `halted`), otherwise halt the run with the first halted child's reason (children in key order; a `stopped` child's reason reads `child <k> stopped`).
   - If every child is `done`, set `Merging = true` and emit `ActionMerge{Step}`.
   - Done when each branch has a table case.
5. **`merge_closed`** (`fork.go`, `next.go`).
   - Record `Results[fork]` with outcomes `result=<joined|conflict>` and artifact `conflict: <keys>`.
   - Route through a new `matchFork` (same shape as `matchCheck`: `result=<v>`, bare `<v>`, then `else`). An unmatched edge halts: `<step> <result>: no edge matches`.
   - The state after a join clears `Awaiting`. A `conflict` result keeps `Results[fork]` so `{{fork.conflict}}` resolves in the next step.
6. **`ReopenFork`** (`fork.go`). A pure `ReopenFork(s State, child string) (State, bool)`: valid only when `Status == halted`, `At == Awaiting.Step`, a fork is awaited and `child` is in `Ended` with a non-`done` status. It returns the state with `Status = running`, `Reason = ""`, and `Ended[child]` removed, and `Merging` false.
7. **Validation** (`validate_fork.go`).
   - `fork.each` is validated with `checkForEachSource`.
   - New rule: a fork is refused when the workflow has no writer `run` step (PROPOSED gap 4). Add a `RuleFork` problem naming the step.
   - In `validate_match.go`, a fork keeps `joined` and `conflict` required, and accepts `halted` as an optional declared outcome (it must not be rejected as an undeclared match).
8. **Delete** the "fork steps are not run by this engine" halt and the test that pins it (`next_test.go` ~line 244, and the `Fork` row in whichever test asserts it). W1's spec clarification 8 is now obsolete: the plan file notes it under "Obsolete".

**Tests** (table-driven, `internal/workflow/fork_test.go`, with the helpers from `next_test.go`):
- start of a `children` fork, an `each: plans` fork and an artifact-`each` fork: the keys, the awaiting set and the one `fork` action.
- an empty `each` list routes `joined`.
- `child_ended` order variants: A then B, B then A, duplicate, stranger.
- one child `halted` with `on.halted` wired and unwired; a `stopped` child; the first halted child's reason when two halt.
- `merge_closed` with `joined` and with `conflict`, and an unmatched result.
- `ReopenFork`: allowed case and each refusal (not halted, wrong step, `done` child).
- events and actions encode and decode.
- validation: fork without a writer, `each` with a bad source, `halted` accepted.

**Mutations** (one named per test; the named test must fail):
- Remove the `Ended`-contains check in `awaits`: fails the duplicate-`child_ended` case.
- Make `ActionMerge` fire when the first child ends: fails the two-child join case.
- Route a `halted` child to `joined` when `on.halted` is unwired: fails the unwired-halted case.
- Drop `Merging` from the `merge_closed` guard: fails the stranger/early-merge case.
- Make `ReopenFork` skip the `Status == halted` check: fails the not-halted refusal case.

**Run:** `go test ./internal/workflow/ -count=1`, then `make check`, then `sh scripts/check-comments.sh`.

**Report must include:** the new `State`/`Event`/`Action` fields, the validation rule's id, the deleted test, and the gap 1/3/4/5 choices as built.

---

=== ROUND 2: Child chain rows and start (`internal/relevo`) ===

**Goal:** the `fork` action creates the child chains under the parent's lock, each with its own branch cut from the parent writer's tip, its own members, and `parent` set. Each child's first step is sent. Nothing yet moves the parent when a child ends (round 3).

**Seams:**
- `chain_engine.go:147` `chainRunAction` (the `ActionFork` case, currently an error).
- `chain_start_wf.go:38-143` (`chainStartWorkflow`, `chainResolveWorkflowStart`, `chainWFStart`), `:303` `chainCreateWorkflow`, `:454` `chainRunStartActions`.
- `chain_members.go:26` `chainMemberNames`, `:114` `chainNameCap`.
- `chain_start.go:25` `ChainOptions`, and `chainRow`.
- `add.go:483` `cutWorktree` (its `base` accepts a ref).
- `internal/store`, `internal/db/chain.go` (`CreateChain`, `ChainDelete`, `chainDeleteChildren`).
- `workflow_resolve.go` `ResolveWorkflow` (the child's definition lookup; the `Env.Workflow` closure in `chainValidateWorkflow`).
- New files: `chain_fork.go` and `chain_fork_start.go`.

**Steps:**
1. **Lock check, then split.**
   - Read `rt.Store.WithLock` and confirm that calling it from inside `chainRunAction` is not allowed (non-reentrant). If it is reentrant, say so in the report and skip the split.
   - Otherwise split `chainCreateWorkflow` into: (a) a preparation that does git and builds bindings and the row, without the lock; (b) a persist-and-first-send that takes a `*store.Tx`. The top-level path keeps calling both in order under `WithLock`, so its behaviour does not change.
   - Done when the existing chain start tests (`chain_start_test.go`, `chain_start_wf` tests) pass unchanged.
2. **`ChainOptions.Parent`.** A new unexported-style field `Parent string`, and `ChainOptions.Params` for the child. `chainRow` writes `Parent` onto the row. The parent's `MasterMindID`, `Feature`, `Ticket` and `Server` carry over (a local parent only; a server parent is refused in step 5).
3. **`chainFlowFork`** (`chain_fork.go`). On `ActionFork`: for each `ChildSpec` in order:
   - Name `<parent>.<key>`; refuse when it exceeds `chainNameCap`.
   - Resolve the child definition with `ResolveWorkflow(rt, spec.Workflow)`; validate it with `Env{Given: ...}` as `chainValidateWorkflow` does.
   - Build the child's inputs: `Plans` as `[]byte` bodies (read from the item path or round-file key through the same reader `chainPlanBodiesFor` uses); `Task`.
   - Cut the branch from the parent's current tip: `Base` is the parent writer's branch (`c.Branch`). The tip is its newest closed round commit.
   - Create row and members through step 1's tx variant, then send the child's first step with `chainRunAction` on the child row.
   - The parent's own row (state with `Awaiting.Children`) is saved through `chainSaveFlow` with the `fork` action, so the trace shows `forked 1, 2`.
4. **All or none.** If child k fails, roll back children 1..k-1 (worktree, branch, rows, members) and halt the parent with the failure's reason through the existing member-could-not-start path (`chain_engine.go:181`). Reuse `chainRollback`.
5. **Refusals at start** (`chain_start_wf.go`, beside `chainRefuseReaderBuilder`): a workflow with a fork refuses `--server`/placed writers, with a message naming the feature as not yet supported (W4). `--dry-run` (`chain_dryrun.go`) lists each fork and the children it would create, with names, without creating anything.
6. **Child naming surface.** `chainMemberNames` already returns `<chain>`/`<chain>-<actor>` for any chain name; children reuse it with the dotted name. Add a test that `chainNameCap` counts the longest suffix for a child.

**Tests** (`chain_fork_start_test.go`; pure through `Runtime` fakes, no harness):
- a fork with two `children` creates two chain rows named `<p>.1`, `<p>.2`, with `Parent == p`, one builder member each, and branches cut from the parent's tip (assert the cut base).
- the same for `each: plans` with three items.
- a failure creating child 2 removes child 1 (row, members, worktree, branch) and halts the parent with the reason.
- a name over the cap is refused at fork time.
- a fork on a placed writer is refused at start with a message naming W4.
- `--dry-run` lists the children without writing.

**Mutations:**
- Cut the child's branch from the chain base instead of the parent tip: fails the cut-base case.
- Skip the rollback of an earlier child: fails the failure case.
- Leave `Parent` empty on the row: fails the two-children case.
- Remove the cap check: fails the over-cap case.

**Run:** `go test ./internal/relevo/ -run 'Fork|ChainStart|ChainDryRun' -count=1`, then `make check`, then `sh scripts/check-comments.sh`.

**Report must include:** whether `WithLock` was reentrant and what the split looks like; the child naming as built; the refusals.

---

=== ROUND 3: Child end, the join and the merge ===

**Goal:** a child's end becomes the parent's `child_ended`, and the parent never delivers to the MasterMind for a child. When all children have ended cleanly, relevo merges their branches into the parent's branch and the fork routes `joined` or `conflict`.

**Seams:**
- `chain_terminal_wf.go:21` `chainTerminalWF`: the one end path (finish, halt, stop; sweep reaches it through `chainSweepFlowHalt`).
- `chain_engine.go:97` `chainAdvance`, `:147` `chainRunAction`, `:345` `chainSaveFlow`.
- `chain_seed.go`: `chainRenderSeed` and `chainSeedInput`, which turn a reference into an openable path. The `{{<fork>.conflict}}` reference resolves from `Results[fork].Artifacts["conflict"]`.
- `internal/git/sync.go:67` `Merge` (to leave alone) and a new `MergeKeep` beside it; `unmergedPaths` (line 98).
- `internal/store` round-file API used by `chainCheck`'s log (a conflict file is stored the way a check's log is, as a round file under the parent chain).
- New files: `chain_fork_end.go`, `chain_fork_merge.go`.

**Steps:**
1. **`git.Client.MergeKeep`** (`internal/git/sync.go`; if it would push the file past 600 lines, put it in `internal/git/merge_keep.go`). It runs `merge --no-edit <ref>` in `dir`. On success it returns nil. On a conflict it leaves the merge in progress and returns the unmerged paths with `ErrMergeConflict`. Any other failure (no unmerged path) aborts the merge and returns the original error, like `Merge`. Done when a git test with a real temp repo shows a leftover `MERGE_HEAD` after a conflict.
2. **Child end → parent event.** In `chainTerminalWF`, when `c.Parent != ""`: skip the delivery (`delivery.Queue`) and the carrier lookup, and call `chainAdvance` on the parent row with `child_ended` (`Step` = the parent's `At`, `Child` = the key after `<parent>.`, `Status` from the child's final state: `done | halted | stopped`, `Reason`). This runs in the same tx as the child's own save. Only a top-level chain (`Parent == ""`) delivers. A child whose parent row is gone logs a warning and ends.
3. **`merge` action** (`chain_fork_merge.go`). On `ActionMerge`:
   - Take the parent writer's worktree (`chainFlowWriterMember` and the member's `CWD`) and its branch.
   - For each child in key order, `MergeKeep(parentWorktree, "relevo/<parent>.<k>")`.
   - On a clean pass, append `merge_closed{Result: joined}`.
   - On the first conflict, stop. Write the conflict file (a round file under the parent: the unmerged paths, one per line, then a `not yet merged:` section listing the branches after the stopped child), and append `merge_closed{Result: conflict, Artifacts: {"conflict": [key]}}`.
   - A non-conflict failure ends the parent with a halt reason naming the child and git's message.
   - Each result is run through `chainAdvance` with the parent row. The trace row reads `merged 1, 2 → joined` or `merged → conflict`.
4. **The conflict file is the one `{{<fork>.conflict}}` hands the builder.** Confirm `chainSeedInput` copies a sealed key out, as for a check's log. Add the reference to the `chainSeedView` artifact map that `refs.go`/`resolve.go` expect (`RefTarget{List: ...}`; see `resolve_test.go:57`).
5. **`joined` and what follows.** Nothing else runs: the workflow wires a `check`. The parent's tree holds the merge commits.
6. **Conflict wiring check.** Add an engine-level test in `internal/relevo` that the parent's `conflict` edge to a builder `run` step sends it a seed that names the conflict file path, and that the builder's tree holds `MERGE_HEAD` at that moment.

**Tests** (`chain_fork_end_test.go`, `chain_fork_merge_test.go`, `internal/git` test; fake-harness-free):
- a child's `done`, `halted` and `stopped` ends produce the matching parent `child_ended`, and queue no delivery.
- a top-level chain still queues exactly one delivery.
- a two-child fork with both done: the parent moves to `merge`, merges both and routes `joined` (real temp git repos, as `internal/git` tests do).
- the second of three children conflicts: the third is listed under `not yet merged`, the tree holds the merge, the fork routes `conflict`, and the conflict file lists the paths.
- a child halted while its sibling is still running: the parent waits, then halts with the child's reason after the sibling ends.
- a non-conflict git failure halts the parent.
- a parent row that is already terminal ignores a late child end.

**Mutations:**
- Queue a delivery for a child's end: fails the no-delivery case.
- Use `Merge` (aborting) instead of `MergeKeep`: fails the conflict-leaves-the-tree case.
- Merge in reverse child order: fails the order case.
- Stop merging after a clean first child: fails the join case.

**Run:** `go test ./internal/git/ ./internal/relevo/ -run 'Fork|Merge' -count=1`, then `make check`, then `sh scripts/check-comments.sh`.

**Report must include:** the conflict file format as built, where `MergeKeep` lives, and a statement that a top-level end still delivers exactly once.

---

=== ROUND 4: Resume, stop and sweep ===

**Goal:** resuming a halted child by name re-opens a parent that halted because of it and re-joins when that child ends. `relevo stop` on the parent stops every running child. The sweep keeps working with children.

**Seams:**
- `chain_resume_wf.go:19` `chainResumeWorkflow` and `chainResumeClosed` (line 217); `chain_resume.go`, `chain_resume_ship.go`.
- `workflow/resume.go`, `workflow.ReopenFork` (round 1).
- `chain_verbs.go:52` `ChainStop`, `:74` `chainStopWorkflow`, `:122` `chainStopWorkflowDirect`.
- `chain_sweep.go:21` `tickChains`, `:59` `chainSweep`.
- New file `chain_fork_resume.go`.

**Steps:**
1. **Resume a child.**
   - After a child's resume succeeds (its state becomes `running`), find its parent row. If the parent is `halted` and its state is on that fork (`ReopenFork` returns ok), save the reopened state (`running`, awaiting the rest) in the same tx and write a trace row `reopened by child <k>`.
   - If the parent is `halted` for a different reason, or `stopped` or `done`, refuse the child's resume with a message naming the parent and its status (PROPOSED: the human resumes the parent).
   - Done when a halted child's resume flips a halted parent back to running and the later `child_ended` produces the join.
2. **Resume the parent by name.**
   - `relevo chain --resume --name <parent>` on a parent halted at a fork with children still halted: refuse and name the halted children, so the human resumes them.
   - With every child ended `done`, it re-enters the join: set `Merging` and run the merge action.
   - Do not rewrite the fork step's children set: resume never re-forks (PROPOSED).
3. **Stop.** `ChainStop` on a chain with children, in this order:
   - Mark the parent stopped first, through `chainStopWorkflowDirect` (a fork state has no `Awaiting.Member`, so the existing direct path applies).
   - Then stop each running child, recursively for nested forks, through the same `ChainStop`.
   - A child's `child_ended` reaches an already-terminal parent and is ignored by the engine, so no join runs.
   - `ChainStop` on a child alone stops that child and lets the parent react with its `child_ended` (`stopped`).
4. **Sweep.** No change expected: each child is a chain row swept like any other, and its end flows through `chainTerminalWF`. Add a test; fix only if it fails. A parent waiting on children has members whose records may be gone: check that this halts the parent as it does today.

**Tests** (`chain_fork_resume_test.go`, `chain_fork_stop_test.go`):
- child halts, sibling finishes, parent halts with the child's reason; resuming the child to done re-opens the parent, which merges and routes `joined`.
- resume of a child whose parent is `stopped` is refused.
- resume of the parent while a child is halted is refused with the child's name.
- `ChainStop` on a parent with two running children marks all three stopped, queues exactly one delivery (the parent's), and runs no merge.
- stop on a nested fork stops grandchildren.
- sweep halts a child whose member is gone, and the parent sees `child_ended`.

**Mutations:**
- Skip `ReopenFork` after a child resume: fails the re-join case.
- Stop children first and the parent last: fails the no-merge-after-stop case.
- Let a stopped parent be re-opened by a child resume: fails the refusal case.

**Run:** `go test ./internal/relevo/ ./internal/workflow/ -run 'Fork|Resume|Stop|Sweep' -count=1`, then `make check`, then `sh scripts/check-comments.sh`.

**Report must include:** the refusals' wording, and a confirmation that one delivery is queued for a stopped fork tree.

---

=== ROUND 5: Surfaces — status, statusline, trace ===

**Goal:** `relevo status` and the statusline collapse children under the parent. `show --trace` carries the fork rows. `show <chain> --workflow` still works for a chain with a fork.

**Seams:**
- `chain_status.go:30` `chainFactsOf`, `:56` `chainFlowFacts`, `:122` `applyChains`, `:166` `ChainStatus`.
- `internal/view`: `ChainFacts`, `BindingStatus`, and the statusline renderer (read where `applyChains` output is consumed; tests sit beside the renderer's own).
- `chain_trace.go:95` `chainTraceEvent`, `:148` `flowTraceLine`, `:213` `showTrace`.
- `chain_server_view.go` (a server mirror shows children only if a server chain has a fork, which W3 refuses: do not touch it).

**Steps:**
1. **Facts.** `ChainFacts` gains `Parent string` and `Children []string` (the chain names, in key order, read from rows whose `Parent` equals the chain). `chainFlowFacts` fills the fork's progress: `fork split · 1/2 done`.
2. **Status.** In `applyChains`, a chain with a `Parent` is not a top-level row. It prints indented under its parent, each with its own `step rN` and status. A halted child shows its reason.
3. **Statusline.** The cockpit statusline collapses children: one parent row with `split 1/2`, and expands a child only when it is `halted` or NEEDS YOU, so a halted child is never hidden. Both Go and the OpenCode statusline share one projection: check where the Go statusline and the OpenCode TUI plugin read `ChainFacts`, and if the plugin is a separate implementation, list it under "not done" instead of editing it in this round (it is not buildable here).
4. **Trace.** `flowTraceLine` prints `forked 1, 2`, `child 1 done`, `merged 1, 2 → joined` and `merged → conflict (n paths)`. `show <parent> --trace` carries no child trace. A child's trace is its own: `show <parent>.1 --trace`. Add the child's parent to its trace header.
5. **Wait.** `ChainWaitTarget`/`WaitChain` (`chain_wait.go`): waiting on a child by name reports its own end line and nothing else. No change unless a test shows one is needed.

**Tests** (`chain_status_fork_test.go`, view tests, trace golden):
- a parent with two children renders them nested, in key order.
- a halted child appears in the collapsed statusline; a running child does not.
- the trace lines for a fork, a child end and a merge result.
- a chain with no fork renders exactly as before (golden unchanged).

**Mutations:**
- Print children as top-level rows: fails the nesting case.
- Collapse a halted child with the running ones: fails the halted-child case.
- Drop the `merged` row's result word: fails the trace case.

**Run:** `go test ./internal/relevo/ ./internal/view/ -run 'Fork|Status|Trace' -count=1`, then `make check`, then `sh scripts/check-comments.sh`.

**Report must include:** each surface touched, and any surface (OpenCode plugin) left alone with the reason.

---

=== ROUND 6: e2e, and the plan file ===

**Goal:** the fake-harness e2e runs a two-child fork end to end: one clean join and one conflict wired to a merge round. This round also saves this plan.

**Seams:**
- `internal/e2e/chain_test.go:65` `TestChainE2E`, `chain_triage_test.go` (a custom-workflow example), `fakes_test.go`, `headless_test.go`.
- `Makefile:74-75` (`make e2e`'s `-run` list).
- `docs/plans/` (this plan; a recent file there shows the format).

**Steps:**
1. **`TestChainForkE2E`** (`internal/e2e/chain_fork_test.go`). A workflow file with a `build` step, a `split` fork (two children running a one-step `child` workflow with the fake builder), a `check` after `joined`, a `merge` builder run on `conflict`, then done. The fake harness edits distinct files in the clean case and the same file in the conflict case. Assert, for the clean case: the parent branch holds both children's commits, the chain ends `done`, and the MasterMind got one delivery (the parent's). For the conflict case: the parent routes `conflict`, the merge round's seed names the conflict file, the builder resolves the merge, and the chain ends `done`.
2. **Add `TestChainForkE2E` to `make e2e`'s `-run` regex.** Done when `make e2e` passes with it.
3. **Existing e2es unchanged.** `TestChainE2E`, `TestChainTriageE2E`, `TestChainRemoteBuilderE2E`, `TestChainServerE2E` must pass without edits.
4. **Save this plan** as `docs/plans/2026-10-02-custom-chains-w3.md`: the whole document from the preamble to round 6 and the report block, with an "Obsolete" note that spec clarification 8 (fork halts) no longer applies.
5. **Update the spec.** Add a "Decisions made while building W3" section to `docs/specs/2026-10-01-custom-chains-design.md` (section 13): gaps 1-7 above as built, and a note that section 4.5's `plans.current-stage` example is replaced by the `each` forms in gap 1.

**Tests:** the e2e from step 1 is the test. Fake harness only; CI has no harness binary and no network, and the e2e uses the fake binary.

**Mutations:**
- Make `merge` run before the last child ends: fails the clean-join assertion.
- Skip `MergeKeep`'s leave-in-progress: the conflict case's builder cannot resolve, and fails.
- Queue a delivery for a child: fails the one-delivery assertion.

**Run:** `go test ./internal/e2e/ -run TestChainForkE2E -count=1`, then `make e2e`, then `make check`, then `sh scripts/check-comments.sh`.

**Report must include:** `git diff --stat` against the six rounds' declared scope, whether any baseline was regenerated, and every item left under "not done" across the six rounds (child clean-up, the OpenCode statusline, fork on a placed writer).

---

## Obsolete

- **Spec clarification 8 no longer applies.** It said: "Until W3, entering a
  `fork` step halts the chain with 'fork steps are not run by this engine'. W1
  parses and validates forks, and W3 adds the merge event and their execution."
  Round 1 built the step kind, so a fork is run by the engine and the halt is
  gone. The spec's section 11 keeps the sentence as history; its section 13
  supersedes it.

## Not done

Carried forward deliberately, across the six rounds:

- **Child clean-up.** A fork child's bindings and worktrees are not removed when
  its parent ends. A child ends like any chain's members do: `relevo done`, or
  `unbind`. Automatic removal is out of scope for W3 (preamble gap 7).
- **The OpenCode statusline.** Round 5 collapsed children in the Go cockpit
  statusline. The OpenCode TUI plugin is a separate implementation and is not
  buildable in this tree, so it was listed and left alone rather than edited
  blind.
- **Fork on a placed writer.** A workflow with a fork refuses a placed
  (`--server` or remote) writer at start, naming the feature as not yet
  supported. Merging into a remote tree is not designed; W4 carries it.
- **A merge round for the branches after a conflict.** The conflict report lists
  the branches the merge stopped short of, and the author's merge round has to
  merge those itself. W3 does not merge them for the author.

## Report block

Every round's report includes:
- the focused and full command outputs;
- `git diff --stat`, compared to the round's declared seams;
- every new test, by name;
- the mutation it ran itself, if any;
- every deviation from this plan, with its reason;
- any coverage-baseline line it changed.

Round 6, the last, also reports the whole of W3: the `git diff --stat` against
the six rounds' combined scope, whether any baseline was regenerated, and every
item left under "Not done" above.

A round that hits a halt condition stops and reports. It does not bend a test.
