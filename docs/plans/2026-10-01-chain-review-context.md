# Chains · round 1 — the reviewer sees the plan's whole diff, the round's own prompt, and a halt says why

Base: main at `6664e352`. One-shot builder round; new commits only (never amend or rebase anything on the branch); `make check` and `make e2e` green on its own. The round writes this plan to `docs/plans/2026-10-01-chain-review-context.md` and commits it with the implementation.

## Seed vs code (said plainly, not guessed around)

**Gap 1 is real, and its wording needed decoding.** The live chain's only trace row carries `{"Kind":"builder_closed",...,"Outcome":"halted","Gate":"red","Reason":""}` and the action `{"Kind":"halt",...,"Reason":"builder halted on plan 1: "}`: `chainEventFromClose` (`internal/relevo/chain.go:159-185`) builds the event from outcome and gate and never fills `Reason`, so `Next` (`internal/chain/chain.go:219`) formats an empty note. The close already holds the parsed tail and the note at that call site (`reconcile.go:403-442`), so the fix passes both in. "the note" is read as the report entry's close note — the value `reconcile.go` joins from the reject reason, the gate suffix, the injection note and `noreport` — because that is the only "note" in the close's vocabulary, and round 1's own contract says "the caller passes the report's note in `Event.Reason`". "say which" is read as: the one-line reason names the source it took, in the order the seed gives.

**Gap 3's bullet 3 cannot work without a fourth fix, so this round also delivers the planner's plan.** The seed names "(a repair plan, a correction plan, or the human's)" as the prompts of closing rounds, but `chainSeedText` (`internal/relevo/chain.go:277-297`) returns the chain's own plan copy for *every* builder send. Probe on this tree: a chain whose reviewer asked for changes and whose planner wrote a distinctive plan stages that *plan copy* as the builder's next prompt, while the planner's plan sits unread at its report path (`<planner>/NNN-<actor>/plan.md`). Spec §4.3 says the opposite ("correcting | plan artifact present | send it to the builder"), so "name the closing round's own prompt when it is not the plan copy itself" names nothing for a correction round until this is fixed. The round fixes it.

**The correction seed names the closing member's round, not the builder's judged round.** On a reviewer close `chainSeedView` (`internal/relevo/chain.go:304-332`) names the builder's report, diff and gate at `closedRound` — the reviewer's own round. They coincide before a manual round and diverge after one. Probe: after a manual builder round 2 and a `changes` verdict, the correction seed named `001-report.md` and `001-diff.patch`. The seed's own resume bullet is about manual rounds, so this round derives the builder round.

**Found, reported, not fixed.**
- `SeedSecurity` and `SeedFixes` render `BranchDiffPath` as "{{.Branch}} against {{.Base}}" but the value is the closing round's diff (`chain.go:314`), never the branch diff against `c.Base`. Same family as Gap 3, a different seed; it stays byte-identical this round and goes in the report's not-done list.
- A `--resume` re-send of a stopped build round stages the plan copy again, so a stop inside a correction or repair round resumes with the plan, not with the round's own prompt. Pinning that needs per-round provenance; reported, not fixed.
- Chains created before this round have no plan-start commit; no backfill, their seeds simply omit the cumulative line.

**Gap 2 supersedes the spec's example line.** `docs/specs/2026-09-30-chains-design.md:322` shows `check red after regate`; the trace will say `check red`. This plan records the supersession; the spec stays the design record.

## Deliverable

- A builder close that is not `done` carries a one-line reason from its report tail: `halted_at`, then the close note, then the first `not_done` item, each labelled; the outcome word when none is set. Every other halt path is audited and unchanged.
- The trace says `check red` for a red builder gate, whether or not any repair round ran.
- The chain records the commit its current plan started at (add-only column, migration 017); the reviewer and correction seeds name the plan, the closing builder round's own prompt when it differs from the plan copy, the report, the round diff labelled "this round's diff", and the plan's cumulative patch (plan-start commit → the closing round's end tree) stored as a round_file beside the round diff. Correction and fix plans reach the builder. A `--resume` keeps the recorded commit, so a manual round is reviewed against the plan's whole diff.

