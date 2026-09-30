# Plan: a round-close absorb must not halt on git's own maintenance (#703)

## 0. Seed vs tree — read this first

Verified in this tree (`857a8fa2`) before writing the plan:

1. **"The halt sticks after a later absorb succeeds" does not reproduce as a leftover halt.** A scratch `internal/relevo` test (10 counted absorb failures, then a success) gave `state=needs_you round=1` at the tenth failure, then `state=active round=2 halt="" failures=0` after the success. The success closes round 1 through `queueReport`, which already clears `Halt`/`HaltAt`/`HaltNotifiedRound` (`internal/relevo/reconcile.go:585-587`). The scratch file was removed; nothing of it is in this plan.
2. **The `NEEDS YOU` the incident's statusline showed is the pending-report fault, not a halt.** `internal/view/statusline.go:426-430` marks a row NEEDS YOU when a report is pending on a route that cannot push ("pull") — which is exactly `round 2 ACTIVE` with `pending report round 1 -> mastermind`. A halt and a closed round cannot coexist in one row (one `State`).
3. **"Don't advance the round counter for a halt" is already the invariant.** The only `Round++` is `internal/relevo/reconcile.go:574`, inside `queueReport`. The halt path (`applyCatchUpAbsorb` returning `stop=true`) returns before `applyCatchUp` reaches `queueReport`. The plan pins this with assertions; it adds no counter logic. **A builder who finds a halt path reaching `queueReport` halts the round.**
4. **The real, reproducible defect is the fetch running maintenance inline.** `FetchBundle` fetches with `-c gc.autoDetach=false` (`internal/git/bundle.go:196`), so an auto-maintenance repack runs *inside* the fetch and rewrites refs while the fetch reads them — the incident's `bad object refs/heads/relevo/db-owner-r1-build` / `did not send all necessary objects` for a round whose ref and objects were afterwards present and `fsck`-clean. So the plan fixes the source (maintenance off for that command) **and** gates the failure counter on the store, so a transient failure is never counted toward the halt at all.

Because of (1)-(3) this round's code changes are: the fetch flags (`internal/git/bundle.go`), the verified-store gate and the halt clear (`applyCatchUpAbsorb`), and one call-site line. The "halt clears on a later success" and "the round counter" clauses become named tests, not new logic.

## 1. Behaviour and cases

Round closes on the server; the client's round-close fetch/absorb (`FetchBundle` via `Transport.Absorb`) fails.

| # | Case | Before | After |
|---|---|---|---|
| 1 | Fetch runs while git maintenance is repacking | maintenance runs inside `fetch`; repack rewrites refs; fetch dies on a spurious bad object | fetch runs with maintenance off; no self-race |
| 2 | Absorb errors, but the binding's branch already holds `view.ResultCommit` | counted as a failure; ten such ticks halt | **not counted**: the store already holds the round; the catch-up proceeds (ack, report, close) |
| 3 | Absorb errors, branch not at the round's commit, first failures | counted; retried next tick | counted; retried next tick (unchanged) |
| 4 | Absorb errors, branch absent, tenth failure | `haltBinding`, NEEDS YOU, round stays | same: `haltBinding`, NEEDS YOU, round stays (unchanged) |
| 5 | Absorb succeeds after the binding was halted on absorb failures | halt clears only when the round closes (a lost `Ack` leaves NEEDS YOU) | halt clears at the absorb step (`State` active, `Halt`/`HaltAt`/`HaltNotifiedRound`/`RemoteAbsorbFailures` reset), `Round` untouched |
| 6 | `rt.Git` nil or `view.ResultCommit` empty (tests, a pre-result server) | — | gate skipped: count as today |
| 7 | `cf.CheckedOut`, `cf.Fatal` | unchanged | unchanged |

## 2. Seams (verified in this tree)

