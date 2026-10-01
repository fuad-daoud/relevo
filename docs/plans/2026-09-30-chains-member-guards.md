# Plan: chain member guards — `done`/`unbind` refuse a running chain's member, and the guide teaches chains

One-shot builder round on `origin/main` (`797ebda1`, the chains slice 1 merge, PR #767). One commit, never amend, never rebase, never push. `make check` green on its own.

Two gaps left by slice 1:

1. `relevo.Send` refuses a member of a running chain (`ErrRunningChainMember`, `internal/relevo/chain.go:19-61`), but `relevo.Done` (`internal/relevo/done.go:27-142`) and `relevo.Unbind` (`internal/relevo/bind.go:902-977`) do not. So the MCP `done` tool (`internal/mcp/verbs.go:161-168`), `relevo done <member>` and `relevo unbind <member>` end a member's round under a live chain. A DONE member is neither gone nor NEEDS YOU, and `chainSweep` (`internal/relevo/chain_sweep.go:64-80`) halts on exactly those two, so the chain waits forever for a close that can no longer come. An unbound member does halt the chain (`member <name> gone`) but unresumably: the record a resume needs is gone.
2. The injected guide (`internal/mastermind/guide.md`) teaches only the manual loop; a MasterMind is never told chains exist.

## Behaviour

**A. `Done` refuses a running chain's member.** In `Done`, inside the existing `rt.Store.WithLock`, immediately after the load block (`internal/relevo/done.go:35-38`) and before the queued check (`:43`) and the remote call (`:50`), call `refuseRunningChainMember(tx, name)` — the same helper the in-lock send path uses (`internal/relevo/send.go:465`), so the sentinel and the wording are the ones `Refuse*` already owns: `ErrRunningChainMember`, `binding %q belongs to running chain %s; relevo stop %s first`, mapped to `codeConflict` by `cmd/relevo/args.go:262` (no new sentinel, no `args.go` edit). Checked in-lock, in the same critical section as the state write, exactly as `Send` does.

**B. `Unbind` refuses a running chain's member.** In `Unbind`, immediately after `rt.Store.Load(name)` (`internal/relevo/bind.go:903-906`) and before the remote server call (`:913`), `stopProcess` (`:933`), the teardown (`:959`) and the archive/delete (`:965-974`), call `refuseRunningChainMemberStore(rt.Store, name)`. Read-only, because `Unbind` has no critical section and a late refusal would stop a process and then keep the record — the refusal must precede every side effect. Same sentinel, same wording, same conflict mapping; `Unbind` keeps returning `UnbindResult{}` with the error, as it does for every other refusal.

**C. Terminal chains stay free.** Both refusals key on `chain.Status == running` only (`runningChainRefusal`, `internal/relevo/chain.go:56-61`): a halted, stopped or done chain's members keep working with `done`/`unbind`. `ChainDone` (`internal/relevo/chain_verbs.go:113-155`) refuses a running chain first and runs only after that, so it still releases every member — its per-member `Done` now passes the same guard, which also means a chain resumed mid-release makes `ChainDone` fail retryably instead of releasing a live chain's members. The CLI's chain arm (`cmd/relevo/done.go:108-112`) and `ErrChainRunning` are unchanged.

**D. `stop` on a member stays allowed.** No production change. A member's stop closes the round through `closeStopped` → `queueReport(..., stopped=true)` (`internal/relevo/stop.go:174,264`) and raises the chain's `stopped` event in the same critical section, which is the spec's "a stopped member stops the chain". A pin test records it; `internal/relevo/chain_resume_test.go:117-126` already relies on it as a helper.

**E. MCP `done` on a chain name routes to `ChainDone`.** Decision: route. `relevo status` and so the MCP `status` tool show one synthetic row named after the chain (`internal/relevo/chain_status.go:54-76`), and the CLI resolves that name to the chain for `done`; leaving the tool to release only the builder would make one name mean two things. In `RelevoVerbs.Done` (`internal/mcp/verbs.go:161-168`): when `v.RT.Store != nil` and `v.RT.Store.Chain(a.Name)` resolves, call `relevo.ChainDone` and return the same `doneResult{DoneResult, Text: relevo.DoneText(a.Name, res)}` — the tool's JSON shape and the `done` schema are unchanged; a name that is no chain, or a store that cannot answer, falls through to `relevo.Done` exactly as today. `internal/mcp/tools.go:134` gains one clause naming the chain case (its `Calls relevo.Done.` becomes wrong otherwise); the three preludes in `internal/mcp/instructions.go` are not touched (they do not contradict the new behaviour).

**F. The guide gains a chains section.** `internal/mastermind/guide.md`, a new `## Chains` between "The recommended loop" and "When something is stuck" (insert at `:40-42`), at most 8 lines, four bullets, the guide's own voice (short, no issue numbers, no history):

- when: several reviewed plans in a row with no MasterMind turn between rounds;
- start: `relevo chain --name <n> --plan r1.md --plan r2.md --feature <label> [--security]`; the plans are written first by a planner actor and reviewed, as in the loop above;
- waiting: `relevo wait --name <n>` returns once — 0 the chain finished, 3 it halted or was stopped; then `relevo show <n> --trace`, and `relevo chain --resume --name <n>` after a halt;
- the rule: never drive a running chain's members by hand — `send`, `done` and `unbind` on a member are refused until the chain is stopped.

## Scope

Change:

- `internal/relevo/done.go` — the guard (A).
- `internal/relevo/bind.go` — the guard (B).
- `internal/mcp/verbs.go` — the route (E); `internal/mcp/tools.go:132-138` — the description.
- `internal/mastermind/guide.md` — the section (F).
- `internal/relevo/chain_verbs_test.go` — the verb tests below (sits with the `ChainDone`/`ChainStop` tests at `:148-263`); reuse `chainRuntime`/`startedChain` (`chain_start_test.go:47,59`), `stoppedChain` (`chain_resume_test.go:117`), `chainBuilderClose`, `chainHaltedBody`, `chainStoredRow`, `chainTrace`, `chainPendingChain` (`chain_test.go:45-90`).
- `internal/mcp/verbs_test.go` — the routing/refusal tests, beside the existing `Done` tests at `:221-261` (seeding a chain row uses `tx.ChainPut`, as `cmd/relevo/chain_test.go:181-191` does).
- `internal/mastermind/handoff_test.go` — the new guide pin, beside `TestGuideNamesTheLabelFlags` (`:29-42`).
- `cmd/relevo/chain_test.go` — the CLI refusal pins, using `seedCLIChain` (`:161-196`), store-only.
- `internal/mcp/testdata/contract/instructions.golden` (guide copy) and `tools-list.golden:64` (the `done` description) — regenerated.
- `docs/plans/2026-09-30-chains-member-guards.md` — this plan, last step.

Do not touch: `internal/relevo/chain.go` (the helpers already say it), `cmd/relevo/args.go`, `cmd/relevo/done.go`, `internal/relevo/stop.go`, `internal/relevo/chain_verbs.go`, `internal/serve`, `internal/pick`, `internal/ui`, `internal/chain`, `internal/db`, `internal/store`, the coverage baseline, and the lint/comments/filesize allow-lists.

Known, deliberate non-goals (say so in the report, do not fix here): `relevo done --pick` and the cockpit call `relevo.Done` directly, so a chain row picked there releases only the builder — the interactive surfaces were not taught chains in slice 1; the served `POST /bindings/{name}/done` arm and `/v1/chains/*` are slice 3; `relevo unbind <chain>` has no chain arm and now refuses only while the chain runs.

## Ordered steps

1. A + B: the two guards, with a comment each saying why a running chain owns its member (`done` leaves the chain waiting; `unbind` steals the record its next send needs) — check: `go test ./internal/relevo -run 'TestDoneRefusedOnARunningChainMember|TestUnbindRefusedOnARunningChainMember' -count=1`.
2. The freedom half: terminal chains, and the `stop` pin — check: `go test ./internal/relevo -run 'TestDoneAndUnbindAllowedAfterTheChainStops|TestStopOnARunningChainMemberStaysAllowed|TestChainDoneReleasesEveryMemberAndMarksDone' -count=1`.
3. E: the MCP route and the description clause + both mcp tests — check: `go test ./internal/mcp -run 'TestRelevoVerbsDone' -count=1`.
4. F: the guide section + `TestGuideNamesTheChainsCommand`; then regenerate `go test ./internal/mcp -run 'TestContract' -update -count=1` and `git diff --stat internal/mcp/testdata/contract/` — the diff must be exactly one new `## Chains` block in `instructions.golden` and one description line in `tools-list.golden`; any other hunk is reverted and reported — check: `go test ./internal/mastermind ./internal/mcp -run 'TestContract|TestGuide|TestHook' -count=1`.
5. The CLI refusal pins — check: `go test ./cmd/relevo -run 'TestChain' -count=1` (store-only: `seedCLIChain` seeds the chain row and its members; no harness, no network, no git — CI has neither, CLAUDE.md).
6. The mutation checks below, one at a time, each reverted after its named test fails — check: the named test fails, then the same test passes again on the restored line (`git diff` empty).
7. `make check` from the committed tree, exit 0; write this text to `docs/plans/2026-09-30-chains-member-guards.md` and commit code + docs + plan as one new commit. Do not push.

Focused command for steps 1-5: the `-run` commands above. Full check: `make check`.

## Tests, by name (what each pins)

- `TestDoneRefusedOnARunningChainMember` (`internal/relevo/chain_verbs_test.go`): `Done` on the chain's builder (`shop`) and on its reviewer member (`shop-rev`) returns `ErrRunningChainMember`, the message names the chain and `relevo stop shop first`, the member stays active with its round open, and no stop entry was appended.
- `TestUnbindRefusedOnARunningChainMember`: `Unbind` with archive false and true on `shop-rev` returns `ErrRunningChainMember`; the member record still loads with its state unchanged and no log entry was added; the chain row is untouched.
- `TestDoneAndUnbindAllowedAfterTheChainStops`: subtests `stopped`, `halted`, `done` — after the chain left `running`, `Done("shop-rev")` and `Unbind("shop-rev", false)` both succeed and the member's record reflects it.
- `TestStopOnARunningChainMemberStaysAllowed`: `Stop(ctx, rt, "shop", StopOptions{})` on the running chain's awaited builder is not refused; the chain reads `stopped` with one `stopped` trace row and one end delivery. Fails if anyone later puts the guard on `Stop`.
- `TestChainDoneReleasesEveryMemberAndMarksDone` (existing, unchanged, `:168-224`): the guard must not bite `ChainDone` — every member ends `StateDone` and the chain ends `done`/`finished`.
- `TestRelevoVerbsDoneOnAChainReleasesEveryMember` (`internal/mcp/verbs_test.go`): the tool's `done` on a stopped chain's name marks every member done and returns `doneResult` with non-empty `Text`.
- `TestRelevoVerbsDoneRefusedOnARunningChain`: the tool's `done` on a running chain's name errors naming `relevo stop <n> first`; on `shop-rev` it errors with the running-chain refusal; nothing is marked done.
- `TestChainMemberDoneAndUnbindRefusedWhileRunning` (`cmd/relevo/chain_test.go`): `done climembers-rev`, `unbind climembers-rev` and `unbind climembers` each exit with `codeConflict` and a message naming the chain and `relevo stop climembers first`.
- `TestChainMemberDoneAndUnbindAllowedAfterTheChainStops`: with the fixture seeded `stopped`, `unbind climembers-rev` and `done climembers-rev` exit 0 (the seeded member has no worktree and no PID, so no git and no harness run).
- `TestGuideNamesTheChainsCommand` (`internal/mastermind/handoff_test.go`): the guide names `## Chains`, `relevo chain --name`, `--plan`, `relevo wait --name`, `relevo show <n> --trace`, `relevo chain --resume --name`, and the rule "Never drive a running chain's members by hand".
- `TestContractInstructions` / `TestContractToolsList` (`internal/mcp/contract_test.go:49-86`): the two regenerated goldens. `TestGuideNamesTheLabelFlags`, `TestHookOutputCarriesTheGuide`, `TestChainDoneRefusedWhileRunning` and `TestChainDoneOnARunningChainIsAConflict` must stay green unedited.

## Mutation checks (each names the test that must fail)

- Drop the `refuseRunningChainMember` call in `Done` → `TestDoneRefusedOnARunningChainMember` fails (and `TestRelevoVerbsDoneRefusedOnARunningChain`).
- Drop the `refuseRunningChainMemberStore` call in `Unbind` → `TestUnbindRefusedOnARunningChainMember` fails.
- Make `runningChainRefusal` refuse any chain member regardless of status → `TestChainDoneReleasesEveryMemberAndMarksDone` and `TestDoneAndUnbindAllowedAfterTheChainStops` fail.
- Make `RelevoVerbs.Done` call `relevo.Done` unconditionally → `TestRelevoVerbsDoneOnAChainReleasesEveryMember` fails.
- Add the guard to `Stop` (the change this round forbids) → `TestStopOnARunningChainMemberStaysAllowed` fails.
- Edit `guide.md` without regenerating → `TestContractInstructions` fails; delete the section → `TestGuideNamesTheChainsCommand` fails.

## Halt conditions

- If any path other than a human or a tool call reaches `relevo.Done`/`relevo.Unbind` for a running chain's member (the daemon's reconcile, GC, the sweep, the chain wiring), halt and report the caller: the guard must never wedge an automatic path. Today the callers are `cmd/relevo/done.go:113`, `cmd/relevo/unbind.go:96`, `internal/mcp/verbs.go:163`, `internal/pick/model.go:83,89`, `internal/ui/actions.go:138,156`, the served handlers and `ChainDone` itself.
- If the `Unbind` refusal cannot sit after `Load` and before the first side effect, halt; a refusal after `stopProcess` or the teardown leaves a half-unbound binding.
- If the MCP route cannot be confined to `RelevoVerbs.Done` — a `Verbs` interface change, a `done` schema change, or a new JSON shape — halt and report; that is a different PR.
- If `ChainDone` cannot release every member with the guard in place (the refusal must key on status, not on membership), halt and report before touching `chain.go`.
- If regenerating the mcp goldens produces any hunk beyond the two described, revert it and halt, naming the hunk.
- If the CLI refusals need a harness or a git repo to test, halt and report; CI has neither, and `seedCLIChain` is the store-only fixture.