## Scope

Changed: `internal/chain/{chain,trace}.go`, `internal/chain/seeds/{reviewer,correction}.md`, `internal/chain/{chain,trace,seeds}_test.go`, `internal/capture/capture.go`, `internal/capture/capture_test.go`, `internal/store/paths.go`, `internal/db/chain.go`, `internal/db/chain_test.go`, `internal/db/testdata/schema.golden`, `internal/relevo/{chain,reconcile,chain_start,chain_resume}.go`, `internal/relevo/{chain,chain_start,chain_resume}_test.go`, `internal/e2e/chain_test.go`.

Created: `internal/db/migrations/017_chain_plan_start.sql`, `docs/plans/2026-10-01-chain-review-context.md`.

Untouched, deliberately: `internal/chain/verdict.go` and `Next`'s transition table, `internal/policy`, `internal/ui`, `internal/view`, `cmd/relevo` (no CLI surface changes), `internal/remote` (chains slice 2/3), `docs/specs/2026-09-30-chains-design.md`.

## Pinned behaviour and seams

### 1. The halt reason (`internal/chain`, `internal/relevo`)

- New pure `func BuilderHaltReason(tail reporttail.Tail, note, outcome string) string` in `internal/chain/chain.go`: the first present of `halted_at: <tail.HaltedAt>`, `note: <note>`, `not_done: <tail.NotDone[0]>`, else `status: <outcome>`. The chosen value is cut at its first newline and trimmed before it is labelled; an empty source is skipped, never rendered as an empty label.
- `chainEventFromClose` (`internal/relevo/chain.go:159`) gains `tail reporttail.Tail` and `note string`; for a builder close whose outcome is not `reporttail.OutcomeDone` it sets `ev.Reason = chain.BuilderHaltReason(tail, note, outcome)`. A done close, and every other event kind, is untouched.
- `queueReport` (`reconcile.go:440`) passes the `tail` and the `note` it already holds (the note after the gate suffix, the reject reason and the injection note are joined). A scraped body passes the zero tail and keeps its `scraped` note, exactly as today's parse skip intends.
- Audit (no code change): `EventNeedsYou` carries the sweep's `m.Halt` (always set where the state is set) or the resume's `resumed` / `member <n> could not start` wording; `reviewer gave no verdict`, `reviewer still wants changes after K corrections`, `planner wrote no plan`, `security gave no finding count` and `member <n> could not start` are fixed non-empty strings. The builder close was the only halt that could format empty.

### 2. The trace word (`internal/chain/trace.go:93-101`)

- `gateWord`: `GateRed → "check red"`. Green and none unchanged.

### 3. The plan-start commit (`internal/db`, `internal/store`, `internal/relevo`)

