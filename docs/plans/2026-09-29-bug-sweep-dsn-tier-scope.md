# Plan: #684 fix-up — the stored-tier cap is a local-launch rule

Round 2 on `relevo/bug-dsn` after round 1 halted at step 13. The branch already carries three commits (`f588f477`, `9a2e5195`, `304cff39`), the tree is clean, and this round adds one commit on top. Do not amend, rebase, reset or redo any earlier step.

## Why round 1 halted

`make check` fails in `internal/e2e`: `TestServedBuilderLaunchesAtPolicyTier`. The server runs `policy.Policy{Tier: {"builder": "yolo"}, MaxTier: "yolo"}`; the client runtime's policy is zero, so its `MaxTierOrDefault()` is `edit`. Round 1 wired the stored-tier re-check (`launchTier`) into `sendPreflight` for every binding, ahead of the remote early return, so the client now refuses to send to a served binding whose tier the server set higher than the client's cap.

The e2e test states the design: a served binding's tier comes from the server's policy and reaches the harness argv, and the server caps it itself at resolution (`internal/relevo/served.go:215`). The client's local `max_tier` must not gate a remote send.

## Decision (made; do not re-decide)

- `sendPreflight`'s stored-tier derivation keeps the cap re-check for a **local** binding only. For a binding with `Builder.Remote()`, the tier stays `effectiveTier(b)`; the server enforces its own cap, and `--allow-yolo` stays a local command's allowance.
- `startRound` and `resumeRound` keep `launchTier` unchanged: each runs on the machine that launches the process (the client for a local binding, the server for a served one), so the cap it checks is the launching machine's.
- Every other round-1 change stands as committed.

## Behaviour and cases

- A local binding whose stored tier is above `max_tier` still refuses without `--allow-yolo` (`ErrTierAboveMax`), stages nothing and starts nothing.
- A remote binding whose stored tier is above the client's `max_tier` sends as before; the server resolves the round and applies its policy.
- The explicit `--tier` check is unchanged (it still wins over the stored tier).

## Seams (on this branch)

| What | file:line |
|---|---|
| the derivation to change | `internal/relevo/send.go:273-279` |
| the remote branch it must stay ahead of | `internal/relevo/send.go:301-325` |
| `launchTier` (unchanged) | `internal/relevo/tier.go:56-63` |
| the local pin to keep green | `internal/relevo/send_test.go:1765` `TestSendRefusesStoredTierAboveMaxWithoutAllowYolo` |
| the served pin | `internal/e2e/tier_test.go:25` `TestServedBuilderLaunchesAtPolicyTier` |

## Steps

1. In `sendPreflight`, replace the round-1 derivation with a local-only cap:

   ```go
   if tier == "" {
       if b.Builder.Remote() {
           // A served round is launched by the server, which caps its tier
           // against the server's own policy; the client's max_tier must not
           // refuse the send.
           tier = effectiveTier(b)
       } else {
           t, err := launchTier(b, rt.Policy, opts.AllowYolo)
           if err != nil {
               return preflight{}, err
           }
           tier = t
       }
   }
   ```

   Update the `preflight.tier` doc comment so it says the cap is checked for a local launch. Comment style: why only, no issue numbers.
   *Done when:* `go test ./internal/relevo/ -run 'TestSendRefusesStoredTierAboveMaxWithoutAllowYolo' -count=1` and `go test ./internal/e2e/ -run 'TestServedBuilderLaunchesAtPolicyTier' -count=1` both pass.
2. Mutation M4: call `launchTier` for every binding again (drop the `Remote()` branch). The named test `TestServedBuilderLaunchesAtPolicyTier` must fail. Restore the fix.
   *Done when:* the named test fails under the mutation and passes after the restore.
3. Copy this plan verbatim to `docs/plans/2026-09-29-bug-sweep-dsn-tier-scope.md`; `gofmt -w internal/relevo/send.go`; `git add` the plan and `internal/relevo/send.go` by name; commit `fix(relevo): cap the stored tier on the local launch only`, body ending `Fixes #684`. Do not push.
   *Done when:* `git log --oneline -4` shows exactly the four commits, `git show --stat HEAD` lists only the plan and `internal/relevo/send.go`, and `git status --porcelain` is empty after the commit.
4. `make check` exactly once.
   *Done when:* `make check` is green, the tree is clean, and the diff adds no exclusion, allow-list entry or coverage-baseline change.

## Report must include

- The halt cause and the decision taken (one sentence each).
- Commit 4: subject, hash, body ending `Fixes #684`; `git log --oneline -4`; nothing pushed; tree clean.
- The focused commands of steps 1-2 and `make check`'s single end-of-round result.
- Mutation M4: the exact edit, the failing test's first failure line, and that it was restored.
- Anything halted on or left undone, instead of improvising.
