# Plan: #1249 -- chain `--resume --reviewer-actor` swaps the actor, resume is atomic, `stop` frees a wedged chain

Branch from `main` (4114e73c). New commits only. One PR, three fixes.

## Root causes (verified in the code)

1. **Why the swap fails.** `chainMemberNames` (`internal/relevo/chain_members.go:25`) names a member by its *part*, not its actor: on the shipped default workflow the reviewer is always `<chain>-rev`. `chainResumeAddMembers` (`chain_resume_wf.go:386`) decides what is new with `have[m.name]`. After `--reviewer-actor reviewer` the planned reviewer member is again `<chain>-rev`, which exists, so nothing is added and nothing is rebound. The `chain_member` row still says actor `live-reviewer`. Later `chainFlowMemberName(tx, c, "reviewer")` (`chain_engine.go:91`, via `chainFlowSend` at :208) finds no member and returns `chain X has no member for actor "reviewer"`. A custom workflow whose member name embeds the actor (`<chain>-<actor>`) already works through the add path; only a same-name different-actor collision is broken.
2. **Why the chain is wedged.** `Store.WithLock` (`internal/store/store.go:313`) is a file lock, not a DB transaction. In `chainResumeWorkflow` the closure at `chain_resume_wf.go:102-194` writes the row as `running` (`ChainPut` at :164, status from :156) and only then runs the send actions (:172). The send fails with the error above, the closure returns, and nothing is rolled back. The row is `running`, its state awaits the new actor with no member, and the stale end delivery was already confirmed by `supersedeChainDelivery` (:97, before the lock).
3. **Why `stop` refuses.** `chainStopWorkflow` (`chain_verbs.go:128`) calls `chainStoredMemberName` (:158), which errors when no member matches the awaited actor. The error returns before `chainStopWorkflowDirect` (:176) can run. `done`, `unbind --archive` and `resume` all refuse a `running` chain by design, so `stop` is the only exit.
4. **Why `wait` says `internal: context canceled`.** `WaitChain` and `chainServerWait` (`chain_wait.go:73`, `chain_server_view.go:109`) correctly poll forever on a chain that stays `running` and return `ctx.Err()` when the caller's context ends (timeout, SIGINT, or the tool cancelling). `waitFailure` (`cmd/relevo/wait.go:~163`) maps everything except `ErrNotFound` to `codeInternal`, so a cancelled wait reads as an internal fault. The wedge is the cause; the mapping is a small local defect on the same path.

## Decisions

**D1. Rebind the existing member in place; do not replace it.** The member keeps its binding name, its `chain_member` row (same `Seq`), its chain-row column (`Reviewer` etc.), its log and round counter, its worktree, and (served) its `Owner`, `Serve`, `Repo`, `Link`, `Base`, `CWD`. What changes is the actor and what the actor resolves to.

Why not replace. A new binding on the same name would restart at `Round: 1` over a log that already holds that name's earlier rounds, and `memberNewestClosedRound` / `chainResumeClosed` would read those old rounds as new. Replacing under a new name would break the part-to-name convention (`-rev`) that terminal deliveries and `relevo status` rely on. The name collision is precisely why the swap is needed, so rebinding is the model that fits.

Fields rewritten on the existing `store.Binding`:
- `Role` (`normRole(newActor)`) and `Shape`.
- `Builder` endpoint, `BuilderCandidate`, `BuilderAccount`, `Tier`.
- The old session is abandoned first (`abandonSession`, `abandon.go:96`), so the new candidate never resumes the old harness session.
- `RoundSwitches` resets to 0.
- The `chain_member.Actor` becomes `newRole`.

