# Plan: a server chain's member keeps a live view, and its chain row opens the member's round

Issue #834, two parts, part 1 first. Base verified at `2fa6007f` (main). The work lands as new commits on the round's branch; never amend or rebase.

## What the code says today (verified, with the seams)

- A plain remote binding's live mirror is read, without the lock, by `fetchRemote` (`internal/relevo/remotefetch.go:57-85`): `GetBinding`, then for a running round `fetchLogMirror` + `fetchDrift` (`:78-84`); the catch-up branches are `:69-77`. It is applied under the lock by `applyRemoteView` (`internal/relevo/remote_sync.go:261-375`): status word, candidate refresh, then `running` → `StalledSince`, `RemoteLive`, log mirror, drift (`:321-333`); `queued` → `RemoteQueue` (`:305-319`); `needs_you`/`closed`/`idle` → halt / `catchUp` / `applyCatchUp` + `Settle` (`:335-370`).
- Two callers feed a per-binding remote: the daemon tick (`daemon.go:288-349` `tickOne` → `prefetchRemote` `:364-381` → `reconcileRemote` `remote_sync.go:393-423`) and the read verbs (`SyncRemote` `remote_sync.go:476-546`, reachable from `relevo status`/`wait` through `SyncRemoteUnlessDaemon`). `relevo show` and the cockpit read local state only; they never sync.
- Five guard sites exist, not four: `daemon.go:368-372`, `remote_sync.go:394-399`, `remote_sync.go:494-498`, `stop.go:129-136`, and `done.go:56-79` (the last one only skips the server's `Done` call). `serverChainMember`/`serverChainMemberStore` live in `internal/relevo/chain_server.go:19-38`; the chain pull (`chain_pull.go:260-378`) is the only installer/acker and clears `RemoteLive` after an install (`:353-356`).
- Part 2: a live chain's member rows are replaced by one synthetic chain row (`internal/relevo/chain_status.go:65-87`, `:96-136`); the row's `Round` is 0 and it carries no `PlanRound`, so `paneRound` (`internal/ui/view_round.go:96-106`) returns `0-1`. The fleet's enter handler is `internal/ui/view_fleet.go:427-434` → `newRoundView` (`view_round.go:36-60`) → `pointDetailAt` (`internal/ui/round_pane.go:158-201`), where `paneRound(*r)`/`roundsOf(*r)` at `:173-174` produce "round -1 of 0". The builder member carries the chain's own name by construction (`chain_start.go:25-27`, `:378-390`; mirrored members come from the server view, `chain_pull.go:132-158`).

## The split: observe live vs collect

Enforced where fetch and apply meet, not per caller, so every entry point obeys it.

| # | Site | Today | Side after the split |
|---|------|-------|----------------------|
| 1 | `daemon.go:364-381` `prefetchRemote`, guard `:368-372` | returns nil for a member | **observe**: fetches the view (live-only); guard deleted |
| 2 | `remotefetch.go:57-85` `fetchRemote` | — | **collect**: for a member, never fetch a catch-up (`:69-77` skipped); running log/drift still fetched |
| 3 | `remote_sync.go:393-423` `reconcileRemote`, guard `:394-399` | returns the member unchanged | **observe**: applies the live-only fetch; guard deleted |
| 4 | `remote_sync.go:261-375` `applyRemoteView` | — | **collect**: an observed fetch's `needs_you`/`closed`/`idle` return before halt/`catchUp`/`applyCatchUp`/`Settle`; `running`/`queued` unchanged |
| 5 | `remote_sync.go:476-546` `SyncRemote`, skip `:494-498` | `continue`, no `GetBinding` | **observe**: fetch+apply the live view; skip deleted; `Settle` stays nil so `:523-525` is unreachable for a member |
| 6 | `stop.go:129-136` inline collect | skips the member | **collect**: unchanged |
| 7 | `done.go:56-79` server `Done` | skips the member | **collect**: unchanged |
| 8 | `chain_pull.go:260-378` | the only collector | **collect**: unchanged |

`observeRemote` (`remote_sync.go:380-387`) keeps its shape; its only unguarded caller is the stop path (#6), so no member reaches it.

### Behaviour after the change

- A member whose server view says **running**: every daemon tick and every `SyncRemote` pass writes `RemoteStatus`, `StalledSince`, `RemoteLive` (tail, usage, live diff stat, progress) and mirrors the builder log and drift through the same `fetchLogMirror`/`applyLogMirror`/`applyDrift` a plain remote binding uses. The cockpit's transcript tab and `relevo show --transcript` read exactly that log (`fetchTerminal`, `internal/ui/fetch.go:463-478`; `RoundTranscript`, `transcript.go:270-290`).
- **closed / idle / needs_you**: only the status word is written (and `RemoteLive`/`RemoteQueue` cleared); no round installed, no prompt/report entry, no halt, no ack, no settle, no delivery. The pull installs and acks on its next pass.
- Error classification (`applyRemoteErr`) is unchanged.
- Not restored, by design: `relevo show --log` reads log entries, and a server chain's open round has none client-side (the server started it; the pull writes the round's prompt entry at close, `chain_pull.go:402-419`). Writing one early would make `HasPromptEntry` skip staging the prompt file. The live view is the mirrored builder log, `RemoteLive`, and `RemoteStatus`. Report this; it is not a gap in this round's scope.
- Also unchanged: the pane's cached tabs refresh on the report row's `Last.TS`; a chain row carries no `Last`, so after part 2 the prompt/report/diff/log tabs fill once and refresh on a status re-point while the transcript tab refetches each tick. #808 owns the chain's own detail page.

### Part 2 behaviour

Entering a chain row opens the builder member's detail at the member's current round: `pointDetailAt`, when `r.Chain != nil`, loads the member binding through the pane's own source (`p.src.Runtime(key)` + `rt.Store.Load(name)`, folding the probe already at `round_pane.go:185-191`) and takes `round`/`rounds` from that binding's `Round`; a member it cannot load leaves round 0, never -1. No `view.ChainFacts` field, no JSON contract change.

## Tests (named, one thing each)

1. `TestServerChainMemberObservesTheLiveRound` (replaces `TestServerChainMemberSkipsTheBindingCatchUp`, `chain_server_start_test.go:419-454`): seed a server chain, `fakeRemote{getBindingResp: running with Live{Tail, Diff}}` and a log body on `roundFileFromResp`/`roundFileFromFunc`; drive both reads — `prefetchRemote` + `reconcileRemote` under the lock, and `SyncRemote`. Assert the stored member has `RemoteStatus == running`, `RemoteLive` non-nil with the tail, the mirrored log readable through `rt.Store.ReadFile(rt.Store.BuilderLogPath("shop", 1))`, and `GetBinding:zen:shop` in `fr.calls`.
2. `TestServerChainMemberIsNotCollectedByThePerBindingPath` (replaces `TestChainPullSkipsTheBindingCatchUp`, `chain_pull_test.go:552-576`): view `closed`, `ClosedRound >= b.Round`, catch-up files served; drive `prefetchRemote` + `reconcileRemote` and `SyncRemote`; assert the member's `Round` is unchanged, no prompt/report entry, no report file, no `Ack:` call, no pending delivery.
3. `TestFleetChainRowOpensTheBuilderMembersRound` (new, `internal/ui`, model test): store holds member `cc-w2` at `Round 3`; report holds one chain row `cc-w2` (`Chain: &view.ChainFacts{...}`, `Display: ACTIVE`, `Role: chain`); enter it (or call `newRoundView`); assert `pane.detail.name == "cc-w2"`, `detail.round == 3`, `detail.rounds == 3`.

No golden moves: no fixture under `internal/ui/testdata` holds a chain row; say so in the report.

## Mutation

Put the guard back on the live mirror: restore `if serverChainMemberStore(d.rt.Store, b.Name) { return nil }` in `prefetchRemote` (`daemon.go:368-372`). `TestServerChainMemberObservesTheLiveRound` must fail (`RemoteLive` nil, log row missing). The mirror mutation — restore the `continue` at `remote_sync.go:494-498` — must fail the same test's `SyncRemote` arm; if it does not, that arm does not really exercise the read.

## Steps

1. Add `Observe bool` to `remoteFetch` (`remotefetch.go:20-35`), set it in `fetchRemote` from `serverChainMemberStore`, and skip the two catch-up branches when it is set. Done when `go build ./...` compiles and a closed view on a member makes no file/bundle call.
2. Guard `applyRemoteView` (`remote_sync.go:261-375`) after the candidate refresh: an observed fetch's `needs_you`/`closed`/`idle` return `b, false, nil`. Done when the regression test's apply arm leaves the member untouched.
3. Delete the three guards (`daemon.go:368-372`, `remote_sync.go:394-399`, `:494-498`), reword their comments and `serverChainMember`'s doc comment to say "observes live, never collects". Done when `grep serverChainMember` shows only `stop.go`, `done.go` and its definition, and the member is fetched.
4. Write tests 1 and 2, delete the two replaced tests; `go test ./internal/relevo -run 'TestServerChainMember' -count=1`. Done when both pass.
5. Part 2: map the chain row in `pointDetailAt` (`round_pane.go:158-201`); add test 3; `go test ./internal/ui -run 'TestFleetChainRowOpensTheBuilderMembersRound' -count=1`. Done when it passes and `detail.round` is 3, not -1.
6. Run the mutation by hand, watch test 1 fail under each of the two placements, restore, re-run test 1. Done when the test fails with the guard and passes without.
7. `make check` (gofmt, vet, lint, comments, filesize, mod tidy, coverage). Done when green with no new exclusion and no baseline move; if `internal/relevo`/`internal/ui` coverage alone falls, regenerate with `sh scripts/check-coverage.sh --write` and say so.
8. `make e2e` (the chain-server scenario now also reads members per binding). Done when green without weakening an assertion.
9. Save this plan as `docs/plans/2026-10-01-server-chain-live-view.md` and commit it with the code in new commits. Done when the tree is clean and the commit(s) name both the code and the plan.

## Deleted (closed list)

1. `daemon.go:368-372`: the server-chain-member early return in `prefetchRemote`.
2. `remote_sync.go:394-399`: the member early return in `reconcileRemote`.
3. `remote_sync.go:494-498`: the member `continue` in `SyncRemote`'s loop.
4. `chain_pull_test.go:552-576` `TestChainPullSkipsTheBindingCatchUp`.
5. `chain_server_start_test.go:419-454` `TestServerChainMemberSkipsTheBindingCatchUp`.
6. The comments that pinned the deleted behaviour (`daemon.go:368-369`, `remote_sync.go:394-396`, `:494-495`, `chain_server.go:19-21`).

Nothing else: the stop and done guards stay, the chain pull is unchanged, no golden, no exclusion, no baseline.

## Halt conditions

- A step needs a lint, filesize or comment exclusion; or a baseline would be lowered: halt.
- An existing test must change its expectation other than the two named replacements; or any golden moves: halt and report.
- `make e2e` fails because the extra per-binding reads break an assertion: report what it asserted; do not weaken it.
- The split cannot be enforced in `fetchRemote`/`applyRemoteView` (e.g. no chain lookup available there): halt rather than guarding call sites.

## Report must include

- Files changed and why, with `git diff --stat` against this plan's scope.
- The guard table above, with the side each site ended on.
- Tests added/removed/deleted, the focused commands, and their results.
- The mutation runs: each placement, the test that failed, and that both were restored.
- `make check` and `make e2e` summaries.
- Goldens: none moved (or which one, why).
- Coverage baseline: unchanged (or regenerated, for which package, why).
- What stays by decision: stop/done guards, open-round `--log` entries, the diff tab before close, cached tab invalidation on a chain row.
- Deviations from the plan, and the commit(s) including the plan file.

## MasterMind amendment (overrides step 7)

Never run `sh scripts/check-coverage.sh --write`. A wholesale regeneration on one machine raises every untouched package to that machine's numbers, and a slower machine then fails. If `internal/relevo` or `internal/ui` coverage falls more than one point, add tests until it does not. Never touch `testdata/coverage-baseline.txt` in this round.