| What | file:line | Role |
|---|---|---|
| fetch command + doc | `internal/git/bundle.go:166-209` (run at `:196`, doc `:166-175`) | maintenance-off flags; doc rewritten |
| absorb outcome -> counter/halt | `internal/relevo/remote_catchup.go:121-140` (`applyCatchUpAbsorb`) | the gate, the halt, the clear |
| the catch-up write sites | `internal/relevo/remotefetch.go:551-589` (call at `:575`, reset at `:579`) | one-line plumbing only; **#705 territory** |
| halt record | `internal/relevo/reconcile.go:203-219` (`haltBinding`) | unchanged |
| the sole round advance | `internal/relevo/reconcile.go:574` (`queueReport`) | unchanged; pinned |
| git test helpers | `internal/git/helpers_test.go:16-37` (`runGit` sets `maintenance.auto=false`, `gc.auto=0`), `:99` (`bareRepo`) | the new test must re-enable `gc.auto` |
| fetch tests | `internal/git/bundle_test.go:229` (`TestFetchBundleFastForwards`), `:318` (`TestFetchBundleMissingPrereq`) | where the new test goes |
| absorb tests | `internal/relevo/remote_test.go:5589` (`TestCatchUpAbsorbFailuresHaltAtTen`), `:4348` (`TestCatchUpOrderAndIdempotence`) | extend / sit beside |
| fake git | `internal/relevo/fake_test.go:108-424` (`fakeGit`, `RefSHA` at `:414`) | no change; the gate test configures `refSHA` |
| fake remote/transport | `internal/relevo/remote_test.go:252-292`, `:2196` | no change |

## 3. Ordered steps

Focused commands use `-count=1`; fix every reported error before the next run.