**D1a. Local vs served.**
- Local: reuse `chainResolveActors` (`chain_start.go:308`, which checks shape and candidate gating) and `chainBuildMembers` (:333) to produce a fresh binding per swap member. Overlay only the fields above onto the stored binding.
- Served: use `servedActorPick` (`chain_served.go:327`) and take `pick.kind`/`pick.token`/`pick.tier` the same way, rewriting `Builder.Kind` and `BuilderCandidate`. Keep the existing `Link` as is. The resume wire carries no client ids, and the member name, which is what the client mirror matches on, is unchanged.
- Remote (`--server`): the client only forwards (`chainServerResume`, `chain_server_view.go:187`; `ReviewerActor` and `Params["reviewer"]` already travel). The server's `internal/serve/chains_resume.go` runs the same `ChainResume`, so the fix lands there. Server placement survives because the binding is mutated, not rebuilt: `Owner`, `Serve`, `Worktree` and `Link` are untouched. The server needs the redeploy; the client mirror refreshes on its next pull.

**D1b. Refusals first.** The swap is refused before any write when:
- the member's round is open (reuse `resumeOpenRoundRefusal` semantics and return `*RoundOpenError`);
- the new actor's shape differs from the member's shape (`chainResolveActors` already errors);
- no ungated candidate resolves (`resolveRole` error).

All resolution (actors, candidates, `resolveBuilder`) is done read-only before the first write. `--planner-actor` and `--security-actor` flow through the same code, since they are the same `chainMember`/part machinery. An edge to document, not fix: when two parts name the same actor, `chainDefaultParts` gives the name to the first part (reviewer wins).

**D2. Atomic resume = prepare first, snapshot, restore on failure.** Flipping `running` last is not possible: `chainRunAction` needs the new members and engine state in place, and `chainSaveFlow` writes the row itself. So:
1. Everything that can fail without writing runs before the first write: swap resolution, the open-round refusal, and `workflow.Resume`.
2. At the top of the lock closure, snapshot: the chain row, `tx.ChainMembers`, and each binding about to be rebound.
3. Any error returned after the first write restores them: `ChainPut(orig)`, `ChainMembersPut(origRows)`, `tx.Save(origBinding)` for swapped members, and `tx.Delete(name)` for bindings the failed attempt added (`store/lifecycle.go:146`). The restore is best effort and joins its own error to the original instead of masking it.
4. A send that cannot start already halts the chain with `member X could not start` (`chainFlowSendText`). That is a legitimate new halt reason, not a failure, so it is unchanged and the swap stays applied.
5. `supersedeChainDelivery` (`chain_verbs.go:382`) currently confirms the halt notice before the lock. Move that work into the closure. Collect the pending `KindChain` indices (`PendingForMasterMindThrough`) first, and confirm exactly those (`ConfirmIndex`) only at the end of a successful closure. Then a failed resume leaves the halt notice pending and a new end delivery queued inside the resume is never superseded. `chainServerResume` keeps its own call, which is the client mirror's concern.
6. Pre-lock writes outside the closure are not undone and are not part of the resumed state: `closeDeadMemberRound` records a round that really did die. Say so in the report.

**D3. Stop on a wedged chain.** In `chainStopWorkflow`, when `chainStoredMemberName` finds no member for the awaited actor, or the member binding is gone (`store.ErrNotFound` from `Stop`), take the direct path (`chainStopWorkflowDirect`) and return `ChainStopActionStopped`, exactly as the existing `ErrNothingToStop` arm does. Other errors still propagate. `chainStopForkChildren` continues to run afterwards. The server's chain stop uses the same function, so served chains need the same deploy.

**D4. `wait`.** In `waitFailure` (`cmd/relevo/wait.go`), map `context.Canceled`/`context.DeadlineExceeded` out of `codeInternal`. Use an existing non-internal code if `cmd/relevo` has an interrupted/timeout code, and read `fail`/`code*` in `cmd/relevo` to pick it. If none fits cleanly, leave the exit code alone and only reword the message to say the wait was cancelled; do not invent a code. Test it as a pure function on `waitFailure`. Do not touch the polling loops: with D1 to D3 the chain no longer stays `running` forever.

## Ordered steps (each ends green with `go test ./internal/relevo -run '<its tests>' -count=1`)

