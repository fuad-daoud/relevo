# Plan — remote fetch: bounded bundle-fetch failures, an explained adopted-rewrite halt, and the no-amend note (#759, #760, #761)

Branch: `relevo/tag-b-build`. One round, one PR, `make check` green. All three issues are open on this tree's HEAD (`29b14ae1`); #751's re-base is present and stays.

## What the code actually says (this shapes the whole plan)

1. **#759.** The catch-up fetch runs without the state lock (`fetchCatchUp`, `internal/relevo/remotefetch.go:330-337`): files first, then `fetchCatchUpBundle` (`internal/relevo/remote_bundle.go:24-59`). Only the stale-base refusal (`sinceStale`, `remote_bundle.go:76-81`) with a non-empty `LastKnown` earns the whole-branch retry (`:31-35`); every other error warns and sets `cf.Abort` (`:36-41`). `applyCatchUp` returns the binding unchanged for `Abort` (`remotefetch.go:492-494`), so the 2 s tick asks again forever, one warn per tick — the #746 incident. `catchUpFetch` (`remotefetch.go:274-288`) has no counter.
2. **The absorb shape #759 must copy.** `applyCatchUpAbsorb` (`internal/relevo/remote_catchup.go:132-150`) counts `RemoteAbsorbFailures`, halts at 10 via `haltBinding`, and skips counting when the branch already holds the round (`absorbHoldsRound`, `:152-171`); `clearAbsorbHalt` (`:178-188`) clears count, halt and state when a later attempt succeeds. The field is `internal/store/binding.go:280`; `recordFormat` never stamps it (`internal/store/format.go:14-21`).
3. **File-route failures are a different seam.** A non-404 report/diff/log/stream failure aborts before any bundle request (`remotefetch.go:343-359`; pinned by `TestCatchUpFetchAbortLeavesNothing`, `remotefetch_test.go:445-485`). They stay as they are; #759 names the bundle route.
4. **#760.** For an adopted binding (`holdsRoundMirror` false, `remote_bundle.go:61-71`) the #751 fallback skips the mirror re-base and goes straight to the absorb; the rewritten whole branch is refused non-fast-forward, so `cf.AbsorbErr` is git's opaque text and the halt reads "cannot absorb round N …". Only the fetch half knows `rewritten` before the absorb runs.
5. `haltBinding` (`reconcile.go:203-219`) trims the leading `<name>: ` into `Halt` and dedups its log per round; the caller saves whatever `Reconcile` returns.
6. **#761.** The placement spec ends at §10 (`docs/specs/2026-09-30-placement-preference-design.md:212`). The remote-builders spec §4.3 owns the inbound bundle (`since=LastKnown`), §5.3 (line ~367) claims "a failure at any step … retries next tick", and §7 (line ~466) lists only the absorb halt — #759 makes that claim false. The plan-writing contract relevo ships is `internal/harness/agents/architect.{claude,agy,opencode}.md` + `architect.codex.toml` (one shared body); `internal/harness/agents/shipped.sha256` records every shipped byte and `make check` runs `scripts/agents-shipped_test.sh`, which checks the real tree, so editing a definition without `sh scripts/agents-shipped.sh --write` fails the gate.

## Behaviour and cases

**#759 — a non-stale bundle fetch is bounded and halts.**
- A round-bundle request that fails after the one stale-base retry increments the binding's consecutive bundle-failure count. The first failure of a run logs today's `slog.Warn("fetch round bundle failed", …)`; failures 2..9 are quiet; the 10th calls `haltBinding` → `NEEDS YOU`, `Halt = "api: cannot fetch round bundle 1 from zen: <err>"` (binding, round, server and the error all named).
- A completed catch-up (or the existing holds-the-round clear) resets the count to 0 and clears a halt the bundle counter caused; a one-off failure leaves the binding `ACTIVE` and the next tick closes the round.
- The stale-base signal is a retry, not a failure: the whole branch is fetched, the mirror is re-based when it is the binding's branch, and `RemoteBundleFailures` stays 0.
- A file-route abort keeps today's quiet retry and never touches the counter.