- New `internal/db/migrations/017_chain_plan_start.sql`: `ALTER TABLE chains ADD COLUMN plan_start_commit TEXT NOT NULL DEFAULT ''`, add-only and Turso-safe per `internal/db/migrations/README.md`; no backfill.
- Why a column and not `SettingsJSON`: the value is chain state, not a §5 setting; `chainResumeLocked` re-marshals the settings JSON on every resume and slice 3 ships that same JSON as the create contract, so the commit gets its own column that no settings rewrite can touch.
- `db.ChainRow.PlanStartCommit string`; `chainCols`, `scanChain`, `updateChain` and `insertChain` carry it (it is the table's last column).
- Written when a plan starts: `chainRow` (`chain_start.go:481-522`) sets `PlanStartCommit: base.commit` for plan 1, and `chainApply` sets `c.PlanStartCommit = sent.RoundBaselineHead` after a successful builder send when the transition starts a plan (`before.Step == StepReviewing && next.Plan != before.Plan`) or starts the security phase's fix plan (`before.Step == StepPlanningFixes`). A correction, a repair and a resume re-send change nothing.
- `chainResumeLocked` never writes it: a resume reviews or re-runs the plan the chain already started, which is the seed's "Resume after a manual builder round must use the same plan-start commit".

### 4. The cumulative diff (`internal/store/paths.go`, `internal/capture`)

- `func (s *Store) PlanDiffPath(name string, round int) string` = `<dir>/NNN-plan-diff.patch`, a round_file key like `DiffPath`, never a file on disk.
- `func PlanDiff(ctx context.Context, d Deps, tx *store.Tx, spec PlanDiffSpec) DiffResult` with `type PlanDiffSpec struct { Name string; Round int; Dir, From, End, Path string }` — `RoundDiff`'s twin: `From` is the plan-start commit, `End` the closing round's closed tree; it runs `d.Git.DiffTrees(ctx, spec.Dir, spec.From, spec.End)` and stores the patch with `tx.PutRoundFile(spec.Name, spec.Round, spec.Path, patch)`. It never returns an error: no Git, an empty `From` or `End` (`Reason: "no baseline"`), a git failure, an empty stat and a truncated patch all land in `DiffResult`, exactly as `RoundDiff` does. No new git call, no new git method.

### 5. The seeds (`internal/chain/seeds`, `internal/relevo/chain.go`)

- `chain.SeedView` gains `PlanDiffPath` and `RoundPromptPath`.
- `chainSeedView` (`chain.go:304`) keeps its signature and derives, per seed:
  - `builderRound`: `closedRound` for `SeedReviewer` (the builder closed it), `memberNewestClosedRound(tx, c.Builder)` for `SeedCorrection` (the event is the reviewer's close), unused for `SeedSecurity`/`SeedFixes`.
  - reviewer and correction: `ReportPath = ReportPath(builder, builderRound)`, `DiffPath = DiffPath(builder, builderRound)`, the gate record from `chainRoundGate(tx, builder, builderRound)`, `PlanDiffPath = chainPlanDiff(...)`, `RoundPromptPath = chainRoundPromptPath(...)`.
  - new `chainPlanDiff(rt, tx, c, builder, builderRound) string`: loads the builder; when `c.PlanStartCommit != ""` and `builder.RoundClosedTree != ""` it calls `capture.PlanDiff` at `PlanDiffPath(builder, builderRound)` and returns that key only when a patch was written; `""` otherwise, and it never fails the close.
  - new `chainRoundPromptPath(rt, planPath, builder, builderRound) string`: `PromptPath(builder, builderRound)` when the plan copy cannot be read or its bytes differ from the round's staged prompt, `""` when they are byte-identical (the round ran the plan copy itself).
  - `SeedSecurity`/`SeedFixes` keep today's exact values: `OutputPath = ReportPath(security, closedRound)`, `DiffPath = DiffPath(builder, closedRound)`, `BranchDiffPath = v.DiffPath`.
- `chainSeedText` gains `fromPlanner bool` (`func chainSeedText(rt, tx, c, s, act, closedRound int, fromPlanner bool) (string, error)`): a builder send with it stages the planning member's own plan for `closedRound` — the `Path` of that member's `KindReport` entry, read with `rt.Store.ReadFile` — and errors if the entry or its file is gone; otherwise the chain's plan copy as today. `chainApply` passes `ev.Round` and `ev.Kind == chain.EventPlannerClosed`; `chainResumeLocked` passes its `closedRound` and `false`.
- `seeds/reviewer.md` and `seeds/correction.md`: the first line of each must stay (the e2e fake harness identifies the seed by it). Both name the plan, the round prompt when `RoundPromptPath` is set, the report, `This round's diff: <DiffPath>`, the cumulative line when `PlanDiffPath` is set (`Plan diff, every round of this plan so far: <path>`), and the existing check line; when the cumulative diff was not captured the seed says so rather than naming a missing file. The reviewer's "Read all four" sentence becomes wording that fits the inputs.

## Ordered steps

1. Gap 2: `gateWord` red → `check red`; update the red `want` and its comment in `internal/chain/trace_test.go`. Check: `go test ./internal/chain -run 'TestTraceLine|TestChainTraceLine' -count=1`.
2. Gap 1: `BuilderHaltReason`, `chainEventFromClose`'s two new parameters, the `reconcile.go` call site, and their tests. Check: `go test ./internal/chain -run TestBuilderHaltReason -count=1 && go test ./internal/relevo -run TestChainBuilderHalt -count=1`.
3. Migration 017, the `db.ChainRow` plumbing, `TestMigration017AddsPlanStartCommit`, `assertChainMatches`, and the regenerated `internal/db/testdata/schema.golden` (`go test ./internal/db -run TestContractSchema -update`). Check: `go test ./internal/db -count=1`, and `git diff --stat internal/db/testdata` shows only the golden, with only its `schema_versions` line moving.
4. `store.PlanDiffPath`, `capture.PlanDiff` and its tests. Check: `go test ./internal/capture -run TestPlanDiff -count=1`.
5. Plan-start recording in `chainRow` and `chainApply` and its tests. Check: `go test ./internal/relevo -run 'TestChainPlanStart|TestChainStartRecords' -count=1`.
6. Seeds: `SeedView` fields, `chainSeedView`'s builder round / plan diff / round prompt, `chainSeedText`'s planner source, the two templates, and their tests. Check: `go test ./internal/chain -run Seed -count=1 && go test ./internal/relevo -run 'TestChainReviewerSeed|TestChainCorrection|TestChainResumeReview|TestChainPlanDiff' -count=1`.
7. e2e: the new `TestChainE2E` assertions. Check: `make e2e`.
8. `make check`, then `make e2e` again after any fix; write `docs/plans/2026-10-01-chain-review-context.md` with this plan's text; commit implementation + plan as new commits.

Keep `internal/relevo/chain.go` under the 600-line cap (474 now); a new small file in the package is fine if it does not fit.

## New tests, by name (what each pins)

`internal/chain`:
- `TestBuilderHaltReasonPrefersHaltedAt`, `...FallsBackToTheNote`, `...FallsBackToNotDone`, `...SaysTheOutcomeWhenTheTailIsEmpty` — the four sources, their labels and their order.
- `TestBuilderHaltReasonCapsToOneLine` — a value carrying a newline renders its first line only.
- `TestTraceLineDetailWords` — the red builder row's want (and `TestChainTraceLineFormatsPlanPhaseStepMemberRound`'s) changes to `check red`.
- `TestReviewerSeedNamesThePlanDiffAndTheRoundPrompt`, `TestCorrectionSeedNamesThePlanDiffAndTheRoundPrompt` — the new paths render in both templates; `TestReviewerSeedOmitsAnUnsetPlanDiff` and `TestReviewerSeedOmitsAnUnsetRoundPrompt` — an empty view field renders no path and no dangling line.

`internal/db`:
- `TestMigration017AddsPlanStartCommit` — apply through 016, insert a chains row, apply 017: the column exists, the old row reads `''`; a second apply records nothing.

`internal/capture`:
- `TestPlanDiffWritesThePatchAndComparesStartWithEnd` — `lastDiffFrom`/`lastDiffTo` are the spec's, and the round_file key holds the patch.
- `TestPlanDiffReportsUnavailableWithoutBothEnds` — an empty `From` or `End` is `Available:false` with a reason and no git call.

`internal/relevo`:
- `TestChainBuilderHaltCarriesTheReportTailReason` — a halted body with `halted_at` gives the chain row, the trace row and the end delivery `builder halted on plan 1: halted_at: <step>`.
- `TestChainBuilderHaltReasonSources` — the note, the first `not_done` item and the outcome fallback, end to end.
- `TestChainReviewerSeedNamesThePlanCumulativeDiff` — with a fake tree and patch: the staged reviewer prompt names both this round's diff and the cumulative diff, and `rt.Store.ReadFile(PlanDiffPath(builder, 1))` holds the patch.
- `TestChainStartRecordsThePlanStartCommit` — a fresh chain's row carries the commit the worktree was cut from.
- `TestChainPlanDiffStartsAtThePlanStartCommit` — a two-plan chain whose head moves between sends: plan 2's review diffs from the head recorded at plan 2's send, not from the closing round.
- `TestChainPlanStartCommitResetOnlyOnNewPlans` — a correction round leaves the row's commit alone; a reviewer pass and a security fix-plan send move it to the sent round's baseline head.
- `TestChainCorrectionRoundRunsThePlannersPlan` — after `changes`, the builder's next staged prompt is the planner's own plan text, not the plan copy.
- `TestChainReviewerSeedNamesTheRoundPromptOfACorrection` — that round's reviewer seed names `PromptPath(builder, round)`.
- `TestChainReviewerSeedOmitsThePlanCopyAsRoundPrompt` — plan 1's own round names no round-prompt path.
- `TestChainCorrectionSeedNamesTheJudgedBuilderRound` — the manual-round resume: after the reviewer asks for changes, the correction seed names the builder round the reviewer judged (report, diff, prompt, cumulative).
- `TestChainResumeReviewKeepsThePlanStartCommit` — a manual round's review uses the stored commit and the manual round's closed tree.
- `TestChainResumeReSendKeepsThePlanStartCommit` — a stopped build round, resumed, keeps the stored value.

`internal/e2e` (`TestChainE2E` gains):
- the reviewer's round-2 seed names the cumulative plan diff and the round diff; the correction planner's seed names the builder's round-2 report; the builder's round-2 prompt is the planner's correction plan.

## Existing tests that change, and why

1. `internal/chain/trace_test.go` — the red want and its comment (`check red after regate` → `check red`); Gap 2's own pin.
2. `internal/chain/seeds_test.go` — `sampleSeedView` gains the two fields; the reviewer and correction assertions gain the new paths; the no-check cases keep working.
3. `internal/db/chain_test.go` — `testChain` and `assertChainMatches` compare the new column so the round trip pins it.
4. `internal/db/testdata/schema.golden` — its `schema_versions` line only.
5. `internal/e2e/chain_test.go` — the new assertions; no existing assertion moves.

No other test is edited, and none is deleted.

## Deletions (closed list)

1. `gateWord`'s `check red after regate` wording (`internal/chain/trace.go:98`).
2. `chainSeedText`'s unconditional "every builder send reads `paths[s.Plan-1]`" branch; the plan copy becomes the fallback for plan sends only.
3. `chainSeedView`'s use of the closing member's round for the builder's report, diff and gate when the closing member is not the builder.
4. Nothing else; no test is deleted.

## Mutation checks (each names the test that must fail)

| # | behaviour | mutation | test that must fail |
|---|---|---|---|
| 1 | a halted builder close carries its report's reason | drop the `ev.Reason` assignment | `TestChainBuilderHaltCarriesTheReportTailReason` |
| 2 | the sources are tried halted_at → note → not_done → outcome | try the note first | `TestBuilderHaltReasonPrefersHaltedAt` |
| 3 | the reason is one line | keep the newline | `TestBuilderHaltReasonCapsToOneLine` |
| 4 | a red gate reads `check red` | restore `check red after regate` | `TestChainTraceLineFormatsPlanPhaseStepMemberRound` |
| 5 | a plan start is recorded | drop the `PlanStartCommit` writes | `TestChainStartRecordsThePlanStartCommit`; `TestChainPlanStartCommitResetOnlyOnNewPlans` |
| 6 | a correction keeps the plan start | reset the commit on every builder send | same test, correction case |
| 7 | the reviewer seed names the cumulative diff | skip the `capture.PlanDiff` call | `TestChainReviewerSeedNamesThePlanCumulativeDiff` |
| 8 | the cumulative diff runs from the plan start | diff from the closing round's baseline | `TestChainPlanDiffStartsAtThePlanStartCommit` |
| 9 | a round prompt is named only when it is not the plan copy | name it always | `TestChainReviewerSeedOmitsThePlanCopyAsRoundPrompt` |
| 10 | a correction round runs the planner's plan | keep the plan-copy branch | `TestChainCorrectionRoundRunsThePlannersPlan` |
| 11 | the correction seed names the judged builder round | use `closedRound` again | `TestChainCorrectionSeedNamesTheJudgedBuilderRound` |
| 12 | a resume review keeps the stored commit | recompute at the resume | `TestChainResumeReviewKeepsThePlanStartCommit` |

## Halt conditions (unverified premises)

1. If a plan-start commit is not accepted as the `from` argument of the existing `DiffTrees` (which runs `git diff <from> <to>`) in a real repository, halt and report; `internal/git` is not this round's.
2. If `RoundBaselineHead` is empty on a plan send in a real git runtime, halt: the cumulative diff would silently vanish, and choosing a different start is not this round's call.
3. If `tx.PutRoundFile` refuses the `NNN-plan-diff.patch` key, halt and report; do not write a plain file into the state directory.
4. If `PlanStartCommit` does not survive a repair tick's `ChainPut` or a terminal tick's save, halt.
5. If the correction-delivery change makes `make e2e` fail anywhere but the assertions this plan adds, halt and report; do not soften the fake harness's builder path.
6. If the security and fixes seeds' named paths move at all, halt: they are out of scope and stay byte-identical.
7. If a chain row with an empty `plan_start_commit` makes a seed fail instead of omitting the cumulative line, halt.
8. If the coverage gate drops for a package and cannot be brought back with tests, halt and report; the baseline is never lowered.


## Gap 4 (added by the MasterMind): `wait` on a halted chain whose builder has an open round

Found in the same live run. The builder member shares the chain's name, so `relevo wait --name <chain>`
always resolves to the chain (`cmd/relevo/wait.go`, the chain branch before `relevo.Wait`). With the chain
halted or stopped, a human who sent a manual round to the builder cannot wait on it: `WaitChain` returns the
old halt line at once (exit 3).

Rule: when the chain's status is not `running` **and** the builder member (`c.Builder`) has an open round
(a prompt entry for its current round and no report entry for it yet, or a queued round), `wait --name
<chain>` waits on that builder round exactly as `relevo.Wait` does for a binding (same exit codes and
payload). Otherwise the chain arm stays as it is. A running chain always waits on the chain.

- Put the decision in `internal/relevo` as a pure-ish helper (for example `ChainWaitTarget(rt, name) (binding string, chain bool, err error)`) so `cmd/relevo/wait.go` only branches on it.
- Tests: `TestWaitChainHaltedWithAnOpenBuilderRoundWaitsOnTheRound` (store-seeded: halted chain, builder with an open round -> the binding path is chosen), `TestWaitChainHaltedWithNoOpenRoundReturnsTheHalt` (unchanged behaviour), `TestWaitChainRunningAlwaysWaitsOnTheChain`. CLI tests stay store-only (no harness, no network).
- Mutation: always choose the chain arm -> `TestWaitChainHaltedWithAnOpenBuilderRoundWaitsOnTheRound` fails.
- Scope additions for this gap only: `cmd/relevo/wait.go`, `internal/relevo/chain_wait.go` and its test, `cmd/relevo/chain_wait_test.go`.

## Remote builders (out of scope, said anyway)

On a server chain (slice 3) the members run beside the builder's tree, so the same `capture.PlanDiff` reads the plan-start commit from the server's git exactly as here; a client that pulls the chain back holds the builder's commits under `relevo/<n>`, so it would read the commit as the fetched ref there. This round adds no wire contract, and no member of a remote chain is planned here.

## The report must include

- The `relevo` tail (`status: done`, `changed_paths`, `commands_run`, `not_done`) and the new commits, never amended.
- The mutation table above, filled with what actually failed for each mutation, and the focused commands run per step.
- `make check` green and the `make e2e` result.
- The seed-vs-code findings: the correction and fix plans never reaching the builder (with the probe's output), the correction seed naming the reviewer's round after a manual round, the two reported-not-fixed items (the security/fixes `BranchDiffPath`, the resume re-send after a correction or repair stop), and the `check red` supersession of the spec's example line.
- Which existing tests changed and why, and that no other chain test moved.
- The migration and the regenerated golden; the coverage baseline untouched.
