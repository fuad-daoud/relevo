# Plan — a rewritten-history remote round must still close (#746)

## What the code actually says (this shapes the whole plan)

1. **The signal is exactly one thing.** The round-bundle route rejects a stale base only in `internal/serve/roundfiles.go:189-197`: `Snapshot` returning `remote.ErrSinceUnknown` becomes `422` + `remote.CodeNotFastForward` ("since is not an ancestor of the result"). `Snapshot(since="")` can never produce it (`internal/git/bundle.go:108-127`; `internal/remote/transport.go:19-22`). The client sees it as `*client.HTTPError{Status: 422, Body.Code: "not_fast_forward"}` (`internal/remote/client/client.go:193-208`); `retry` does not retry it (`client.go:248-259`).

2. **A bare "retry once with an empty `since`" is not enough, and I verified it with git.** The rewritten round means the server's `refs/heads/relevo/<name>` no longer descends from the client's copy of the same ref, and the client keeps that ref between rounds (`remote_add.go:294` creates it; `remotefetch.go:503-548` only ever fast-forwards it; `refclean.go:47-57` keeps it while the binding is live). `Absorb` is **fast-forward-only by contract** (`internal/remote/transport.go:24-30`) and `FetchBundle` hardcodes a non-forced refspec (`internal/git/bundle.go:197-205`). So the full bundle is rejected `non-fast-forward` and the round *still* does not close — it just moves from the 422 fetch failure to an absorb failure (`internal/relevo/remote_catchup.go:136-145`, counted to a halt at 10). Verified with plain git in `/tmp/opencode/ffy`: incremental `merge-base --is-ancestor since ref` fails; the full-bundle fetch into the client's existing `refs/heads/relevo/api` is `! [rejected] ... (non-fast-forward)`; deleting that ref first makes the same fetch a `[new branch]` success. **A full retry must therefore also re-base the client's own mirror ref** — this is the answer to the seed's "should a successful full retry update anything else".

3. **Fix 2 as stated is unsafe and I will not plan it.** Closing the round with the report while the bundle/absorb failed:
   - contradicts the code that withholds it on purpose (`remote_catchup.go:132-150` returns `stop=true`, so `applyCatchUp` never builds the `catchUpAck`; `remote_catchup.go:58-64` acks before any report; spec `docs/specs/2026-09-19-remote-builders-design.md:315` "ack is sent only after both fetches succeeded");
   - contradicts a pinned test: `internal/relevo/remote_test.go:4628-4654` (`TestCatchUpOrderAndIdempotence`) asserts no `Ack`, no `KindReport` entry, and `LastKnown` unchanged after a failed absorb — and the seed's own rule is not to bend tests;
   - poisons the *next* round: `applyCatchUp:580` would set `LastKnown = view.ResultCommit`, a commit the client never absorbed, so the next incremental bundle needs base objects the client does not have → permanent absorb failures; leaving `LastKnown` alone instead makes every later round re-fetch a full bundle that is still non-fast-forward.
   The "round already arrived" case the seed hopes to reach is **already handled** by `absorbHoldsRound` / `clearAbsorbHalt` (`remote_catchup.go:152-188`, pinned at `remote_test.go:5864-5931`). Fix 2 goes to the report as a deliberate non-goal with this evidence.

## Scope

One PR: **make a remote round whose server branch was rewritten close on the client.** Fix 1 (full-bundle fallback) plus the mirror re-base it needs. Touches `internal/relevo` only; no change to the server, the transport contract, or the absorb-failure ordering.

## Decisions (the seed's open questions)