1. **Maintenance off.** In `internal/git/bundle.go`'s `FetchBundle`, replace the fetch's `-c gc.autoDetach=false` with `-c maintenance.auto=false -c gc.auto=0` (before `fetch`, as globals), and rewrite the doc sentence (`:166-175`) to say why: the round-close fetch must not trigger git's auto-maintenance, because a repack rewrites refs mid-fetch and the fetch then dies on a bad object for a round the store already holds. No issue numbers. Done when `go build ./...` passes.
2. **Pin the flags.** Add `TestFetchBundleLeavesAutoGcOff` in `internal/git/bundle_test.go` beside `TestFetchBundleFastForwards` (`:229`): a `bareRepo` whose `gc.auto` is set back to `1` (the helper turned it off), a bundle that moves a ref, `FetchBundle`, then assert the object store gained no pack and kept its loose object. Done when `go test -count=1 -run TestFetchBundle ./internal/git/` passes (it fails on today's tree — report the first failure as the proof).
3. **The gate and the clear.** In `internal/relevo/remote_catchup.go`, change `applyCatchUpAbsorb` (`:121-140`) to take the closed-round view, and: on `cf.AbsorbErr != nil`, if the store already holds the round — new helper `absorbHoldsRound`, true when `rt.Git != nil`, `view.ResultCommit != ""`, and `rt.Git.RefSHA` on the binding's local branch (normalised to `refs/heads/…` as `fetchCatchUpBundle` does) returns exactly `view.ResultCommit` — do **not** count; return the cleared binding with `stop=false`. Otherwise count as today and halt at the kept threshold of 10. On the success fall-through, clear an absorb-failure halt with new helper `clearAbsorbHalt`: when `RemoteAbsorbFailures > 0`, reset it and `Halt`/`HaltAt`/`HaltNotifiedRound`, set `State` active, and leave `Round` alone. Comments say why (the maintenance race, the round rule). Both helpers stay well under 70 lines; `remote_catchup.go` stays far under 600. Done when `go test -count=1 -run 'TestCatchUpAbsorb|TestCatchUpOrderAndIdempotence' ./internal/relevo/` passes.
4. **One-line plumbing.** In `internal/relevo/remotefetch.go`, `applyCatchUp` (`:551-589`, call at `:575`): keep the returned binding when the absorb did not stop — `next, stop, err := applyCatchUpAbsorb(ctx, rt, b, view, cf); if stop { return next, nil, err }; b = next`. Without this the gate/clear value is discarded (`next` is dropped for `stop=false`). This is the one line inside #705's region; the builder reads #705's diff first and reconciles if it moved.
5. **Behaviour tests.** In `internal/relevo/remote_test.go`: (a) extend `TestCatchUpAbsorbFailuresHaltAtTen` (`:5589`) to assert `got.Round == 1` on every iteration (the round-counter rule; with no `Git` the gate is skipped, so the counter climbs as today); (b) add `TestCatchUpAbsorbIgnoresAFailureWhenTheBranchHoldsTheRound` beside it — `fakeTransport` with `absorbErr`, `rt.Git = &fakeGit{refSHA: map[string]string{branch: resultCommit}}`, `getBindingResp.ResultCommit = resultCommit` — one reconcile closes round 1: `Ack` called, report queued, `RemoteAbsorbFailures == 0`, no NEEDS YOU; (c) add `TestCatchUpAbsorbRecoveryClearsTheHaltWithoutAdvancingTheRound` — the binding stored NEEDS YOU at round 1 with `RemoteAbsorbFailures` at the threshold, a clean bundle, `Ack` failing — assert `State` active, `Halt == ""`, `Round == 1`. Done when `go test -count=1 -run 'TestCatchUpAbsorb|TestCatchUpOrderAndIdempotence' ./internal/relevo/` passes.
6. **Mutations** M1–M4 in §4; restore each before the next.
7. **Ship.** Copy this plan verbatim to `docs/plans/2026-09-30-absorb-verified-halt.md`, run `make check` once, commit `fix(remote): a round-close absorb does not halt on git's own maintenance` (no issue number in the code; `Fixes #703` in the body only), do not push. Done when `make check` exits 0, `git status --porcelain` is clean, and `git show --stat HEAD` lists exactly the plan plus the declared scope.

## 4. Mutation checks

| # | Break this | Command | Test that must fail |
|---|---|---|---|
| M1 | drop `-c gc.auto=0`/`-c maintenance.auto=false` from the fetch | `go test -count=1 -run TestFetchBundleLeavesAutoGcOff ./internal/git/` | that test (a pack appears) |
| M2 | make `absorbHoldsRound` always false | `go test -count=1 -run TestCatchUpAbsorbIgnoresAFailureWhenTheBranchHoldsTheRound ./internal/relevo/` | that test (halts / no close) |
| M3 | make `absorbHoldsRound` always true | `go test -count=1 -run TestCatchUpAbsorbFailuresHaltAtTen ./internal/relevo/` | that test (never halts) |
| M4 | drop `clearAbsorbHalt` on the success path | `go test -count=1 -run TestCatchUpAbsorbRecoveryClearsTheHaltWithoutAdvancingTheRound ./internal/relevo/` | that test (State/Halt unchanged) |

Restore each; the final diff contains none.

## 5. What this round deletes (closed list)

1. The `-c gc.autoDetach=false` argument on the fetch in `internal/git/bundle.go` (`:196`), replaced by `-c maintenance.auto=false -c gc.auto=0`.
2. The doc sentence at `internal/git/bundle.go:173-175` about `gc.autoDetach`, rewritten to say why maintenance is off.
3. Nothing else. No test, route, error code, message or golden is deleted; the 10-failure halt threshold stays; `haltBinding`, `queueReport`'s `Round++` and the catch-up write order are untouched; no lint, size or coverage exclusion is touched.

## 6. Declared scope

The builder's diff touches exactly:

- `internal/git/bundle.go` — the fetch flags and the doc.
- `internal/relevo/remote_catchup.go` — `applyCatchUpAbsorb` (signature + gate + clear) and the two small helpers.
- `internal/relevo/remotefetch.go` — one line in `applyCatchUp` (`:575`); the only overlap with #705.
- `internal/git/bundle_test.go` — the new git test.
- `internal/relevo/remote_test.go` — the extended and new tests.
- `docs/plans/2026-09-30-absorb-verified-halt.md` — this plan.

No change to `Runtime.Git` / the git interface / `fakeGit`, no change to `catc`h-up write sites beyond the one line, no `store` change, no baseline file.

## 7. Verification

- Focused: `go test -count=1 -run 'TestCatchUpAbsorb|TestCatchUpOrderAndIdempotence' ./internal/relevo/` and `go test -count=1 -run TestFetchBundle ./internal/git/`.
- Full: `make check` once at the end (gofmt over every tracked `.go`, `go vet`, `go mod tidy` check, golangci-lint, comment and file-size checks, coverage baseline). CI has no harness binary and no network; both new tests are pure `internal/relevo` / `internal/git` tests using the existing git helpers.

## 8. What the report must include

- the seed-vs-tree findings in §0 restated with the scratch evidence (the halt already cleared on the close; the statusline NEEDS YOU was the pending report; the single `Round++`);
- each focused command with its pass output, and the single `make check`;
- every step halted on or left undone, instead of improvising (particularly a halt path that reaches `queueReport`, or #705 having rewritten `applyCatchUp`'s call site);
- `git diff --stat` checked against §6, the commit SHA/subject/body, and that nothing was pushed;
- M1–M4: the exact edit, the named test that failed with its first failure line, and that it was restored;
- statement coverage of `internal/relevo` and `internal/git` against `testdata/coverage-baseline.txt`, and that no baseline was edited;
- deferred, with reasons: whether the `stalled` pending-report NEEDS YOU (`internal/view/statusline.go:426-430`) should be revisited, and the untestable acceptance branch of the store gate (a ref at its head cannot be made to fail a clean `git fetch`), so the gate is pinned by the predicate and the fake-git table, not end to end.
