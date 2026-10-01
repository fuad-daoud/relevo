# Chains · round 2: seeds name real files, the trace keeps each event's plan, resume rows say where they went

Base: main at `33575e61` (PR #779 merged); `git log -1` must show it or later, and `git ls-tree origin/main internal/db/migrations/` must still end at `018_chain_plan_start.sql`. One-shot builder round: `make check` and `make e2e` green on the branch's own commits. New commits only — never amend or rebase a commit already on a binding's branch. This round writes its plan to `docs/plans/2026-10-01-chain-seed-files.md` and commits it with the implementation (last step).

## Seed vs code (said plainly, not guessed around)

1. **Gap 1 is real, exactly as told.** The reviewer and correction seeds name `Store.DiffPath` / `Store.PlanDiffPath` (`internal/relevo/chain.go:362,372`), row-only keys (`internal/store/paths.go:206-217` plus `internal/store/roundfile_put.go:22`), and the builder's report, gate log and staged prompt (`:361,369,373`), which a seal pass moves into `round_file` and removes from disk (`internal/store/seal.go:349-417`). Live, the reviewer was handed `turso-466/004-diff.patch`, which never existed.
2. **The correction, security and fixes seeds name a reader's flat report path that no reader ever writes.** `v.OutputPath = rt.Store.ReportPath(c.Reviewer, closedRound)` (`chain.go:375`) and `ReportPath(c.Security, closedRound)` (`:378`) are `<member>/NNN-report.md`; a reader records its output at `<member>/NNN-<actor>/<label>.md` (`internal/relevo/summary.go:16-23`), which is also the path its log entry carries. `plannerReportPath` (`internal/relevo/chain_seed.go:56`) already reads the planner's entry path; the two output lookups do not. This round resolves all three the same way, or the correction/fixes seeds would newly say `not available` for an input that exists.
3. **`plan-diff.patch` is a row-only key that the store does not reserve.** `reservedRoundFileRe` (`internal/store/reserved.go:10`) lists `diff.patch`, `drift.patch`, `builder-segments.json` and consult findings, but not `plan-diff.patch`. A raw `os.Lstat` copy decision would therefore name a planted file where the row is the record — for `diff.patch` too. The decision who is the record must come from the store, not the filesystem.
4. **Gap 2 is real.** `RenderTrace` feeds `doc.Plan` (the chain row's current plan) to every `TraceLine` (`internal/relevo/chain_trace.go:84-89`); `chain_event` has no plan column (`internal/db/chain.go:57-68,185`). origin/main's highest migration is 018, so 019 is next: 019 is right.
5. **Gap 3 is real.** `chainResumeLocked` writes the constant reason `resumeEvent` = `"resumed"` (`internal/relevo/chain_resume.go:76,274`) into an `EventNeedsYou` row whose stored `Step` is the state **before** the resume; `TraceLine.detail()` renders that constant (`internal/chain/trace.go:79-83`). Nothing says where the chain went.
6. **Reported, not fixed:** the security/fixes `BranchDiffPath` is still the builder's round diff at the *closing member's* round number, never the branch diff against `c.Base` (`chain.go:377-381`) — the value stays exactly as it is; the chain directory (plan copies, and now the input copies) is removed by no verb; a resume re-send of a stopped correction or repair round still stages the plan copy.

## Deliverable

- **Gap 1.** Before a seed is sent, every input it names is a path a runner can open: the input's own path when it is a regular file that the store reads as itself; otherwise a copy written under the chain's own directory from `rt.Store.ReadFile`; otherwise the plain clause `not available: <why>`. A seed never names a round_file key. Every seed's first line and its final verdict/findings block are unchanged.
- **Gap 2.** Each `chain_event` row stores the plan it was written on; `show <n> --trace` renders each row's own plan; a row written before 019 (plan 0) falls back to the chain's current plan.
- **Gap 3.** A resume row renders `resumed -> <step>` — the step the resume moved to, in the trace's own step words (`build`, `review`, `correct`, `scan`, `planning`).

## Where the copies go, and why

`<state>/.chains/<chain>/inputs/<source-binding>-<NNN>/<source base name>`, e.g. `.chains/shop/inputs/shop-004/004-diff.patch`. New `Store.ChainInputPath` derives it from the input's own path.

- Every input is a fact about its **source** member round (`Store.bindingRelOf` already resolves any path under the state root to `(binding, round_file name)`), which the seed builder knows; the receiving member's not-yet-sent round is irrelevant, and two seeds naming the same round file (the reviewer's and the correction seed both name the builder's report, diff and plan diff) share exactly one copy, whose bytes cannot fork.
- `.chains/<chain>` already holds the plan copies that seeds name and that live readers read (`chain_start.go:533`), so a path there is a shape readers already open. It is dot-prefixed (`paths.go:301-306`), so no store walk, no seal pass and no binding name can reach it: a named copy stays valid for a whole round, while a binding round file can be sealed and removed mid-round.
- The receiving member's artifact dir is the member's **own** output (`summary.go:18-23`): relevo inputs would mix with what the member wrote, and on a served chain (slice 3) it is what a client pulls back as the reader's artifacts.

An input is "a regular file on disk" only when the store says so: new `Store.DiskRegularFile` (a reserved round-file name is never one — the row is the record — and neither is a symlink or a special file). `plan-diff.patch` joins `reservedRoundFileRe`.

## Scope

Changed: `internal/db/chain.go`, `internal/db/chain_test.go`, `internal/db/testdata/schema.golden`, `internal/store/paths.go`, `internal/store/reserved.go`, `internal/store/reserved_test.go`, `internal/store/chain_test.go`, `internal/chain/{trace.go,trace_test.go}`, `internal/chain/seeds/{reviewer,correction,security,fixes}.md`, `internal/relevo/{chain.go,chain_seed.go,chain_resume.go,chain_trace.go}`, `internal/relevo/{chain_test.go,chain_resume_test.go,chain_trace_test.go}`, `internal/e2e/{headless_test.go,chain_test.go}`.
Created: `internal/db/migrations/019_chain_event_plan.sql`, `docs/plans/2026-10-01-chain-seed-files.md`.
Untouched, deliberately: `internal/chain/chain.go` and `verdict.go` (the pure table), `internal/chain/seeds.go`, `internal/capture`, `internal/git`, `internal/policy`, `internal/ui`, `cmd/relevo`, `internal/remote`, `internal/serve`, `docs/specs/2026-09-30-chains-design.md` (the spec stays the design record). `internal/relevo/chain.go` is 526 lines and is not in `scripts/check-filesize.allow`: put the new code in `chain_seed.go` and keep `chain.go` under 600.

## Seams and behaviour

### 1. The plan column (Gap 2) — `internal/db`, `internal/relevo`

- New `internal/db/migrations/019_chain_event_plan.sql`: `ALTER TABLE chain_event ADD COLUMN plan INTEGER NOT NULL DEFAULT 0;` — add-only, Turso-safe, no backfill, citing `internal/db/migrations/README.md` like 018 does.
- `ChainEventRow.Plan int` (`internal/db/chain.go:57`); `chainEventCols` (`:185`) gains `plan`; `scanChainEvent` (`:208`) scans it; `ChainEventAppend` (`:293-296`) inserts it.
- `chainSaveWithTrace` (`internal/relevo/chain.go:408-422`) sets `Plan: before.Plan` — the plan the row's phase and step already describe.
- `ChainTraceEvent.Plan int` (`internal/relevo/chain_trace.go:34`), filled from `r.Plan` in `ChainTrace` (`:72`); `RenderTrace` (`:81-93`) passes `Plan: e.Plan` when it is non-zero, else `doc.Plan`, and `Plans: doc.Plans` (the total is fixed at start). `--json` therefore carries each row's own plan.

### 2. The resume word (Gap 3) — `internal/chain`, `internal/relevo`

- `internal/chain/trace.go`: new `func (s Step) Word() string` — `building→build`, `reviewing→review`, `correcting→correct`, `scanning→scan`, `planning-fixes→planning`, else `""`; new `func ResumeReason(s Step) string` = `"resumed -> " + s.Word()`. `TraceLine.stateWord()` (`:36-53`) uses `Word()`, so the row's door and the state column speak one vocabulary.
- `internal/relevo/chain_resume.go`: delete the `resumeEvent` constant (`:71-76`); the success event (`:274`) becomes `Reason: chain.ResumeReason(next.Step)`. `next.Step` is already the step the resume moved to (`:243`), and the row keeps storing `before.Step`/`before.Phase`. The failed-send halt path (`:265`) is unchanged.

### 3. The seed inputs (Gap 1) — `internal/store`, `internal/relevo`, `internal/chain/seeds`

- `internal/store/paths.go`: `func (s *Store) ChainInputDir(name string) string` = `<ChainDir>/inputs`; `func (s *Store) ChainInputPath(chain, source string) (string, bool)` = `ChainInputDir/inputs/<binding>-<NNN>/<base of the round_file name>` via `bindingRelOf`, false for a path outside the state root or not a binding round file; `func (s *Store) DiskRegularFile(path string) bool` — `os.Lstat` says a regular file **and** the name is not a reserved round-file name (so a plant at a row-only key is never the record, and a symlink is never opened). `internal/store/reserved.go`: `plan-diff\.patch` joins the alternation; the comment says why.
- `internal/relevo/chain_seed.go`: `func chainSeedInput(rt Runtime, c db.ChainRow, path string) string` — `""`→`""`; `DiskRegularFile`→the path; else read with `rt.Store.ReadFile`, `MkdirAll`+`WriteFile` the bytes at `ChainInputPath(c.Name, path)` (0o644 under 0o755) and return that path; a failed read, a failed `ChainInputPath` or a failed write → `chainSeedMissing(err)`. `func chainSeedMissing(err error) string` renders one path-free line: `"not available: "` plus the underlying reason (`*fs.PathError`'s `Err`, else the error's first line) — the input's own path is never repeated, so nothing in the seed reads as an openable path. `plannerReportPath` is renamed `func memberReportPath(tx *store.Tx, member string, round int) (string, error)` (same body); its two callers move.
- `internal/relevo/chain.go`'s `chainSeedView` (`:344-384`), signature unchanged: every `SeedView` field it sets goes through `chainSeedInput`. For `SeedCorrection`, `OutputPath` comes from `memberReportPath(tx, c.Reviewer, closedRound)`; for `SeedFixes`, from `memberReportPath(tx, c.Security, closedRound)`; a lookup error becomes its `not available:` clause. `SeedSecurity`'s `OutputPath` is unused by its template and stays as it is. The three `memberReportPath` helpers run before the switch where they are needed; no new error path is added — an input that cannot be produced is worded, never a failed close.
- The four templates keep their first line and their verdict/findings block, and obey one contract: **every input line is `<label>: <value>.` with the value as the line's last field**, where the value is an absolute path or a `not available:` clause. That needs three lines reworded: reviewer/correction `Plan: {{.PlanPath}} (plan {{.Plan}} of {{.Plans}}).` → `Plan {{.Plan}} of {{.Plans}}: {{.PlanPath}}.`; the gate line `Check result: {{.GateResult}}; its output is at {{.GateLogPath}}.` → `Check result: {{.GateResult}}; its output: {{.GateLogPath}}.`; security/fixes `Branch diff: {{.BranchDiffPath}} ({{.Branch}} against {{.Base}}).` → the path last, the branch/base sentence kept after it. Empty fields keep today's `{{else}}` wording (`No cumulative plan diff was captured for this plan.`, `No check ran for this round.`).

### 4. The e2e pin — `internal/e2e`

- `internal/e2e/headless_test.go`, `fakeHarnessScript`'s reader branch (`:399-454`): before a chain seed's canned message is printed, the fake opens every path the seed names — for each line of `$plan` (the staged seed) whose text after its last `": "` up to the final `.` starts with `/`, `[ -r "$path" ]` or `echo "fake-claude: seed names a missing input: $path" >&2; exit 3` — no marker, so the round closes without a verdict and the chain halts. The four `case "$seed" in` arms (`:419-444`) each run it; an ordinary reader round is untouched.
- `internal/e2e/chain_test.go`: the reviewer's round-2 seed assertions (`:178-187`) become the copies: the named file is on disk and its bytes equal `chainStaged(rt.Store.DiffPath(...))` / `PlanDiffPath(...)`; the trace assertions add each row's own plan.

## Ordered steps

1. **Migration 019 + the plan plumbing.** Write `019_chain_event_plan.sql`; add `ChainEventRow.Plan`, `chainEventCols`, `scanChainEvent`, `ChainEventAppend`; add `TestMigration019AddsChainEventPlan` and pin the field in `TestChainEventsAppendInSeqOrder`; regenerate the golden. Check: `go test ./internal/db -run 'TestMigration019|TestChainEvents|TestContractSchema' -count=1`, then `go test ./internal/db -count=1`, and `git diff --stat internal/db/testdata` shows only the golden with only its `chain_event` line and `schema_versions` line moving.
2. **Writer + renderer (Gap 2).** `chainSaveWithTrace` writes `before.Plan`; `ChainTraceEvent.Plan`, `ChainTrace` and `RenderTrace` (fallback on 0); `TestChainTraceRowsKeepTheirOwnPlan`, `TestChainTraceRowWithoutAPlanUsesTheChainsPlan`. Check: `go test ./internal/relevo -run 'TestChainTraceRowsKeep|TestChainTraceRowWithout|TestShowTrace' -count=1`.
3. **Step word + resume reason (Gap 3).** `Step.Word`, `ResumeReason`, `stateWord`; delete `resumeEvent`; write `chain.ResumeReason(next.Step)`; `TestResumeReasonNamesTheStepItMovedTo`, the resume case in `traceLineCases`, `TestChainResumeTraceRowNamesTheStepItMovedTo`, and the updated `TestChainResumeWritesOneTraceRow`. Check: `go test ./internal/chain -run 'TestResumeReason|TestTraceLine' -count=1 && go test ./internal/relevo -run 'TestChainResume' -count=1`.
4. **Store: the copy path, the disk record, the reserved key.** `ChainInputDir`, `ChainInputPath`, `DiskRegularFile`; `plan-diff\.patch` reserved; `reservedKeyCases` (and the seal test over the same table) gain the key; `TestChainInputPathKeysACopyByItsSource`, `TestDiskRegularFileRefusesAPlantAtARowOnlyKey`; `chainEventFor`/round trip pin the plan. Check: `go test ./internal/store -run 'TestReadFile|TestStatFile|TestSealRound|TestChainInputPath|TestDiskRegularFile|TestStoreChainRoundTrip' -count=1`.
5. **Resolver, seed view and templates (Gap 1).** `chainSeedInput`, `chainSeedMissing`, `memberReportPath`, the `chainSeedView` field wiring, the four templates. Check: `go test ./internal/chain -run Seed -count=1 && go test ./internal/relevo -run 'TestChainReviewerSeed|TestChainCorrection|TestChainSeed|TestChainPlanDiff' -count=1`.
6. **e2e.** The fake's per-seed path check and the e2e assertions. Check: `make e2e`.
7. **Full check.** `make check`, then `make e2e` again after any fix; fix every reported error before the next run.
8. **The document and the commits.** Write `docs/plans/2026-10-01-chain-seed-files.md` with this plan's text and commit it with the implementation as new commits. Check: `git status` clean, `git log --oneline` shows only new commits, `make check` green at the tip.

## New tests, by name — what each pins

`internal/db`: `TestMigration019AddsChainEventPlan` — 019 adds `chain_event.plan` reading 0 on a row written before it, and a second apply records nothing (19/19).
`internal/store`: `TestChainInputPathKeysACopyByItsSource` — the copy path is `<chainDir>/inputs/<binding>-<NNN>/<base>`, false outside the root and for a chain-dir path; `TestDiskRegularFileRefusesAPlantAtARowOnlyKey` — a plant at `NNN-diff.patch` or `NNN-plan-diff.patch` is not a disk record, a plain `NNN-report.md` is.
`internal/chain`: `TestResumeReasonNamesTheStepItMovedTo` — every step's words, unknown step included.
`internal/relevo`:
- `TestChainReviewerSeedNamesACopyOfTheSealedDiff` — after a closed builder round the reviewer seed names an existing regular file whose bytes equal `ReadFile(DiffPath(builder, round))`, and not the key.
- `TestChainReviewerSeedNamesACopyOfThePlanDiff` — the same for `PlanDiffPath` (with `chainPlanDiffFixtures`).
- `TestChainCorrectionSeedCopiesASealedBuilderReport` — after `tx.SealRound` of the judged round, the correction seed names a copy holding the sealed report's bytes.
- `TestChainReviewerSeedSaysNotAvailableForAnUnreadableGateLog` — a check whose log was never written renders `not available:` for it, names no path for it, and still names the other inputs.
- `TestChainCorrectionSeedNamesTheReviewersOutputFile` — the reviewer's own artifact output path, not the flat `NNN-report.md`.
- `TestChainTraceRowsKeepTheirOwnPlan` — plan 1's rows read `plan 1/2` after the chain advanced to plan 2.
- `TestChainTraceRowWithoutAPlanUsesTheChainsPlan` — a planted plan-0 row renders the chain's current plan.
- `TestChainResumeTraceRowNamesTheStepItMovedTo` — a re-sent build round renders `resumed -> build`, a reviewed manual round `resumed -> review`, in the stored event and the line.
`internal/e2e`: `TestChainE2E` gains the fake's every-named-path check (Gap 1 end to end) and byte-equality assertions on the reviewer seed's diff and plan-diff copies.

## Existing tests that change, and why

1. `internal/chain/trace_test.go` — a resume row joins `traceLineCases` (a case added).
2. `internal/relevo/chain_test.go` — `TestChainReviewerSeedNamesThePlanCumulativeDiff` asserts the copies and their bytes (a row-only key is no longer named); `TestChainReviewerSeedNamesTheCheckThatRan` uses the reworded gate line.
3. `internal/relevo/chain_resume_test.go` — `TestChainCorrectionSeedNamesTheJudgedBuilderRound` asserts the diff/plan-diff copies; `TestChainResumeWritesOneTraceRow` asserts the target step as the reason.
4. `internal/store/reserved_test.go` — `reservedKeyCases` gains the plan-diff key and the seal test walks it (every reserved key pinned once).
5. `internal/store/chain_test.go` — `chainEventFor` carries a plan; the round trip asserts it.
6. `internal/e2e/chain_test.go` — the reviewer round-2 seed's diff and plan-diff assertions become the copies.
7. `internal/db/chain_test.go` — `TestChainEventsAppendInSeqOrder` pins the plan field; the new migration test is added.
No other test is edited, and no test is deleted.

## Deletions (closed list)

1. `resumeEvent` (`internal/relevo/chain_resume.go:71-76`) and its use as the resume row's reason — `chain.ResumeReason` replaces both.
2. `plannerReportPath` (`internal/relevo/chain_seed.go:56`) — renamed `memberReportPath`; no second copy is kept.
3. Naming a round_file key in a seed: the `DiffPath`/`PlanDiffPath` values in the reviewer and correction seeds are the copies now.
4. `RenderTrace`'s unconditional `doc.Plan` for every row — the row's own plan wins, with the 0 fallback for pre-019 rows.
5. The gate line's `its output is at` wording, the reviewer/correction `Plan: <path> (plan i of N).` tail, and the security/fixes branch-diff parenthetical — superseded by the path-final contract.
6. Nothing else; no test is deleted.

## Mutation checks (each names the test that must fail)

| # | gap | mutation | test that must fail |
|---|---|---|---|
| 1 | 1 | make `chainSeedInput` return the input path unchanged (no copy) | `TestChainReviewerSeedNamesACopyOfTheSealedDiff` |
| 2 | 1 | keep the path on a failed read instead of `not available:` | `TestChainReviewerSeedSaysNotAvailableForAnUnreadableGateLog` |
| 3 | 1 | decide "on disk" with a raw `os.Lstat` (a plant at a row-only key counts) | `TestDiskRegularFileRefusesAPlantAtARowOnlyKey` |
| 4 | 1 | resolve the reviewer output from the flat `ReportPath` again | `TestChainCorrectionSeedNamesTheReviewersOutputFile` |
| 5 | 2 | write plan 0 into the row (drop `before.Plan`) | `TestChainTraceRowsKeepTheirOwnPlan` |
| 6 | 2 | render `doc.Plan` for every row again | `TestChainTraceRowsKeepTheirOwnPlan` |
| 7 | 3 | keep `Reason: "resumed"` | `TestChainResumeTraceRowNamesTheStepItMovedTo` |
| 8 | 1 | drop the fake reviewer's path check | `TestChainE2E` (the end-to-end pin) |

## Halt conditions (unverified premises)

1. If `rt.Store.ReadFile` cannot serve a closed round's diff/plan-diff row in the e2e's real state, halt: the copies would all be `not available`.
2. If the e2e fake cannot open a copy under `<state>/.chains/<chain>/inputs/...` at round time, halt: the placement is wrong and the MasterMind must choose the artifact dir; never soften the check.
3. If `git ls-tree origin/main internal/db/migrations/` no longer ends at 018 when the round starts, halt and report the number the plan should use.
4. If a pre-019 `chain_event` row cannot be stored or read as plan 0, halt.
5. If the resume's row is saved with a `next.Step` that differs from the step it sent to, halt: the row would name the wrong target.
6. If making `plan-diff.patch` reserved breaks any existing store or chain test this plan does not list, halt and report.
7. If `make e2e` fails anywhere but the seed path checks and the assertions this plan adds, halt; do not touch the fake harness's builder path.
8. If a package's coverage drops and cannot be brought back with tests, halt; the baseline (`testdata/coverage-baseline.txt`, go1.27 linux/amd64) is never lowered.

## The report must include

- The `relevo` tail (`status`, `changed_paths`, `commands_run`, `not_done`) and the new commits, never amended.
- The mutation table above filled with what actually failed for each mutation, and the focused command run per step.
- `make check` green and the `make e2e` result.
- The seed-vs-code findings: the reader output paths the correction and fixes/security seeds named (with the log-entry path that replaces them), `plan-diff.patch` missing from the reserved names, and the still-open ones (the security/fixes `BranchDiffPath` value, the un-GC'd chain inputs dir, the resume re-send of a correction round).
- Which existing tests changed and why, and that no other test moved.
- The migration 019 and the regenerated golden; the coverage baseline untouched.
