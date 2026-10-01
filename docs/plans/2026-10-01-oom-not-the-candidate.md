
Repo: main at `0ac52a80` (37a669e6 + chains slice 2; re-check line numbers) (throwaway tree, nothing changed). One builder round, **new commits only** — never amend or rebase a commit already on the binding's branch. `make check` must pass; `cmd/relevo` tests never spawn a harness and never reach the network.

## 1. Behaviour and cases

**Part A — detection reads the journal, not systemctl.**
Scopes start `systemd-run --user --scope --quiet --collect` (`internal/proc/scope.go:27-48`), so a failed unit is garbage-collected at once and `systemctl --user show --property=Result` reports `Result=success` for it. `oomKilled` (`internal/relevo/oom.go:30-45`) is rewritten to read `journalctl --user USER_UNIT=<unit>.scope -o json`, parse `UNIT_RESULT` and `MEMORY_PEAK`, and ignore entries older than the current process start (`b.Builder.StartedAt`, Unix seconds). No journal, no journalctl, any failure → not oom, today's path. Behaviour matrix:

- unknown exit + journal `UNIT_RESULT=oom-kill` at/after process start → `requeueOOM`: same candidate, no exclusion, no switch, no counted switch; exit entry, re-queue note and (at `oomMaxKills`) halt say `killed: out of memory (peak <human>)`, never `systemd-oomd (host out of memory)` — a cgroup OOM is not a host OOM.
- exit `137` (the shell supervisor's `128+SIGKILL`, written when the kernel kills only the inner process; `internal/proc/proc.go:84` and existing `TestABuilderKilledByASignalLeavesItsCode`) + journal oom-kill → same re-queue path.
- oom-kill only from a previous attempt of the same round (before process start) → not oom; a re-queued round reuses the unit name, so the `since` filter carries the load.
- journal `success`, empty, missing journalctl, probe error, or `rt.Scope == nil` → today's path (counted switch / exclusion unchanged).
- retry: the user manager may log the result after the daemon sees the exit; the probe retries **3 attempts, 200 ms apart, inside one 5 s context**, and stops early on any `UNIT_RESULT`. Bound stated in code comment.
- stop requested → stop still wins (checked after detection, unchanged).

**Part B — two refusals stop being `internal` + `relevo bugreport`.**
- planner seed over the 4 KiB cap (`internal/relevo/send.go:301-302`) → `usage` (exit 2), next `trim the seed or pass --force`.
- chain member round still open (`internal/relevo/chain_send.go:36-38`, reached by `relevo chain --resume` while a member round is open) → `conflict`, next `relevo stop <member>` (the actual member name).
Both map through the existing mechanism: library sentinels/typed error + `writeError` (`cmd/relevo/args.go:246-282`). `send` and `chain` registry rows already list `usage`/`conflict` (`cmd/relevo/registry_rows.go:31-43,372-383`; help-json golden unchanged — verify).

## 2. Seams

| File | Change |
|---|---|
| `internal/spawn/spawn.go:177-185` | Replace `ScopeResultProber` with journal-based contract; add `ScopeResult{Result string; PeakBytes int64}`; method becomes `ScopeResult(ctx, unit string, since time.Time) (ScopeResult, error)`. |
| `internal/proc/scope.go:134-150` | Delete the systemctl `ScopeResult`; add journal probe; add pure `ParseScopeResult(lines []byte, since time.Time) spawn.ScopeResult`; add pure `journalArgv(unit string, since time.Time) []string`; add seam `var journalOutput = runJournal` and `var scopeResultRetryPause = 200*time.Millisecond`, `scopeResultAttempts = 3`. |
| `internal/relevo/oom.go:30-45` | `oomKilled(ctx, rt, b) (peakBytes int64, ok bool)`; guard `rt.Scope == nil \|\| b.Builder.StartedAt == 0`; `since = time.Unix(b.Builder.StartedAt,0).UTC()`. Add `oomWords(peak int64) string` (`killed: out of memory (peak 7.6 GiB)`; `(peak unknown)` when 0). |
| `internal/relevo/oom.go:68-118` | `requeueOOM(ctx, rt, tx, b, peak, now)`; halt message and both re-queue notes use `oomWords`. |
| `internal/relevo/oom.go:187` | `oomNoteFormat` cause clause: scope/kernel, not `host`/`systemd-oomd` (fourth surface; same false claim to the builder). |
| `internal/relevo/headless.go:890-893,922` | Probe when `codeText == "unknown" \|\| codeText == "137"`; suffix `; ` + `oomWords(peak)`; pass peak to `requeueOOM`. |
| `internal/relevo/fake_test.go:695-699,792-797` | `scopeResults map[string]spawn.ScopeResult`, `scopeResultErr error`, record `since`; new method signature. |
| `internal/relevo/send.go:24-26,301-302` | `var ErrSeedOverCap = errors.New(...)`; wrap it at the cap. |
| `internal/relevo/chain_send.go:13-24,36-38` | `type RoundOpenError struct{ Member string; Round int }` with today's exact message; return it. |
| `cmd/relevo/args.go:251-282` | `writeError`: `errors.Is(err, relevo.ErrSeedOverCap)` → `failNext(codeUsage, "trim the seed or pass --force", …)`; `errors.As(err, &*relevo.RoundOpenError)` → `failNext(codeConflict, "relevo stop "+open.Member, …)`. |

## 3. Ordered steps

1. `internal/spawn`: add `ScopeResult`, rewrite the prober interface (deliverable: the contract); check `go build ./internal/spawn/`.
2. `internal/proc/scope.go`: journal probe + `ParseScopeResult` + `journalArgv` + bounds/seams (deliverable: detection source); check `go build ./internal/proc/`.
3. Proc tests: `TestParseScopeResult` (table: incident fixture, before-`since`, `success`, peak-without-result, malformed line, missing timestamp, empty), `TestJournalArgv`, `TestScopeResultReadsTheJournal`, `TestScopeResultRetriesUntilTheJournalLands`, `TestScopeResultGivesUpAfterTheBound`, `TestScopeResultMissingJournalctlIsNotAnError` (real `runJournal`, empty PATH; seam tests are not `t.Parallel` and restore via `t.Cleanup`); check `go test ./internal/proc/ -run 'TestParseScopeResult|TestJournalArgv|TestScopeResult' -count=1`.
4. `internal/relevo/oom.go` + `headless.go`: peak return, 137 gate, wording, `requeueOOM` param (deliverable: the requeue behaviour); check `go build ./internal/relevo/` (still red until step 5).
5. Fake + relevo tests: update `oomRT`, all `TestOOMKilled*` scripting; update wording assertions (`headless_oom_test.go:31,77-78,92-93`); new `TestOOMKilledSIGKILLExitIsQueued`, `TestOOMKilledProbesSinceTheProcessStart`, `TestOOMKilledJournalFailureTakesTodayPath`, `TestOOMKilledJournalWithoutRecordTakesTodayPath`, `TestOOMKilledExitNoteNamesThePeak`, `TestOOMKilledRequeueNoteNamesThePeak`, `TestOOMKilledThirdKillHaltNamesThePeak`, `TestOOMNoteSaysScopeNotHost`; check `go test ./internal/relevo/ -run 'TestOOM' -count=1`.
6. Part B library: sentinel + typed error + internal tests (`TestPlannerSeedCapRefusesOverCap` adds `errors.Is(err, ErrSeedOverCap)`; new `TestSendChainRoundRefusesOpenRound`); check `go test ./internal/relevo/ -run 'TestPlannerSeedCap|TestSendChainRound' -count=1`.
7. Part B CLI: `writeError` mapping tests (`TestSeedOverCapIsAUsageRefusal`, pure), plus `TestChainResumeOpenMemberRoundIsAConflict` with a store-only seed (halted chain row, real plan file path, member prompt log entry, no report — the refusal precedes `startRound`, so nothing spawns); check `go test ./cmd/relevo/ -run 'TestSeedOverCap|TestChainResumeOpenMemberRoundIsAConflict|TestHelpJSON' -count=1` (golden must be unchanged).
8. Mutation check: temporarily make `Runner.ScopeResult` read `systemctl --user show --property=Result` again → **`TestScopeResultReadsTheJournal` must fail**; restore and re-run it green. `TestParseScopeResult` still passes (it pins parsing, not the command).
9. Save this plan verbatim to `docs/plans/2026-10-01-oom-not-the-candidate.md`; check `git status` shows only intended paths.
10. Run `make check` once; then commit code + plan doc in one new commit (`fix(headless): … (#782)`) and confirm `git log -1 --stat`.

Focused commands: step 3, 5, 6, 7 above. Full check: `make check` (once, at the end). `make e2e` is not required (`make check` does not include it).

## 4. Deleted behaviour (closed list)

1. `internal/proc/scope.go` systemctl `ScopeResult` implementation (lines 134-150) — replaced by the journal probe.
2. The `codeText == "unknown"`-only gate on OOM detection (`internal/relevo/headless.go:890`) — replaced by unknown-or-137.
3. `; killed by systemd-oomd (host out of memory)` in the exit-entry suffix (`headless.go:892`).
4. The same wording in the local re-queue note (`oom.go:101`).
5. The same wording in the served re-queue note (`oom.go:103`).
6. The same wording in the `oomMaxKills` halt message (`oom.go:77`).
7. `the host ran out of memory and systemd-oomd killed the builder process` in `oomNoteFormat` (`oom.go:187`).
8. The `fakeRunner.ScopeResult` string-scripting shape (`fake_test.go:695-699,792-797`) — test-only.

## 5. Fixture, mutation, report

Fixture (exact incident values, in the proc table test and the relevo fake): unit `relevo-round-local-turso-466-rev-5.scope`; `{"UNIT_RESULT":"oom-kill","MESSAGE":"Failed with result 'oom-kill'."}` and `{"MEMORY_PEAK":"8178532352","MESSAGE":"Consumed …"}` with `__REALTIME_TIMESTAMP` µs; human rendering `7.6 GiB`. No test runs the real journalctl.

Report must include: status / changed paths vs this plan; the one commit hash and subject (new commit, no amend/rebase); the mutation run and its exact failing test; `make check` result and the focused commands; coverage-baseline statement (no regeneration expected; if the check demands one, it is `sh scripts/check-coverage.sh --write` and the report says so — never lowered); the note that the `oomNote` prompt text was changed as a fourth surface beyond the three named ones; and `not_done` (e.g. no peak stored in `OOMRequeue`, no README change).

## MasterMind note

`TestChainResumeOpenMemberRoundIsAConflict` (step 7) stays in `cmd/relevo` only if it provably starts no daemon, spawns no harness and reaches no network. If it needs any of those, drop it and pin the mapping as a pure `writeError` test plus the library test `TestSendChainRoundRefusesOpenRound`; say which you did in the report.