1. **Failing tests first** in `internal/relevo/chain_resume_swap_test.go` (new). Build on `chainRuntime`, `startFlowChain`, `stoppedChain`, `chainStoredRow`, `chainBinding`, and `servedChainRuntime`/`servedChainStart` from `chain_served_test.go`. Tests:
   - `TestChainResumeSwapsTheReviewerActor`: halt a default-workflow chain on review, resume with `ReviewerActor:"<other configured reader actor>"`. Assert the `<chain>-rev` binding keeps its name, `Round` and log, has `Role`/`BuilderCandidate` of the new actor's candidate and no `StreamSessionID`, `chain_member` row `Actor` is the new role and keeps its `Seq`, the chain is `running`, the new round is open on that member, and `SettingsJSON.ReviewerActor` is the new actor.
   - `TestChainResumeSwapsPlannerActor`: the same for `PlannerActor`, a table case beside the reviewer one.
   - `TestChainResumeSwapRefusesAnOpenMemberRound`: the swap leaves the member and the chain untouched and returns `*RoundOpenError`.
   - `TestChainResumeSwapRefusesWrongShape`: a writer actor given as `ReviewerActor` is refused before any write.
   - `TestServedChainResumeSwapKeepsPlacement`: a served chain halted on review, resumed with a different reviewer actor. Assert the binding's `Owner`, `Serve`, `Link`, `CWD` and `Repo` are unchanged and the candidate and `Builder.Kind` are the server's pick for the new actor.
   - `TestChainResumeFailureLeavesTheChainHalted`: force a post-write failure (see note below) and assert status `halted`, the original `Reason`, `StateJSON`, `WorkflowJSON`, `SettingsJSON`, the original `chain_member` rows and the original binding all byte-equal to the pre-resume snapshot; the stale halt delivery is still pending.
   - `TestChainResumeFailureRemovesMembersItAdded`: use `flowBranchWorkflow` (`chain_resume_wf_test.go:15`) with `Params{"scan":"true"}` so the researcher member is added, then force the same failure and assert `shop-researcher` and its `chain_member` row are gone.
   - `TestChainStopWithNoAwaitedMemberStopsDirectly`: a `running` chain whose `chain_member` row for the awaited actor was removed (and a second case: row present, binding deleted). `ChainStop` returns `ChainStopActionStopped`, the chain is `stopped`, one trace row, and `ChainDone` then succeeds.
   - A pure test of `waitFailure` in `cmd/relevo` for `context.Canceled`. **CI runners have no harness binary and no network: this test calls `waitFailure` directly and executes no subcommand.**
   
   Note on forcing the failure: inject it through an existing seam. Read `chain_resume_test.go` (`TestChainResumeHaltsWhenTheSendFails` plants a symlink at the prompt path) and look for a fault that makes `chainRenderSeed`, `tx.Load` of the member, or the final `ChainPut` return an error rather than halting, for example a seed template that fails to render or a member binding file made unreadable. A send that fails to start is a halt, not an error (D2.4), so do not use that one. If no seam reaches an error return after the first write, add the smallest test-only hook at the closure's seam and say so in the report. Do not weaken the assertion.
   Deliverable: the new tests, all failing for the stated reasons.
