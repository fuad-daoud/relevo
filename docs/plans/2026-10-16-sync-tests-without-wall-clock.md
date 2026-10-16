# Plan: sync tests without wall clock (`sync-tests-without-wall-clock`)

Base: `origin/main` at `2c821c8b` on branch `relevo/rf-flaky`.

# MasterMind amendments (read first; these override the plan below)

- Prefer shrinking the work over removing a test's bound. "Run under the production
  default" (a 9-minute StepTimeout) is allowed only for a test that does not pin the
  bound AND whose work cannot be shrunk; name each such site in the report. A hung test
  must still fail well inside `go test`'s 10-minute default.
- No production change in internal/sync unless step 5 proves a syncpipe seam is
  unavoidable; another chain (se5b) is editing internal/sync production code. A seam in
  internal/syncpipe is acceptable if minimal and default-preserving.
- Do not touch testdata/coverage-baseline.txt.
- Commit your work on the branch before writing the report (new commits only).



# Sync tests that do not depend on wall-clock speed — implementation plan

Fixes #1178 (wall-clock step bound flakes) and #1209 (syncpipe coverage swing).
Fix the tests, not the thresholds. Tests only, unless a minimal production
seam proves necessary; keep any `internal/sync` production change to the
minimum (the se5b sync-rekey chain is changing that production code now).

Seed-vs-code check: no contradiction found. The seed's cited bounds exist
(`internal/sync/drain_test.go:78` `Timeout: 30 * time.Second`,
`internal/relevo/sync_config_test.go:65` `Timeout: time.Second`), the three
named failures match #1178's body and comments, and the syncpipe swing
(88.4–90.0 vs baseline 90.0 in `testdata/coverage-baseline.txt:55`) matches
#1209. `gh issue view 1178/1209` read during planning confirms the above.

## Behaviour and cases

1. **Steady drain without wall-clock dependence.** `TestSteadyDrainsEveryPageOfAnOrigin`
   (`internal/sync/drain_test.go:67-86`) still pins that one `SyncOnce` reads a
   backlog larger than one page to the end (mutation: a single `Import` stops
   after the first page and the test fails), but passes under `-race` on a
   loaded host because the work is small, not because the bound is large.
   Preferred direction per #1178: shrink the page for this test (a `pagingLog`
   page of a few dozen entries with `total = page + 1`, still two pages), not a
   bigger `Timeout`. No bigger timeouts as the fix (CLAUDE.md); no baseline
   change.
2. **Every sync `Runner` with a wall-clock step bound inventoried and made
   safe.** Each test that builds a `Runner` with `Timeout > 0` is listed with a
   verdict (at risk / not at risk + why). At-risk cases stop depending on how
   fast a step runs: shrink the work, remove the test's bound (run under the
   production default so the bound cannot fire mid-test), or drive the step
   through an injected clock / test hook. The named cases
   `TestHandEditedHookExportsOnTheNextAttempt`
   (`internal/relevo/sync_hooks_test.go:66-94` via `cfgMachine`) and
   `TestAnOverCapSecretIsRefusedOnExport`
   (`internal/relevo/sync_secrets_test.go:162-192`) must pass under `-race`
   with `-count=5` and under load. The two deliberate-bound tests keep pinning
   the bound deterministically and are NOT removed or loosened:
   `TestSteadyCancelsAStepThatRanPastItsBound`
   (`internal/sync/steady_test.go:179-207`, `cancelLog` never answers so the
   30 ms bound always fires) and `TestWithinStopsTheStepAtItsBound`
   (`internal/sync/stop_test.go:147-182`, first call held open, bound passes
   while it waits).
3. **Deterministic syncpipe coverage.** The timing-dependent branches in
   `internal/syncpipe` (found by running the package's tests several times
   with `-coverprofile` and diffing covered blocks) are made deterministic via
   an injected clock or a hook/synchronisation (e.g. ready channels such as the
   existing `waitForWorker` pattern, `hangOnceBackend`), not by lowering the
   baseline. Repeated runs of the same commit then measure within the coverage
   job's tolerance (drop of more than one point below
   `testdata/coverage-baseline.txt` fails; no round lowers a baseline to get
   green, CLAUDE.md:95-102). Wall-clock `elapsed > …` / `time.After(5s)`
   assertions that can cover different blocks per run are synchronised or
   removed.