- **Which errors warrant the full retry: only the stale-base signal** — `*client.HTTPError` with `Status == 422` and `Body.Code == remote.CodeNotFastForward`, and only when `b.Builder.LastKnown != ""`. Reason: the retry has a side effect (re-basing the client's own ref), so firing it on any error would delete a good mirror on, say, a 500. Unreachable is already retried inside the client; every other error stays hard exactly as today.
- **What a successful full retry must also do:** re-base the client's own mirror ref (`refs/heads/relevo/<name>`) before the absorb, so the authoritative server branch can land. Only when the binding's branch *is* that mirror (`serverRef == branchRef`, i.e. not an adopted branch); an adopted binding keeps today's absorb-failure path, because force-moving the MasterMind's own branch is not this round's call.
- **No other binding field changes in the fetch half.** `LastKnown`/`RemoteAbsorbFailures` stay the apply half's job.

## Per-file changes

- **`internal/relevo/remote_bundle.go` (new)** — the bundle half of the catch-up, extracted so `remotefetch.go` stays under the 600-line cap and each function stays under 70:
  - `fetchCatchUpBundle`: request `RoundBundle(since = b.Builder.LastKnown)`; on the stale-base signal warn once and re-request with `since = ""`; a still-failing request keeps today's hard path (`cf.Abort = true`, `cf.release()`, return); a successful *fallback* calls the re-base below; then `absorbCatchUpBundle`.
  - `sinceStale(err) bool`: the 422 + `not_fast_forward` classification, in the `errors.As(&client.HTTPError)` style `applyRemoteErr` already uses (`remote_sync.go:189-218`).
  - `resetRoundBase(ctx, rt, b, serverRef) error`: `rt.Git.DeleteBranch(ctx, b.Repo, serverRef)` — the checked-out-safe reset (`git branch -D` refuses a branch held by any worktree); any error is classified by the caller.
  - `absorbCatchUpBundle`: today's `remotefetch.go:498-549` moved verbatim (server ref + dirty side ref allow-list, `checkedOut`, `AbsorbErr`, `Fatal`, adopted-branch `UpdateRef`).
- **`internal/relevo/remotefetch.go`** — `fetchCatchUpBundle` body (485-549) leaves; `catchUpFetch` fields/`applyCatchUp` (554-591) untouched, so a hard bundle failure still discards the fetched report exactly as today; `checkedOut` (309-313) widens to also match git's `"used by worktree"` refusal, which is the message `DeleteBranch` gives on a checked-out base; the `Abort`/`AbsorbErr` comments (274-288) and the `fetchCatchUpBundle` doc comment are re-worded to the retry.
- **`internal/relevo/remotefetch_test.go`** — fetch-half tests using `roundClosedRemote`/`roundBundleFunc`/`countCalls`/`fakeTransport`/`fakeGit`.
- **`internal/relevo/remote_test.go`** — the real-git repro plus the checked-out base case, beside `TestCatchUpOrderAndIdempotence` (4567-4703) and `TestCatchUpBranchCheckedOutRetries` (5688-5744).
- **`docs/plans/2026-09-30-remote-fetch-wedge.md` (new)** — this plan, in the round's commit.
- **`testdata/coverage-baseline.txt`** — only if `make check` reports `internal/relevo` below baseline (86.1); expected not to, since every new line is covered.

## Ordered steps (each names its deliverable and done-when)

1. Extract `internal/relevo/remote_bundle.go` (move + split only, behaviour identical). Done when `go build ./...` passes, `git add internal/relevo/remote_bundle.go && sh scripts/check-filesize.sh` prints ok, and `remotefetch.go` is under 600 lines.
2. Add `sinceStale` and the one-shot full retry in `fetchCatchUpBundle`, guarded on `LastKnown != ""`. Done when step 5's first three tests pass.
3. Add `resetRoundBase` and call it on the fallback path (after the full bundle is open, before the absorb); route a refusal to `cf.CheckedOut` + `warnCheckedOut` and any other error to `cf.AbsorbErr`; widen `checkedOut`. Done when step 6's second test and step 5's "no reset when the retry fails" test pass.
4. Re-word the affected comments (why, never what; no issue numbers). Done when `sh scripts/check-comments.sh` and `make check-static` pass.
5. Fetch-half tests in `remotefetch_test.go`:
   - `TestFetchCatchUpBundleFallsBackToAFullBundle` — a 422 on `since=LastKnown` then a fresh stream on `since=""`: exactly two `RoundBundle:` calls with those `since` values, one `DeleteBranch` of the mirror, one `Absorb`, and `Abort` false.
   - `TestFetchCatchUpBundleKeepsOtherErrorsHard` — a 500 on the incremental: one `RoundBundle` call, `Abort` true, no `DeleteBranch`, no `Absorb`.
   - `TestFetchCatchUpBundleDoesNotResetTheBaseWhenTheRetryFails` — 422 then 500: no `DeleteBranch` (the mirror survives), `Abort` true.
   - `TestFetchCatchUpBundleRetriesOnlyForAStaleBase` — a 409 carrying `not_fast_forward`, and a 422 carrying another code, each get one call.
   Done when `go test -race -count=1 -run 'TestFetchCatchUpBundle' ./internal/relevo/` passes.
6. `remote_test.go`:
   - `TestCatchUpClosesRoundAfterHistoryRewrite` — real git: client repo via `newRemoteClientRepo`, round 1 closes and the mirror reaches `r1`; then the same server-side repo amends that commit (`r2` does not descend from `r1`), the fake answers 422 for `since=r1` and a real `Snapshot(..., since="")` bundle for `since=""`; assert `Round == 3`, `LastKnown == r2`, the client's `refs/heads/relevo/api == r2`, a `KindReport` entry, one `Ack`, and two `RoundBundle:` calls. **This is the seed's "reproduces the non-ancestor case and proves the round closes".**
   - `TestCatchUpRewriteOnCheckedOutBaseRetries` — the mirror checked out (`checkout relevo/api`) with a stale base: the reset is refused as "used by worktree", so state stays ACTIVE, `RemoteAbsorbFailures == 0`, no halt, no `Ack`, no `Absorb`.
   Done when `go test -race -count=1 -run 'TestCatchUpClosesRoundAfterHistoryRewrite|TestCatchUpRewriteOnCheckedOutBaseRetries' ./internal/relevo/` passes.
7. Run the gate: `make check`. Done when it is green with the new file staged; if `scripts/check-coverage.sh` reports a drop, regenerate with `sh scripts/check-coverage.sh --write` and say so in the report.

Focused command for the loop: `go test -race -count=1 -run 'TestFetchCatchUpBundle|TestCatchUp' ./internal/relevo/`. Full gate: `make check`.

## Mutation pins

- Remove the retry (`if sinceStale` branch) → `TestCatchUpClosesRoundAfterHistoryRewrite` must fail: one `RoundBundle` call, round stays ACTIVE.
- Remove `resetRoundBase` → the same test must fail at the absorb (non-fast-forward, `RemoteAbsorbFailures == 1`, no `Ack`).
- Widen the retry to any error → `TestFetchCatchUpBundleKeepsOtherErrorsHard` must fail (extra call / mirror deleted).
- Move the reset before the full fetch completes → `TestFetchCatchUpBundleDoesNotResetTheBaseWhenTheRetryFails` must fail (`DeleteBranch` recorded).
- Revert `checkedOut` to `"checked out"` only → `TestCatchUpRewriteOnCheckedOutBaseRetries` must fail (halt/count instead of quiet retry).

## Deleted behaviour (closed list)

1. Nothing behavioural is deleted: `applyCatchUp`'s `cf.Abort` early return (`remotefetch.go:555-557`) stays, so a hard bundle/file failure still discards the fetched report exactly as today.
2. The single-request body of `fetchCatchUpBundle` (`remotefetch.go:488-549`) is removed from that file and re-created in `remote_bundle.go` — a move, with the absorb half byte-for-byte equivalent.
3. `checkedOut` is not deleted; its match widens.

## Closed file list

1. `internal/relevo/remote_bundle.go` (new)
2. `internal/relevo/remotefetch.go`
3. `internal/relevo/remotefetch_test.go`
4. `internal/relevo/remote_test.go`
5. `docs/plans/2026-09-30-remote-fetch-wedge.md` (new)
6. `testdata/coverage-baseline.txt` — only if the gate reports a drop

## Rules

- Non-test files ≤ 600 lines and functions ≤ 70: enforced, and `internal/relevo` is **not** on `scripts/check-filesize.allow`, so the new file must be `git add`ed before `sh scripts/check-filesize.sh` is trusted (`git ls-files`).
- Comments say why, never what; no `#NNN`, no "round N", no spec citations in code — the issue number belongs in the commit message. Tests follow the same rule.
- One change per PR; `make check` is the only gate; no baseline is lowered and no exclusion added to get green; a `cmd/relevo` test must not spawn a harness or reach the network (this round adds none).

## What the round's report must include

- That fix 2 was **not** done, with the code evidence: `remote_catchup.go:132-150` + `:58-64`, the pinned `remote_test.go:4628-4654`, spec `:315`, and the `LastKnown`-poison argument.
- The mirror re-base: why a bare retry was insufficient (the verified non-fast-forward absorb), that it applies only when `serverRef == branchRef`, and that an adopted binding with a rewritten server branch still stops at the absorb-failure halt.
- Observed limits left in place: a persistent non-stale bundle error still retries each tick without a halt; `Absorb`'s fast-forward contract is untouched.
- Gate output: `make check` result, `check-filesize` with the new file staged, and whether `testdata/coverage-baseline.txt` moved.
- The commands run, and the plan file committed as `docs/plans/2026-09-30-remote-fetch-wedge.md`.