2. **Swap** in `internal/relevo/chain_resume_swap.go` (new). It holds the pure split of planned members into add and swap sets, the read-only resolution, and the local and served overlay (D1a, D1b). `chainResumeAddMembers` (`chain_resume_wf.go:386-456`) calls it. The swap-set is members whose name is in `have` but whose stored actor differs from `m.actor` via `chainFlowActorName`. Functions at most 70 lines; keep `chain_resume_wf.go` under 600 (it is 474, so move the add/served helper there into the new file if it grows). Success signal: the swap tests pass.
3. **Atomic resume.** Move the closure at `chain_resume_wf.go:102-194` into a named function (`chainResumeLocked`), since `chainResumeWorkflow` is already 197 lines and must not grow. Put the snapshot and restore in `internal/relevo/chain_resume_undo.go` (new). Move the delivery supersession into the closure as in D2.5, and drop the call at :97. Success signal: the atomicity tests pass, and `TestChainResumeHaltsWhenTheSendFails`, `TestChainResumeSupersedesTheStaleEndDelivery` and the other `chain_resume_*_test.go` tests still pass unchanged.
4. **Stop.** Edit `chainStopWorkflow` in `chain_verbs.go:128-154` per D3. Keep the "no <actor> member to stop" error text only for non-stop callers, if any (`grep chainStoredMemberName`), otherwise delete the unused error. Success signal: the stop tests pass, and the existing `chain_verbs_test.go` stop tests are unchanged.
5. **wait mapping** in `cmd/relevo/wait.go` per D4.
6. **Full check.** Run `go vet ./...` first, then `make check` once at the end. Do not run `make e2e`. It is not part of `make check`, but it is run by CI, and these changes should not affect it.
7. **Mutation pass** (each mutation must make a named test fail, then be reverted):
   - M1. In the swap classifier, drop the `chainFlowActorName(stored) != m.actor` condition so a same-name member is never swapped: `TestChainResumeSwapsTheReviewerActor` and the planner and served cases fail.
   - M2. Skip `abandonSession` or the `StreamSessionID` reset: the swap test's session assertion fails.
   - M3. Make the served overlay rebuild the binding with `servedChainMember` (reset `Round`/`Link`): `TestServedChainResumeKeepsPlacement` fails.
   - M4. Remove the restore call: both atomicity tests fail.
   - M5. Remove `tx.Delete` for added members from the restore: `TestChainResumeFailureRemovesMembersItAdded` fails.
   - M6. Confirm the superseded deliveries at the start of the closure instead of the end: the stale-delivery assertion in the failure test fails.
   - M7. Remove the missing-member arm in `chainStopWorkflow`: both stop cases fail. Remove only the `store.ErrNotFound` arm: the deleted-binding case fails.
   - M8. Revert `waitFailure`: its test fails.
8. **Save this plan verbatim** as `docs/plans/2026-10-17-chain-resume-swap-actor.md` (last step, after everything is green) and commit it in the same PR as the code, as new commits. Never amend or rebase a pushed commit.

## Files touched (declared scope)

- `internal/relevo/chain_resume_wf.go` (extract the locked closure; call the swap; move supersession)
- `internal/relevo/chain_resume_swap.go` (new)
- `internal/relevo/chain_resume_undo.go` (new)
- `internal/relevo/chain_verbs.go` (`chainStopWorkflow`; supersession helper reused or reshaped)
- `cmd/relevo/wait.go` (`waitFailure`)
- `internal/relevo/chain_resume_swap_test.go` (new)
- `cmd/relevo/wait_test.go` (or the nearest existing wait test file)
- `docs/plans/2026-10-17-chain-resume-swap-actor.md` (new)

Nothing is deleted. Do not touch `internal/serve`, `internal/remote`, `chain_engine.go`, `chain_members.go`, the workflow package or any exclusion list; `chain_engine.go` (502 lines) stays as is. No new entries in `scripts/check-filesize.allow`, `scripts/check-comments.allow` or `.golangci.yml`. If a function genuinely cannot stay under 70 lines without a split, split it.

## Code-style reminders

- Comments say why only. No issue or PR numbers, no history words.
- Test names say what they pin.
- Package coverage must not drop. If code moves between packages, run `sh scripts/check-coverage.sh --write` and say so.

## The report must include

- The D1 choice (rebind in place), with the reason, and confirmation that served `Owner`/`Serve`/`Link`/`Worktree` are unchanged.
- Which seam forced the post-write failure in the atomicity tests, and whether a test-only hook was added.
- Which pre-lock writes are not undone (`closeDeadMemberRound`).
- The `waitFailure` outcome: the code chosen, or why only the message changed.
- The mutation results: M1 to M8, each with the test that failed.
- `git diff --stat` against the declared scope above.
- `make check` output (last lines).
- A reminder that served-chain fixes reach a server only after it is redeployed, and that a chain already wedged on a server is freed by running `relevo stop <chain>` after that deploy.
- Anything in the plan that contradicted the code, as a halt and not a workaround.