4. **No production behaviour change.** `StepTimeout` (`internal/sync/steady.go:34`,
   9 min), `dataCallTimeout`/`controlCallTimeout`
   (`internal/syncpipe/client.go:53-56`), and the
   `TestSteadyStepBoundSitsBelowTheDataCallBound` ordering
   (`internal/syncpipe/client_test.go:760-764`) are unchanged. If a production
   seam (injectable clock/hook on `Runner`, `Client`, or `Supervisor`) proves
   necessary it is minimal, default-preserving, and named in the report.

## Seams (files, types, functions, line ranges)

- `internal/sync/runner.go:27-61` — `DefaultTimeout`, `Runner` (`Timeout` field
  `42-45`: zero means `StepTimeout`; test-shortened bound).
- `internal/sync/steady.go:23-34` — `errStepTimedOut`, `StepTimeout`;
  `:84-93` `SyncOnce`; `:102-109` `attempt` (`CaptureFiles` runs before the
  bound — relevant to hook-test cost); `:117-158` `exchange` (`within` at
  `:121`); `:234-253` `drainImport` + `within` per `Import` (`:240`);
  `:284-307` `within` (the timer/select that reports `errStepTimedOut`);
  `:318-325` `stepTimeout`; `:401-407` `recordRemoteKDF` (also under the step
  bound).
- `internal/sync/drain.go:23-35` — `drainImport` loop shared by join + steady.
- `internal/sync/drain_test.go:12-38` — `drainPage` (`:14`, 1024), `pagingLog`,
  `seedFarMany`; `:44-63` join drain (no `Runner` bound, reference);
  `:65-86` steady drain (the flake, `Timeout: 30s` at `:78`);
  `:88-109` held-origin drain (`Timeout: time.Second` at `:97`, plus an
  `elapsed > 5s` wall-clock assertion at `:106`).
- `internal/sync/steady_test.go:46-91` export+import (`Timeout: time.Second`
  `:58`); `:139-174` `cancelLog` harness; `:179-207` deliberate timeout
  (keep); `:209-231` caller-cancel (keep, `Timeout: time.Minute` `:219` is
  never reached); `:233-270` trouble+times (`:247`).
- `internal/sync/stop_test.go:14-60` `restartLog`; `:96-128`
  `TestRunnerStopEndsAnAttemptBetweenItsCalls` (`Timeout: time.Minute` `:102`,
  channel-synchronised, keep pattern); `:143-182` `within` bound test (keep).
- Other `Timeout: time.Second` `Runner` sites to inventory (all fast
  `MemTransport` cases; verdict each): `internal/sync/sign_test.go:24,56,112,147,180`;
  `internal/sync/marks_test.go:71,110,145,173`; `internal/sync/compact_test.go:52,87`;
  `internal/sync/bytes_test.go:40,67`; `internal/sync/bytes_join_test.go:37`;
  `internal/sync/quota_test.go:74`; `internal/sync/disable_test.go:68`
  (plus `Disabler.Timeout` at `:280,339` — out of scope unless the inventory
  shows risk); `internal/sync/stop_test.go:102` (covered above).
- `internal/relevo/sync_config_test.go:40-67` — `cfgMachine`/`newCfgMachine`
  (`Timeout: time.Second` at `:65`); `:69-74` `attempt`.