## Repo rules carried

- `make check` green; the coverage baseline is never lowered and no allow-list gains an entry — the new lines are covered by the tests above; `internal/relevo/bind.go` is already in `check-filesize.allow` and keeps its entry.
- New comments say why and carry no `#NNN`, no `§`, no round numbers; test names say what they pin.
- The plan is committed with the implementation; no amend, no rebase, no push.

## What is deleted when the work deletes behaviour

Deleted behaviour: none. Closed list:

1. None. No function, branch, flag, test or golden is removed; the two regenerated goldens are rewritten in place, and every existing pin named above stays unedited and green.

## The report must include

- `git diff --stat` and the commit hash.
- The two golden diffs verbatim, and one line that nothing else in `internal/mcp/testdata/contract/` moved.
- The focused test output for `internal/relevo`, `internal/mcp`, `internal/mastermind` and `cmd/relevo`, then the full `make check` output with exit 0.
- Each mutation check as: the line mutated, the named test, its failure line, and the reverted state.
- One line confirming `TestStopOnARunningChainMemberStaysAllowed` is green with `stop.go` untouched.
- One line on the surfaces left alone (`done --pick`, the cockpit, the served done arm) and one line confirming that `grep -rl "The relevo guide" .` still finds only `internal/mastermind/guide.md` and `internal/mcp/testdata/contract/instructions.golden` (no plugin or agent file is a copy of the guide).
