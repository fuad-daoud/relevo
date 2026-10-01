# Chains · one builder round — the reviewer judges the plan, a verdict survives a recap, gates see lint

Base: main at `55bf3655` or later (`git log -1 --oneline` must show `55bf3655` or a descendant). One-shot builder round: `make check` and `make e2e` green on the branch's own commits. New commits only — never amend or rebase a commit already on a binding's branch. The round writes this plan to `docs/plans/2026-10-01-chain-judge-the-plan.md` and commits it with the implementation (last step). Change no file in a tree other than the round's own worktree.

## Seed vs code (said plainly, not guessed around)

All three gaps are real in this tree; the seams and the readings the seed leaves open:

1. **Gap 1.** The reviewer seed (`internal/chain/seeds/reviewer.md:6`) labels `This round's diff: {{.DiffPath}}.` and the closing sentence says "judge the diff against the plan"; nothing says the reviewer judges the plan as a whole, and `chainSeedView` (`internal/relevo/chain.go:336-413`) already carries the plan copies, the builder's report/diff/prompt, the gate, the cumulative diff and the closing-round gate. The cumulative diff already exists (#779) but is presented as a peer of the round diff. #781 already made every named input a real file (`chainSeedInput`, `internal/relevo/chain_seed.go:74-106`), so the new lines go through it.
2. **The plan's first builder round.** The seed's "the closing builder round is not the plan's first builder round" needs a boundary. **The chain's plan copy is staged exactly once per plan — as that plan's first builder round's prompt.** `chainSeedText` (`chain.go:299-334`) hands the builder the plan copy only for a start/advance send (`s.Plan`); a correction or fix round gets the planner's own plan (`fromPlanner`), a repair round gets `repairPlan` (`repair.go:130-137`), a human round gets the human's file. So a round whose staged prompt bytes differ from the current plan copy is not the plan's first, and the plan's first builder round is the first round going backwards whose staged prompt equals that copy. This is the same predicate `chainRoundPromptPath` (`chain_seed.go:47-55`) already computes; no migration and no new column are needed.
   - Honest edge, reported not fixed: a `--resume` re-send of a builder round stopped mid-correction or mid-repair stages the plan copy again (the #779 "reported, not fixed" item), so that round reads as the plan's first and the seed omits the kind line. It is named in the report's not-done list.
3. **Gap 2 is real.** `writeReaderSummary` (`internal/relevo/summary.go:38-73`) writes the reader output from `transcript.FinalText` — the *last* assistant text (`internal/transcript/final.go:9-40`). `chainEventFromClose` then reads `chain.ParseVerdict(body)`/`ParseFindings(body)` from that file (`chain.go:181,189`), so a recap after the block is the only body and the chain halts "reviewer gave no verdict". #780's `blockValue` reads from the last block *within one message*; it cannot see an earlier message. The reader prompt (`send.go:70-87`) tells the runner "Your final message comes after it", which is what produced the recap.
4. **Gap 2's transcript helper.** `internal/transcript` decodes harness messages in `finalCandidates` (`final.go:42-71`), used by `FinalText`. No helper enumerates messages, so one is added there and reused; the wiring never re-implements harness shapes.
5. **Gap 3 is real.** `dist/relevo.service:14` and `dist/relevo-serve.service:12` pin `Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin`. `make lint` skips quietly when `golangci-lint` is absent (`Makefile:15-28`), and CI installs it to `$(go env GOPATH)/bin` (`.github/workflows/ci.yml:97-98`), whose default is `%h/go/bin`. `scripts/relevo-service-template_test.sh` is run by `make check-scripts` but pins only the `[Service]` memory/OOM/start-limit keys, never PATH, and only `dist/relevo.service`.

## Deliverable

- **Gap 1.** The reviewer and correction seeds say the reviewer judges plan i as a whole against the plan text: the plan's cumulative diff is the primary input and this round's diff is its latest increment. When the closing builder round is not the plan's first, the seed names what it is (a repair round after a red check, a correction round, a fix-plan round, or a round a human sent) and the round it sits on top of, and lists every builder round of the plan with its prompt and report. When the cumulative diff was not captured, the seed names the commit to diff from (the plan-start commit, else the chain base for plan 1) and tells the reviewer to diff from it; it never presents a single round's diff as the plan's.
- **Gap 2.** A reviewer or security verdict/findings is read from the round's output body when it carries the key, and otherwise from the round's stream, newest assistant message first, reusing `internal/transcript`'s decoding. The reader prompt says the message carrying the relevo block(s) must be the last text the runner writes and that the done marker is its final action, with nothing written after.
- **Gap 3.** Both shipped unit templates carry `%h/go/bin` on `PATH`; a Go test and the service-template script pin it. **No installed unit is changed or reinstalled.**

## Scope

Changed: `internal/chain/chain.go`, `internal/chain/seeds/{reviewer,correction}.md`, `internal/chain/seeds_test.go`, `internal/transcript/final.go`, `internal/transcript/final_test.go`, `internal/relevo/{chain.go,chain_seed.go,send.go}`, `internal/relevo/{chain_test.go,send_test.go}`, `internal/e2e/{headless_test.go,chain_test.go}`, `cmd/relevo/serve_unit_test.go`, `dist/relevo.service`, `dist/relevo-serve.service`, `scripts/relevo-service-template_test.sh`.

Created: `docs/plans/2026-10-01-chain-judge-the-plan.md`.

Untouched, deliberately: `internal/chain/verdict.go`, `internal/chain/chain.go`'s state machine, `internal/chain/seeds/{security,fixes}.md` (byte-identical), `internal/reporttail`, `internal/relevant`'s table, `internal/db` and migrations (no schema change), `internal/capture`, `internal/git`, `internal/policy`, `internal/ui`, `internal/remote`, `internal/serve`, `docs/specs/2026-09-30-chains-design.md` (the design record stays). `internal/relevo/chain.go` is 556 lines and is not in `scripts/check-filesize.allow`: keep the new computation in `chain_seed.go` (and a small new file if needed) so `chain.go` stays under 600.

## Seams and behaviour

### 1. The seed view (`internal/chain/chain.go:166-175`)

- New `type SeedRound struct { Round int; PromptPath, ReportPath string }`.
- `SeedView` gains: `BuilderRounds []SeedRound`; `BuilderRoundKind string`; `BuilderRoundOn int`; `DiffFrom string`.
- New kind words as constants beside the member names: `BuilderRoundRepair = "a repair round after a red check"`, `BuilderRoundCorrection = "a correction round"`, `BuilderRoundFix = "a fix-plan round"`, `BuilderRoundHuman = "a round a human sent"`. `BuilderRoundKind` holds one of these; `""` means the closing round is the plan's first.

### 2. The templates (`internal/chain/seeds/reviewer.md:1-17`, `correction.md:1-14`)

- The first line of each stays byte-for-byte (the e2e fake recognises a seed by it): `Review the round and give a verdict.` and `Write a correction plan for the builder.`
- Both templates replace the closing "judge the diff against the plan" sentence with the framing sentence: the reviewer judges plan `{{.Plan}} of {{.Plans}}` as a whole against the plan text, the cumulative diff is the primary input, and this round's diff is its latest increment.
- The cumulative-diff block becomes `{{if .PlanDiffPath}}Plan diff, every round of this plan so far: {{.PlanDiffPath}}.` / `{{else if .DiffFrom}}No cumulative plan diff was captured; diff the plan yourself from {{.DiffFrom}}.` / `{{else}}No cumulative plan diff was captured for this plan.{{end}}`. The `else` sentence stays exact (`TestReviewerSeedOmitsAnUnsetPlanDiff` pins it).
- When `BuilderRoundKind` is set: one line `This closing round is {{.BuilderRoundKind}}, on top of round {{.BuilderRoundOn}}.` followed by `Builder rounds of this plan:` and, `{{range .BuilderRounds}}`, one line per path so the e2e fake's per-line path check sees both: `- round {{.Round}} prompt: {{.PromptPath}}` and `- round {{.Round}} report: {{.ReportPath}}`.
- `This round's diff: <path>.`, the gate line/`No check ran for this round.`, the verdict/findings blocks and every other line are unchanged.

### 3. The wiring (`internal/relevo/chain_seed.go`, `chain.go:336-413`)

- New `chainBuilderPlanView(rt, tx, c, s, act, builder, builderRound) (kind string, on int, rounds []chain.SeedRound, diffFrom string)` in `chain_seed.go`, called by `chainSeedView` for `SeedReviewer`/`SeedCorrection` only:
  - `planCopy := chainPlanPaths(c)[s.Plan-1]`; compare `rt.Store.ReadFile(PromptPath(builder, r))` with `rt.Store.ReadFile(planCopy)` for each `r` walked down from `builderRound`. The largest `r` whose prompt equals the copy is the plan's first round.
  - `builderRound == first` (or no match) → kind `""`, no rounds; `first < builderRound` → kind and `on = builderRound - 1`, rounds = every round in `[first, builderRound]` with `PromptPath`/`ReportPath` through `chainSeedInput`.
  - Kind: the builder's `KindPrompt` entry for `builderRound` (`tx.ReadLog`) whose `Note` begins `repair ` → `BuilderRoundRepair`; else the builder's staged prompt bytes equal `memberReportPath(tx, c.Planner, memberNewestClosedRound(tx, c.Planner))` → `BuilderRoundCorrection`, or `BuilderRoundFix` when `s.Phase == chain.PhaseSecurity`; else `BuilderRoundHuman`.
  - `diffFrom = c.PlanStartCommit`, else `c.Base` when `s.Plan == 1`, else `""`.
- `chainSeedView` sets the four fields from this helper for `SeedReviewer`/`SeedCorrection`; `SeedSecurity`/`SeedFixes` stay exactly as they are (the two templates render no new fields).

### 4. The transcript helper (`internal/transcript/final.go:9-71`)

- New `func Texts(kind string, stream []byte) []string`: every message text `FinalText`'s decoding already recognises, in stream order — per JSON line, the same `finalCandidates(kind, obj)` values (`last` preferred, `fallback` when `last` is empty), skipping non-JSON and empty ones. `FinalText` keeps its current behaviour and its body is refactored onto the same `finalCandidates` call.

### 5. The event build (`internal/relevo/chain.go:154-192`)

- New helpers in `chain_seed.go` (or a small new `chain_body.go`): `chainReaderVerdict(rt, b, body)` and `chainReaderFindings(rt, b, body)` — if `chain.ParseVerdict(body)`/`chain.ParseFindings(body)` carries the key, return it; else read `rt.Store.StreamPath(b.Name, b.Round)` with `rt.Store.ReadFile`, take `transcript.Texts(lastStreamKind(b), stream)`, walk it newest-first, and return the first non-empty parse. A missing stream, an unreadable stream and a message that parses to nothing all fall through to "" / not-given.
- `chainEventFromClose` uses the two helpers for `MemberReviewer`/`MemberSecurity`; every other part and path is unchanged. `b.Round` is still the closing round at this call site (`reconcile.go:440`, before the advance at `:605`).

### 6. The reader prompt (`internal/relevo/send.go:61-87`)

- `readerPrompt` keeps its first line, the `Read:` line, `Your final message is your %s: it is saved as %s.`, the block, and a `create this empty file: %s` line (the e2e awk at `headless_test.go:378-380` reads both). It drops `Your final message comes after it: relevo saves it once you finish.` and instead says the message carrying the block must be the last text the runner writes; the done marker is its final action, created after it, and nothing is written after the marker.
- `internal/relevo/headless.go:1134-1139`'s comment ("the final message comes after the marker") is reworded to the close's real reason: a reader closes on its runner's exit, not on the marker, because the runner may have written its output near the end. No behaviour changes there.

### 7. The e2e pin (`internal/e2e/headless_test.go:443-483`, `chain_test.go:168-323`)

- The fake's reviewer branch (`:446-461`) keeps its round counter and first-line match; it prints the block-bearing `type:"result"` line, then a second `type:"result"` line whose result is a recap with no relevo block. `FinalText` then returns the recap (the last fallback), so the reviewer's written output excludes the verdict, and `transcript.Texts` returns both messages for the rescan.
- `TestChainE2E` gains: the reviewer's second output file contains the recap marker and no `verdict:` block, and the trace's reviewer verdict for that row is still `pass` and the chain ends `done/finished` (i.e. the verdict was read from the stream). Its existing plan-1/plan-2, correction and finish assertions do not move.

### 8. The units (`dist/relevo.service:14`, `dist/relevo-serve.service:12`, `scripts/relevo-service-template_test.sh`)

- Both `Environment=PATH=` lines become `%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin`.
- The script asserts, for both templates, exactly one `Environment=PATH=` line and that it contains `%h/go/bin`; the existing `[Service]` memory/OOM/start-limit checks and the optional single-template argument stay.
- New `TestServiceUnitsCarryTheGoBinOnPath` (`cmd/relevo/serve_unit_test.go`, which already reads `../../dist/relevo-serve.service`) reads `dist/relevo.service` and `dist/relevo-serve.service`, finds each `Environment=PATH=` line, and fails unless each names `%h/go/bin`.

## Ordered steps

1. **Chain view and kinds.** `SeedRound`, the `SeedView` fields, the four kind constants; extend `sampleSeedView`; add `TestReviewerSeedFramesThePlanAndListsTheRounds`, `TestReviewerSeedNamesTheDiffFromWithoutAPlanDiff`, `TestCorrectionSeedFramesThePlan`. Check: `go test ./internal/chain -run Seed -count=1`.
2. **Templates.** Reviewer and correction wording per §2. Check: `go test ./internal/chain -run Seed -count=1`.
3. **Wiring.** `chainBuilderPlanView` and the `chainSeedView` call for the two seeds; add `TestRepairRoundSeedFramesThePlanAndListsEveryBuilderRound`, `TestChainReviewerSeedNamesTheCorrectionRoundKind`, `TestReviewerSeedNamesTheDiffFromWhenNoPlanDiffWasCaptured`. Check: `go test ./internal/relevo -run 'TestRepairRoundSeed|TestReviewerSeed|TestChainReviewerSeed' -count=1`.
4. **Transcript.** `Texts` and `TestTextsReturnsEveryMessageInOrder`. Check: `go test ./internal/transcript -run TestTexts -count=1`.
5. **Event build.** `chainReaderVerdict`/`chainReaderFindings` and the review/security arms; add `TestReviewerRecapAfterTheBlockStillYieldsTheVerdict`, `TestSecurityRecapAfterTheBlockStillYieldsTheFindings` (output file = recap, stream = the block-bearing message then the recap; reconcile and assert the transition happened). Check: `go test ./internal/relevo -run 'TestReviewerRecap|TestSecurityRecap' -count=1`.
6. **Reader prompt.** New wording; add `TestComposePromptReaderPutsTheBlockMessageLast` (the block message is the last text, the marker the final action, and no "comes after" sentence); reword the `headless.go` comment. Check: `go test ./internal/relevo -run TestComposePromptReader -count=1`.
7. **e2e.** The fake's second reviewer line and the `TestChainE2E` assertions. Check: `make e2e`.
8. **Units.** Both templates, the script's PATH assertion, `TestServiceUnitsCarryTheGoBinOnPath`. Check: `sh scripts/relevo-service-template_test.sh && go test ./cmd/relevo -run TestServiceUnitsCarryTheGoBinOnPath -count=1`.
9. **Mutate, then revert.** The table below, each mutation reverted before the next; each named test must fail under its mutation and pass again after the revert. Check: the focused command named in the row.
10. **Full, then commit.** `make check`, then `make e2e` again after any fix; write `docs/plans/2026-10-01-chain-judge-the-plan.md` with this plan's text and commit it with the implementation as new commits. Check: `git status` clean, `git log --oneline` shows only new commits, `make check` green at the tip.

## New tests, by name — what each pins

`internal/chain` (`seeds_test.go`): `TestReviewerSeedFramesThePlanAndListsTheRounds` — the framing sentence, the kind line and both paths of every listed round render in the reviewer template; `TestReviewerSeedNamesTheDiffFromWithoutAPlanDiff` — an empty `PlanDiffPath` with `DiffFrom` set renders the diff-from-the-commit sentence, names no `Plan diff,` line, and never presents the round diff as the plan's; `TestCorrectionSeedFramesThePlan` — the correction template carries the same framing and list.

`internal/transcript` (`final_test.go`): `TestTextsReturnsEveryMessageInOrder` — claude assistant texts and result fallbacks come back in stream order, non-JSON and the trailer are skipped, and an empty stream is empty.

`internal/relevo` (`chain_test.go`, `send_test.go`): `TestRepairRoundSeedFramesThePlanAndListsEveryBuilderRound` — a red gate repairs round 1 then closes red after the budget; the reviewer seed for round 2 carries the framing, `a repair round after a red check`, `on top of round 1`, and rounds 1 and 2 with their prompt and report as openable paths; `TestChainReviewerSeedNamesTheCorrectionRoundKind` — after a correction round the seed says `a correction round`; `TestReviewerSeedNamesTheDiffFromWhenNoPlanDiffWasCaptured` — no cumulative diff: the seed names `PlanStartCommit` (plan 1: `c.Base`) and tells the reviewer to diff from it; `TestReviewerRecapAfterTheBlockStillYieldsTheVerdict` — output file is a block-free recap, stream holds the block then the recap, the chain advances; `TestSecurityRecapAfterTheBlockStillYieldsTheFindings` — the same for `findings`; `TestComposePromptReaderPutsTheBlockMessageLast` — the prompt pins the block message as the last text and the marker as the final action, and drops the "comes after" sentence.

`internal/e2e` (`chain_test.go`): `TestChainE2E` gains the recap pin above.

`cmd/relevo` (`serve_unit_test.go`): `TestServiceUnitsCarryTheGoBinOnPath` — both shipped units name `%h/go/bin` on `PATH`.

`scripts/relevo-service-template_test.sh`: the PATH assertion for both units (shell test under `make check-scripts`).

## Existing tests that change, and why

1. `internal/chain/seeds_test.go` — `sampleSeedView` gains the four fields, and the reviewer/correction assertions gain the new lines (this change's own pin).
2. `internal/relevo/send_test.go` — `TestComposePromptReaderNamesTheOutputFile` keeps its output-file assertion and gains the marker-last assertion (or the new test replaces the trailing-sentence assumption).
3. `internal/e2e/chain_test.go` — the reviewer round-2 seed assertions gain the kind and the round list; `TestChainE2E` gains the recap assertion. No existing assertion is removed.
4. `internal/e2e/headless_test.go` — only the fake reviewer's message lines; the round counter and every seed first line stay.

No other test is edited, and no test is deleted.

## Deletions (closed list)

1. `readerPrompt`'s `Your final message comes after it: relevo saves it once you finish.` (`internal/relevo/send.go:87`) — replaced by the marker-last wording.
2. The reviewer and correction seeds' closing `judge the diff against the plan` sentence (`seeds/reviewer.md:11-12`, `seeds/correction.md:12-14`) — replaced by the plan framing.
3. The body-only verdict/findings read as the only source (`internal/relevo/chain.go:181,189`) — the output body becomes the first attempt, not the only one.
4. `headless.go:1135`'s "the final message comes after the marker" comment rationale — reworded to the close-on-exit reason.
5. Nothing else. No test is deleted, and the security/fixes seeds stay byte-identical.

## Mutation checks (each names the test that must fail)

| # | gap | mutation | test that must fail |
|---|---|---|---|
| 1 | 1 | leave `BuilderRounds`/`BuilderRoundKind`/`DiffFrom` unset in `chainSeedView` | `TestRepairRoundSeedFramesThePlanAndListsEveryBuilderRound`; `TestReviewerSeedNamesTheDiffFromWhenNoPlanDiffWasCaptured` |
| 2 | 2 | drop the stream fallback from the reviewer/security event build | `TestReviewerRecapAfterTheBlockStillYieldsTheVerdict`; `TestChainE2E` (the chain halts "reviewer gave no verdict") |
| 3 | 3 | remove `%h/go/bin` from `dist/relevo.service` | `TestServiceUnitsCarryTheGoBinOnPath`; `scripts/relevo-service-template_test.sh` |

## Halt conditions (unverified premises)

1. If `transcript.Texts` cannot return a message `FinalText`'s decoding sees (the rescan needs a shape it drops), halt and report; do not fork the decoding.
2. If no builder round's staged prompt equals the current plan copy in a real chain, so the plan boundary cannot be found, halt; do not guess a round.
3. If the e2e fake's second stream line makes `FinalText` return the block-bearing message instead of the recap, halt: the Gap 2 pin would be vacuous.
4. If `go env GOPATH` for the account the daemon unit runs as is not `%h/go` (so `%h/go/bin` is the wrong directory), halt and report before editing the units.
5. If a unit's `Environment=PATH=` line differs from the exact text above, halt rather than invent one.
6. If `make e2e` fails anywhere but the fake reviewer's message and the assertions this plan adds, halt; do not touch the fake's builder path or the close.
7. If an existing reader-prompt, chain-seed or service-unit test not listed above fails, halt and report rather than edit it.
8. If a package's coverage drops and cannot be brought back with tests, halt; `testdata/coverage-baseline.txt` is never lowered.

## The report must include

- The `relevo` tail (`status`, `changed_paths`, `commands_run`, `not_done`) and the new commits, never amended.
- The mutation table above filled with what actually failed for each mutation, and the focused command run per step.
- `make check` green and the `make e2e` result.
- Which existing tests changed and why, and that no other chain/transcript/reader test moved.
- The seed-vs-code reading of "the plan's first builder round" (the plan-copy predicate), and the not-done items: the `--resume` re-send of a stopped correction/repair round reading as the plan's first, and that a chain started before #779 without `PlanStartCommit` on a later plan can name no commit to diff from.
- That no installed unit was changed or reinstalled: only `dist/*.service` and their pins were edited; the human reinstalls with `make service` when ready.
- Coverage: `scripts/check-coverage.sh` passed and the baseline was untouched; no migration was added.