- `internal/relevo/sync_hooks_test.go:44-50` `attemptIn`; `:62-94` hand-edited
  hook case (failure quoted in #1178).
- `internal/relevo/sync_secrets_test.go:105-157` max-size inline case (heavy:
  `largestSealedSecret` seal loop + over-threshold bodies);
  `:159-192` over-cap refusal case (failure quoted in #1178);
  `:720-778` forged-entry helpers.
- `internal/relevo/sync_joinzero_test.go:205`,
  `internal/relevo/syncverb_join_opener_test.go:55,205` — further
  `Timeout: time.Second` `Runner` sites for the inventory.
- `internal/syncpipe/client.go:46-86` `dataCallTimeout`/`controlCallTimeout`,
  `callBound`, `bound` (`Client.cfg.Timeout` override `:81-86`);
  `Config.Timeout` `:121-124`; `:365-407` `call`/`callLocked`;
  `:441-480` `readReply` (timer branch `:477-479` `"no reply within"`);
  `:482-519` `fail`/`miss`/`cancel`/`kill`; `:521-552` `isGone`/`reap`/`gone`.
- `internal/syncpipe/supervisor.go:20-53` `Supervisor`; `:42-53`
  `NewSyncRunner`; `:264-321` `drive`/`worker`/`settle` (success / `ErrRefused` /
  `errCancelled` / miss branches — prime coverage-swing suspects);
  `:239-251` `Cancel`.
- `internal/syncpipe/client_test.go:70-147` fake-worker modes (`hang`,
  `hang-once`, `die`, `refuse*`, `wrong-id`, `stale-id`); `:422-448`
  `handshake` (`Timeout: 10s` at `:430`) / `startFakeWorker`;
  `:568-591` hang-hits-deadline (`Timeout: 300ms`); `:661-684` miss-vs-refusal
  counting; `:704-720` `waitForExit` poll loop; `:729-764` `callBound` +
  step-below-data-bound pins (keep).
- `internal/syncpipe/supervisor_test.go:167-202` death-counted + missed-deadline
  (`Timeout: 300ms` at `:189`); `:227-282` cancel-in-flight (`Timeout: 10s`
  `:233`, `time.After(5s)` `:254`, `waitForWorker` `:271-282`);
  `:462-502` disable-cancels-hung-pull (`Timeout: 30s` `:471`,
  `elapsed > 5s` `:491`, `time.After(5s)` `:496`).
- Guardrails: `scripts/check-coverage.sh:1-142` (tolerance logic `:112-128`,
  `RELEVO_REQUIRE_COVERAGE=1` gate `:99-102`); `testdata/coverage-baseline.txt:53,55`
  (`internal/sync 77.5`, `internal/syncpipe 90.0`); `Makefile:26-85`
  (`check`, `check-test` runs `go test -race -count=1 -cover ./...`);
  `CLAUDE.md:75-102` (style, no-history comments, test names say what they pin,
  coverage rules).
- Output: `docs/plans/2026-10-16-sync-tests-without-wall-clock.md` (existing
  convention `docs/plans/YYYY-MM-DD-<name>.md`, CLAUDE.md:68-69).

## Ordered steps (one line each: deliverable + how to know it worked)

1. Inventory every test that builds a sync `Runner` with a wall-clock step
   bound — deliverable: risk table (`grep -rn 'Timeout:' internal/sync
   internal/relevo internal/syncpipe --include=*_test.go` plus the `Disabler`
   sites) with an at-risk/safe verdict per site — works when every site in the
   Seams list above appears in the table with a one-line reason.
2. Reproduce the flake signal and locate syncpipe's timing-dependent blocks —
   deliverable: `go test -coverprofile` run several times on
   `internal/syncpipe` with the covered-block diff showing which branches move
   between runs — works when the diff names the exact blocks/functions.
3. Make the steady drain test clock-independent — deliverable: small-page
   `TestSteadyDrainsEveryPageOfAnOrigin` (still two pages, still fails if the
   loop stops after one `Import`) — works when the single-`Import` mutation
   fails it and it passes `go test -race -count=5 ./internal/sync -run
   TestSteadyDrain`.
4. Make the remaining at-risk `Runner` tests clock-independent (cfgMachine
   helper, hooks/secrets/join cases) — deliverable: updated helpers/tests using
   shrunk work, unbound production default, or injected clock/hook, with the
   two deliberate-bound tests (`steady_test.go:179-207`, `stop_test.go:147-182`)
   intact — works when each previously at-risk test passes
   `go test -race -count=5` and a bound-injected-to-fire mutation (or the
   single-`Import` mutation where applicable) still fails it.
5. Make syncpipe's timing-dependent branches deterministic — deliverable:
   synchronised/injected-clock pipe + supervisor tests covering the Step-2
   blocks on every run, no baseline change — works when five consecutive
   `go test -coverprofile ./internal/syncpipe` runs cover those blocks every
   time and `RELEVO_REQUIRE_COVERAGE=1 sh scripts/check-coverage.sh` passes.
6. Prove the flake is gone under load — deliverable: touched packages green
   under `go test -race -count=5` and under load (`GOMAXPROCS=2` alongside a
   parallel `go test ./...`), per the seed constraints — works when all runs
   pass with no `errStepTimedOut` and no `elapsed > …` failures.
7. Full gate, plan file, and commit — deliverable: `make check` green plus the
   plan file saved to `docs/plans/2026-10-16-sync-tests-without-wall-clock.md`
   and committed with the code as new commit(s) (no amend/rebase of a remote
   branch) — works when `make check` passes, `git status` shows the plan file
   tracked, and `git log` shows only new commit(s).

Batching: Steps 3–5 edit disjoint files (`drain_test.go`; `sync_config_test.go`
+ `sync_hooks_test.go`/`sync_secrets_test.go`/join tests; `syncpipe/*_test.go`
plus any minimal seam) and can be built in parallel once Steps 1–2 fix their
targets; Step 6 runs one focused command per touched package before the full
`make check` in Step 7. One edit call per file, or one scripted sweep for
mechanical `Timeout` changes.