**#760 — an adopted rewrite halts with an explanation.**
- When the fallback bundle arrives and the binding's branch is not the client's own mirror (`rewritten && !holdsRoundMirror`), the fetch does not attempt the absorb: it closes the bundle, sets `cf.AbsorbErr = "api: cannot absorb round N from zen: the server branch was rewritten; delete or re-point feature/x"` and returns. The existing absorb-failure path counts it (budget 10) and its halt carries that text. `absorbHoldsRound` still short-circuits: an adopted branch already sitting at the round's result closes the round instead of counting.
- Mirror bindings are untouched (re-base → absorb). Option 1 (re-pointing the adopted branch with `UpdateRef`) is not implemented.

**#761 — the rule is written where plan authors read.**
- Placement spec: a short `## 11. Operational notes` section: never amend or rebase a commit on a remote binding's branch; the client fetches each closed round as an incremental bundle based on the last absorbed commit, a rewrite makes that base a non-ancestor, and the client survives it only by re-fetching the whole branch and re-basing its mirror — an adopted binding's rewrite still ends in a halt; a plan must not order it.
- Remote-builders spec: one cross-reference sentence in §4.3, and §5.3/§7 made true again (the bundle fetch counts too; the adopted rewrite's halt names the rewrite).
- Architect definitions: one bullet in the "What a plan says" list (identical in all four kind files), then `sh scripts/agents-shipped.sh --write`.

## Decisions (the seed's open questions)

- **Budget 10, one shared constant** (`catchUpFailureBudget`, used by the absorb path at `remote_catchup.go:141` and the new site). No case for another number: both stages mean "this closed round cannot be brought home", and the issue asks for the same shape. No backoff: at the 2 s tick it would only push the halt later without changing what the operator must do.
- **New field `RemoteBundleFailures`, not `RemoteAbsorbFailures` reused.** The two stages have different messages, and the absorb path's git-race guard (`absorbHoldsRound`) does not apply to an HTTP/bundle-route failure; a separate counter keeps the halt text honest. `BindingFormat` stays 10: a binary that drops the counter loses only a failure run, which restarts.
- **Warn once per run**, gated on the binding snapshot's counter being 0 (the fetch half reads the count the last apply persisted). The halt is the escalation; the per-tick warning loop is the thing being deleted.
- **Bundle route only.** A file-route abort (`cf.Abort`, no bundle error) keeps today's retry; the report states this as deliberately left.
- **#761 guidance goes to the architect definitions**, not `CLAUDE.md`: the architect contract is the plan guidance relevo ships and what planner actors and MasterMind personas read; `CLAUDE.md` is this repo's local session guidance and would only duplicate it.

## Per-file changes

- `internal/store/binding.go:280-281` — add `RemoteBundleFailures int \`json:"remote_bundle_failures,omitempty"\`` beside `RemoteAbsorbFailures`, with a why-comment (the apply half owns it; the fetch half cannot write binding state).
- `internal/store/format.go:14-21` — extend the never-stamped list and what dropping the new field loses.
- `internal/relevo/remotefetch.go` — `catchUpFetch` (`:274-288`) gains `BundleErr error` ("the bundle request failed after the whole-branch retry"); the `Abort` branch of `applyCatchUp` (`:491-494`) routes `cf.Abort && cf.BundleErr != nil` through `applyCatchUpBundleFailure` and keeps the plain `b, nil, nil` otherwise; the success path (`:517-518`) also zeroes `RemoteBundleFailures`.
- `internal/relevo/remote_catchup.go` — `const catchUpFailureBudget = 10` with a why-comment; new `applyCatchUpBundleFailure(ctx, rt, b, view, cf)` beside `applyCatchUpAbsorb`: increment, halt at the budget with `fmt.Errorf("%s: cannot fetch round bundle %d from %s: %s", …)`; `clearAbsorbHalt` (`:178-188`) admits a bundle-only failure in its guard and clears both counters.
- `internal/relevo/remote_bundle.go` — `fetchCatchUpBundle`: warn only when `b.RemoteBundleFailures == 0`; on the hard path set `cf.BundleErr = err` (keep `Abort`); after a successful fallback, split on `holdsRoundMirror`: mirror → existing reset; adopted → close the bundle, set `cf.AbsorbErr` to the rewrite message, return (no git touched).
- `internal/relevo/remotefetch_test.go` — extend `TestFetchCatchUpBundleKeepsOtherErrorsHard` (`:701-724`) to assert `BundleErr`; new `TestFetchCatchUpBundleNamesARewrittenAdoptedBranch` using `bundleFetchFixture` (`:628-644`) with `b.Branch = "feature/x"` and `staleBase()` (`:646-650`); extend `TestCatchUpFetchAbortLeavesNothing` (`:445-485`) with `RemoteBundleFailures == 0`.
- `internal/relevo/remote_test.go` — new `TestCatchUpBundleFailuresHaltAtTen` (beside `:6068-6117`): a persistent 500, counts 1..9, no halt before 10, halt at 10 containing binding/server/round, and exactly one "fetch round bundle failed" warning across the ten ticks (`captureHandler`, as at `:6043`); new `TestCatchUpBundleFailureRecoveryClearsTheHalt` (mirror of `:6195-6249`); new `TestCatchUpAdoptedRewriteHaltsNamingTheRewrite`; extend `TestCatchUpClosesRoundAfterHistoryRewrite` (`:4731-4860`) with `RemoteBundleFailures == 0`.
- `docs/specs/2026-09-30-placement-preference-design.md` — `## 11. Operational notes` after §10 (line 212).
- `docs/specs/2026-09-19-remote-builders-design.md` — §4.3 cross-reference; §5.3 and §7 made true.
- `internal/harness/agents/architect.{claude,agy,opencode}.md`, `architect.codex.toml`, `shipped.sha256` (regenerated).
- `docs/plans/2026-09-30-remote-fetch-followups.md` (new) — this plan, committed as the round's record, as #746's round did.

## Ordered steps (each names its deliverable and done-when)

1. Add the `Binding.RemoteBundleFailures` field and the `format.go` comment line. Done when `go build ./...` passes and `go test -race -count=1 ./internal/store/` is green.
2. Add `catchUpFetch.BundleErr`, route the `Abort`/`BundleErr` case in `applyCatchUp` and zero the counter on success in `remotefetch.go`. Done when the existing catch-up tests stay green.
3. Add `catchUpFailureBudget`, `applyCatchUpBundleFailure` and the `clearAbsorbHalt` extension in `remote_catchup.go`. Done when steps 5-6's new halt/recovery tests pass.
4. Rework `fetchCatchUpBundle`: warn-once, `BundleErr`, adopted-rewrite detection with the explanatory `AbsorbErr`. Done when the fetch-half tests pass and `TestCatchUpClosesRoundAfterHistoryRewrite` is still green.
5. Add the fetch-half tests (step list above, `remotefetch_test.go`). Done when the focused command below passes.
6. Add the apply-half tests (step list above, `remote_test.go`). Done when the focused command passes.
7. Docs: the two spec edits; the architect bullet in all four files; `sh scripts/agents-shipped.sh --write`. Done when `sh scripts/agents-shipped.sh --check` and `go test -race -count=1 ./internal/harness/` pass.
8. Commit the plan verbatim as `docs/plans/2026-09-30-remote-fetch-followups.md`. Done when `git show --stat` names it.
9. Run the gate. Done when `make check` is green (`make check` runs the agents-shipped test and the coverage check; if `scripts/check-coverage.sh` reports a drop, regenerate with `sh scripts/check-coverage.sh --write` and say so in the report — no baseline lowered to get green).

Focused command: `go test -race -count=1 -run 'TestCatchUp|TestFetchCatchUpBundle|TestObserveRemote' ./internal/relevo/`. Full gate: `make check`.

## Mutation pins

1. Drop the `BundleErr` routing (no count) → `TestCatchUpBundleFailuresHaltAtTen` must fail: no `NEEDS YOU`, count 0.
2. Count the stale-base retry as a failure (set `BundleErr` on the first 422, or count the fallback) → `TestFetchCatchUpBundleFallsBackToAFullBundle` and `TestCatchUpClosesRoundAfterHistoryRewrite` must fail.
3. Revert `clearAbsorbHalt`'s guard/zeroing → `TestCatchUpBundleFailureRecoveryClearsTheHalt` must fail.
4. Drop the adopted-rewrite detection (fall through to the absorb) → `TestFetchCatchUpBundleNamesARewrittenAdoptedBranch` must fail (nil or git-text `AbsorbErr`), and `TestCatchUpAdoptedRewriteHaltsNamingTheRewrite` must fail on the halt text.
5. Leave the warning ungated → `TestCatchUpBundleFailuresHaltAtTen`'s warning count must fail.

## Deleted behaviour (closed list)

1. The unbounded per-tick retry of a non-stale round-bundle failure, one warning per tick (`remote_bundle.go:36-41` + `applyCatchUp`'s unchanged-return abort): replaced by the count, the first-failure warning and the halt at 10.
2. The opaque absorb attempt for an adopted binding whose server branch was rewritten (the fall-through from `remote_bundle.go:45-58` to `absorbCatchUpBundle`): replaced by the fetch-half detection and the rewrite message; no git ref is touched for that case.
3. Nothing else. The stale-base retry and mirror re-base stay; `Abort` still discards the fetched files; `RemoteAbsorbFailures`, `absorbHoldsRound` and the absorb halt stay; the file-route retry stays.

## Closed file list

1. `internal/store/binding.go`
2. `internal/store/format.go`
3. `internal/relevo/remotefetch.go`
4. `internal/relevo/remote_catchup.go`
5. `internal/relevo/remote_bundle.go`
6. `internal/relevo/remotefetch_test.go`
7. `internal/relevo/remote_test.go`
8. `docs/specs/2026-09-30-placement-preference-design.md`
9. `docs/specs/2026-09-19-remote-builders-design.md`
10. `internal/harness/agents/architect.claude.md`
11. `internal/harness/agents/architect.agy.md`
12. `internal/harness/agents/architect.opencode.md`
13. `internal/harness/agents/architect.codex.toml`
14. `internal/harness/agents/shipped.sha256`
15. `docs/plans/2026-09-30-remote-fetch-followups.md` (new)
16. `testdata/coverage-baseline.txt` — only if the gate reports a drop

## Rules

- Code comments say why, never what; no `#NNN`, no `§`, no "round N" (specs and plan docs may cite issues; Go comments and tests may not).
- Non-test files ≤ 600 lines, functions ≤ 70; `internal/relevo` is not on the filesize allow-list, so `git add` new files before trusting `scripts/check-filesize.sh`.
- CI has no harness binary and no network: the new tests are fakes (`fakeRemote`, `fakeTransport`, `fakeGit`) or real-git local repros, like the existing catch-up tests.
- No exclusion added, no baseline lowered, no test bent: if a step contradicts the code, halt and report.

## What the round's report must include

- Per issue: #759 (counter, warn-once, budget 10 shared with the absorb path, no backoff and why, file-route retries deliberately left); #760 (the detection, the message, option 1 not done); #761 (both spec edits and the architect line, with `agents-shipped --check`).
- The new `Binding` field and why `BindingFormat` stayed 10.
- Test evidence: the halt-at-ten test, the recovery test, the stale-base test (`RemoteBundleFailures == 0`), the adopted-rewrite fetch test and its halt text; the focused command's output.
- `make check` result, whether `testdata/coverage-baseline.txt` moved, and the `agents-shipped.sh --write` output.
- Commands run; `git diff --stat` compared against the closed file list; the plan file committed at `docs/plans/2026-09-30-remote-fetch-followups.md`.

```relevo
status: done
halted_at: ""
changed_paths: []
commands_run: ["gh issue view 759/760/761", "read remote_bundle.go, remote_catchup.go, remotefetch.go, remote_sync.go, reconcile.go, binding.go, format.go", "read the catch-up tests in remotefetch_test.go and remote_test.go", "read the placement and remote-builders specs, the architect definitions, the 2026-09-30-remote-fetch-wedge plan", "git log/show for #751 and the agents-shipped tooling", "touch /home/fuad/.local/state/relevo/tag-b-plan/001-done"]
not_done: ["no code or docs changed: this is the planning round", "make check not run: no build was made", "file-route (report/diff/log/stream) retries left out of scope per the seed's bundle-fetch decision"]
```
