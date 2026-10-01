
Base: main at `0ac52a80` (37a669e6 + chains slice 2, #785; re-check the line numbers below against it). One-shot builder round; **new commits only** — never amend or rebase a commit already on a binding's branch. `make check` and `make e2e` green on the round's own commits. This round writes its plan to `docs/plans/2026-10-01-chain-security-diff.md` and commits it with the implementation (last step).

## Seed vs code (verified on this tree, not guessed)

1. **The bug is exactly where the brief says, and wider than "one line".** `chainSeedView`'s `SeedSecurity`/`SeedFixes` arm (`internal/relevo/chain.go:405-413`) sets `v.DiffPath = chainSeedInput(rt, c, rt.Store.DiffPath(c.Builder, closedRound))` and `v.BranchDiffPath = v.DiffPath`; `closedRound` is the *closing reader's* round (`ev.Round` from `chainApply`), i.e. the reviewer's round for a security send and the security member's round for a fixes send. The security template then prints `{{.Branch}} against {{.Base}}`, a branch-vs-base sentence about a value that is one builder round's patch keyed to an unrelated member's round. On turso-466 (18 commits, 92 files) that is the one-line diff the scan read.
2. **The spec already agrees with the fix.** `docs/specs/2026-09-30-chains-design.md:171-172` says security gets "the branch diff against the chain's base" and the fix planner "the security output and the branch diff". This round implements the spec; the spec file stays untouched.
3. **No migration.** The patch is a new round_file key written with `tx.PutRoundFile` (the pattern `PlanDiffPath`/`capture.PlanDiff` established) and read back through the store like any diff. No `chains` column, no schema change, no backfill: a chain already running captures a fresh patch at its next security/fixes send, so there is nothing to migrate. If the builder finds a store path that refuses the key, halt rather than adding a column.
4. **Sizes.** `internal/relevo/chain.go` is 558 lines (cap 600) and `chainSeedView` is 67 lines (style cap 70). The new helpers go in `chain_seed.go` (280 lines), and `chainSeedView` sheds lines rather than growing: its plan-path block moves into a helper, its security/fixes arm loses the three dropped assignments.
5. **No goldens.** `internal/chain` has no `testdata/` and the seed tests are substring assertions (`internal/chain/seeds_test.go`); the e2e fake identifies each seed by its first line. So there are no template goldens to regenerate, and the first lines of `security.md` and `fixes.md` are frozen.
6. **The e2e can pin the span cheaply, yes.** Today the fake builder writes the same one-line file every round, so `base→head` and `round-N→head` produce the *same net patch*; no assertion could tell them apart. The fake builder must append a round-numbered line to `fake-round.txt` (one shell line, read by no other test) so the whole diff carries every round and a one-round diff cannot masquerade as it. The fake's `check_seed_inputs` already opens every path line a chain seed names, so the new lines are covered without touching that check; the Go test does the byte/span assertions after the run. No runner logic is added.

## Behaviour and the cases

1. **Whole diff.** At a `SeedSecurity` send (reviewer passed, build phase → scanning) and at a `SeedFixes` send (security found > 0 → planning-fixes), `BranchDiffPath` is the seed copy of a new row-only key `NNN-chain-diff.patch` on the builder's **newest closed round** N, holding `git diff <c.Base> <builder.RoundClosedTree>`. `c.Base` is the commit the builder's worktree was cut from (`chain_start.go` sets `Base: base.commit`), so N=1..18 reads as the whole chain, not one round.
2. **End is `b.RoundClosedTree`, not worktree HEAD** (the decision the brief asks for). It is the tree the store snapshotted under the state lock when that builder round closed (`reconcile.go:631`) — the exact state the chain's own reviewer judged and the one a resume reproduces. Worktree HEAD can have moved or gone dirty since (a crashed process, a manual edit), is not recorded anywhere, and reaching it would add a `SnapshotTree`/`HeadCommit` call inside the state lock, which `capture.PlanDiff` is deliberately built not to need ("it takes the trees it is given"). It is also the end `chainPlanDiff` already uses, so the plan diff and the chain diff share one meaning of "the builder's state". When `RoundClosedTree` is empty the seed falls back to the base commit, which is the designed no-path case.
3. **Never fails the close.** No base, `builderRound < 1`, no builder record, empty closed tree, empty diff, no git, a git failure, or a truncated patch (`git.DefaultMaxPatchBytes`, 4 MiB, which `capture.PlanDiff` reports as `Truncated` with no path) all mean: no key is written, the seed names no path, and the template says `No branch diff was captured; diff the branch yourself from <c.Base>.`. With no base either it says `No branch diff was captured.` The security or fixes round still opens; the close never returns an error from this input.
4. **Plan copies.** Both seeds list every chain plan copy — `chainPlanPaths(c)` passed through `chainSeedInput` — one path-final line each: `Plan copy: <path>.` under a `Plan copies:` line.
5. **Dropped inputs.** Neither seed names `ReportPath`/`DiffPath` at `closedRound` any more (the security seed's own `OutputPath` assignment, which named the reviewer's round, goes too; it was never read by the security template). The fixes seed keeps `Security output: <path>.` — the security member's real artifact path from `memberReportPath`.
6. **Paths only.** No seed inlines content; the existing `TestSeedsNamePathsOnly` keeps pinning that.
7. **Row-only key.** `chain-diff.patch` joins `reservedRoundFileRe`, so a plant at `NNN-chain-diff.patch` is never the record and `chainSeedInput` copies the row's bytes under `.chains/<chain>/inputs/<binding>-<NNN>/` (the path a runner can open).

## Seams

| File | What changes |
|---|---|
| `internal/store/paths.go` (after `PlanDiffPath`, ~:213-218) | `func (s *Store) ChainDiffPath(name string, round int) string` — `roundFile(name, round, "chain-diff", ".patch")`; comment: a row-only key like the round and plan diffs, keyed to the builder's newest closed round. |
| `internal/store/reserved.go:12` | `chain-diff\.patch` joins the alternation; the comment names it. |
| `internal/store/reserved_test.go:56-67` | `reservedKeyCases` gains `001-chain-diff.patch`; "six" → "seven". This alone extends the ReadFile/StatFile/Seal/DiskRegularFile walks. |
| `internal/chain/chain.go:185-201` | `SeedView.PlanPaths []string` — every plan copy the chain holds, in plan order, for the seeds that judge the branch as a whole. |
| `internal/chain/seeds/security.md` | first line unchanged; whole-diff line or base fallback; branch/base sentence only with a captured diff; `Plan copies:` list; the rest unchanged. |
| `internal/chain/seeds/fixes.md` | first line unchanged; `Security output:` kept; same whole-diff/fallback block; `Plan copies:` list; the read sentence says the whole diff. |
| `internal/chain/seeds_test.go` | `sampleSeedView` gains `PlanPaths`; four new template tests (below). |
| `internal/relevo/chain_seed.go` (beside `chainPlanDiff`, :20-43) | `chainBranchDiff(rt, tx, c, builder, builderRound) string` — `chainPlanDiff`'s twin: guards `c.Base == "" || builderRound < 1`, loads the builder, `"" `when `RoundClosedTree == ""`, calls `capture.PlanDiff` with `From: c.Base`, `End: b.RoundClosedTree`, `Path: rt.Store.ChainDiffPath(builder, builderRound)`, returns the key only when `res.Path != ""`. Never fails. Plus `chainSeedPlanView(rt, c, plan, *chain.SeedView)` — decodes `chainPlanPaths`, fills `PlanPaths` through `chainSeedInput`, and sets `PlanPath` for the current plan; a decode error is silently empty (as today). |
| `internal/relevo/chain.go:349-415` | `chainSeedView`: plan block → `chainSeedPlanView(rt, c, s.Plan, &v)`; the `SeedSecurity, SeedFixes` arm sets `v.OutputPath` only for fixes and `v.BranchDiffPath = chainSeedInput(rt, c, chainBranchDiff(rt, tx, c, c.Builder, memberNewestClosedRound(tx, c.Builder)))`; the `ReportPath`/`DiffPath`/`Base`-diff assignments are deleted. Signature unchanged, so `chainApply` and `chainResumeLocked` are untouched. |
| `internal/relevo/fake_test.go:107-296` | `fakeGit.diffFunc func(ctx, dir, from, to) (git.Diff, error)`, honoured by `DiffTrees` when set. |
| `internal/relevo/chain_secdiff_test.go` (new) | the five relevo tests below. |
| `internal/e2e/headless_test.go:503` | the fake builder appends `one round of fake work <NNN>` to its worktree file instead of overwriting a constant. |
| `internal/e2e/chain_test.go` (~:275-282) | `TestChainE2E` gains the whole-diff assertions. |
| `docs/plans/2026-10-01-chain-security-diff.md` (new) | this plan, committed with the code. |

Untouched, deliberately: `internal/capture` (PlanDiff already does everything needed), `internal/git`, `internal/db`, migrations, `internal/relevo/chain_start.go`, `chainResumeLocked`, `cmd/relevo`, `internal/serve`, `internal/remote`, the seed first lines, and the spec.

## Ordered steps

1. **Store key + reservation.** `ChainDiffPath` in `internal/store/paths.go`; `chain-diff\.patch` in `internal/store/reserved.go:12`; the new case + comment in `internal/store/reserved_test.go`. Check: `go test ./internal/store -run 'TestReadFileRefusesAReservedRoundFileOnDisk|TestStatFileRefusesAReservedRoundFileOnDisk|TestSealRoundLeavesAReservedRoundFileAlone|TestDiskRegularFileRefusesAPlantAtARowOnlyKey' -count=1` — the four walks must now cover the new name.
2. **View field.** `SeedView.PlanPaths` in `internal/chain/chain.go`; `sampleSeedView` gains two paths. Check: `go test ./internal/chain -count=1` — green with the field unused.
3. **Templates + their pins.** Rewrite `security.md` and `fixes.md` per the behaviour above (first lines byte-identical); add the four template tests. Check: `go test ./internal/chain -run Seed -count=1`.
4. **Capture + plumbing + relevance tests.** `chainBranchDiff`, `chainSeedPlanView`, the `chainSeedView` arm, `fakeGit.diffFunc`, and the five tests in `internal/relevo/chain_secdiff_test.go`. Check: `go test ./internal/relevo -run 'TestChainSecuritySeed|TestChainFixesSeed' -count=1`.
5. **E2E.** The fake builder's appended round line; the `TestChainE2E` assertions. Check: `make e2e`.
6. **Full check + mutations.** `make check` (fix every reported error before re-running), then run every mutation in the table below and confirm the named test fails, restoring each mutation; then `make e2e` once more at the tip.
7. **Document and commit.** Write `docs/plans/2026-10-01-chain-security-diff.md` with this plan's text; commit implementation + plan as new commits. Check: `git status` clean, `git log --oneline` shows only new commits, `make check` green at the tip.

## New tests, by name — what each pins

`internal/store` (no new function; the table does the work): `reservedKeyCases` gains `001-chain-diff.patch`, so `TestReadFileRefusesAReservedRoundFileOnDisk`, `TestStatFileRefusesAReservedRoundFileOnDisk`, `TestSealRoundLeavesAReservedRoundFileAlone` and `TestDiskRegularFileRefusesAPlantAtARowOnlyKey` each pin the new key once.

`internal/chain`:
- `TestSecuritySeedNamesTheWholeBranchDiffAndThePlanCopies` — with `BranchDiffPath` and two `PlanPaths`, the security seed names the whole-diff path, each `Plan copy: <path>.`, and no fallback sentence.
- `TestSecuritySeedSaysToDiffFromTheBaseWhenNoBranchDiffWasCaptured` — `BranchDiffPath == ""`, `Base` set: the seed carries `No branch diff was captured; diff the branch yourself from <base>.`, names no path, and (with `PlanPaths` nil) renders no `Plan copies:` line; with `Base == ""` it says `No branch diff was captured.` with no dangling branch sentence.
- `TestFixesSeedNamesTheWholeBranchDiffAndThePlanCopies` — the fixes seed names `Security output: <path>.`, the whole-diff path and every plan copy.
- `TestFixesSeedSaysToDiffFromTheBaseWhenNoBranchDiffWasCaptured` — its base fallback, no path.

`internal/relevo` (new `chain_secdiff_test.go`):
- `TestChainSecuritySeedNamesTheWholeChainDiff` — a 2-plan chain: builder rounds 1 (plan 1, one correction) and 2 (correction), then plan 2's round 3; `c.PlanStartCommit` is moved to `head-plan2` before plan 2 starts so the chain base is the only correct start. `fakeGit.diffFunc` answers a three-file patch for `(commit-head-123, tree-round-3)` and a third-file-only patch for any other `from` to `tree-round-3`. The security seed names `ChainInputPath(ChainDiffPath("shop", 3))`, not the key; the copy's bytes carry all three rounds' changes; the seed does not name `ReportPath`/`DiffPath` at the closing round; `fg.lastDiffFrom == row.Base` and `fg.lastDiffTo == "tree-round-3"`; the close did not fail (step scanning, awaiting the security round). This is the test the MasterMind's mutation must break.
- `TestChainSecuritySeedListsEveryPlanCopy` — a 2-plan chain: the security seed names `ChainPlanPath("shop", 1)` and `(2)` as `Plan copy:` lines.
- `TestChainSecuritySeedNamesTheBaseWhenTheBranchDiffIsTruncated` — `fg.diffResult` truncated (`Truncated: true`): no key, no path, the seed names `row.Base`, and the security round still opens.
- `TestChainSecuritySeedNamesTheBaseWhenTheBuilderHasNoClosedTree` — the builder's `RoundClosedTree` is cleared before the reviewer passes: same fallback (git stays, so the reader send can still build its scratch worktree).
- `TestChainFixesSeedNamesTheSameWholeChainDiff` — a chain whose builder newest closed round is 2 while the security round is 1: the fixes seed names the same `ChainDiffPath("shop", 2)` copy the security seed named, the copy holds the whole patch, the seed names the security output, and it does not name `ChainDiffPath("shop", 1)` (so keying the diff on the closing reader's round fails here).

`internal/e2e`: `TestChainE2E` reads the security member's staged seed and asserts it names the copy of `ChainDiffPath(builder, 3)` (the builder's newest closed round at the scan), that the copy is a regular file whose bytes equal the key's and contain `one round of fake work 001`, `002` and `003` (the whole span — a one-round diff carries only the last), that it does not name the old `DiffPath(builder, 3)` copy, and that the fix planner's seed names the same copy.

No goldens exist, so none are updated.

## Existing tests that change, and why

1. `internal/store/reserved_test.go` — the table plus the "six" count; the new key is pinned by four existing walks.
2. `internal/chain/seeds_test.go` — `sampleSeedView` gains `PlanPaths`; the four new tests sit beside the existing security/fixes ones.
3. `internal/relevo/fake_test.go` — `diffFunc`; no existing assertion changes.
4. `internal/e2e/headless_test.go` — the fake builder appends its round line; no Go assertion changes. Note the knock-on in `chain_test.go`: the existing reviewer-round-2 block (`:204-224`) flips from its `else if "not available"` arm to its `if` arm, because the correction round now changes `fake-round.txt`; both arms are already written and both must pass, and the comment there ("changes no tracked content") must be reworded.
5. `internal/e2e/chain_test.go` — the new assertions; no existing one moves.
6. No test is deleted. No `cmd/relevo` test is added (and none may spawn a harness or reach the network).

## Mutation checks (run every one; the MasterMind will run #1)

| # | behaviour | mutation | test that must fail |
|---|---|---|---|
| 1 | the diff starts at the chain base | diff from the newest round's baseline/start instead of `c.Base` (it is cleared at close, so the capture finds no baseline) | `TestChainSecuritySeedNamesTheWholeChainDiff` |
| 2 | the whole diff is the chain base → newest closed tree | end at `PlanStartCommit` or the closing reader's tree | `TestChainSecuritySeedNamesTheWholeChainDiff` (partial patch lacks round one) |
| 3 | a seed names a copy, never a key | return the key from `chainSeedView` without `chainSeedInput` | `TestChainSecuritySeedNamesTheWholeChainDiff` |
| 4 | the key is the builder's newest closed round | key the diff on `closedRound` | `TestChainFixesSeedNamesTheSameWholeChainDiff` |
| 5 | truncated means no path | return a path when `Truncated` | `TestChainSecuritySeedNamesTheBaseWhenTheBranchDiffIsTruncated` |
| 6 | an empty diff value renders the fallback | render the path branch when `BranchDiffPath == ""` | `TestSecuritySeedSaysToDiffFromTheBaseWhenNoBranchDiffWasCaptured` + the truncated relevo test |
| 7 | the seeds list every plan copy | drop `PlanPaths` | `TestChainSecuritySeedListsEveryPlanCopy`, `TestSecuritySeedNamesTheWholeBranchDiffAndThePlanCopies` |
| 8 | the new key is row-only | drop `chain-diff\.patch` from the alternation | `TestDiskRegularFileRefusesAPlantAtARowOnlyKey` + the other store walks |
| 9 | the old round-diff input is gone | restore `v.DiffPath = DiffPath(builder, closedRound)` | `TestChainSecuritySeedNamesTheWholeChainDiff` |

## Deletions (closed list)

1. `chainSeedView`'s `SeedSecurity`/`SeedFixes` `ReportPath` and `DiffPath` assignments at `closedRound`, and `v.BranchDiffPath = v.DiffPath` (`internal/relevo/chain.go:410-412`).
2. The security seed's `OutputPath = ReportPath(c.Security, closedRound)` assignment (`internal/relevo/chain.go:406`) — unused by its template and keyed to the reviewer's round; only the fixes seed keeps an output path.
3. `security.md`'s unconditional `Branch diff: {{.BranchDiffPath}}.` and unconditional `{{.Branch}} against {{.Base}}.` — the path line becomes conditional, the branch/base sentence renders only beside a captured diff.
4. `fixes.md`'s unconditional `Branch diff: {{.BranchDiffPath}}.`.
5. Nothing else: no migration, no schema/golden change, no reserved key removed, no test deleted, and no seed first line touched.

## Halt conditions (unverified premises)

1. If `git diff c.Base <RoundClosedTree>` cannot produce a patch in the e2e's real repository (base unreachable, tree not a valid revision), halt and report — do not fall back to HEAD.
2. If `tx.PutRoundFile` refuses `NNN-chain-diff.patch`, halt; never write a plain file into the state directory.
3. If a chain row without `Base` makes the seed fail instead of wording the miss, halt.
4. If the fake builder's round-content change breaks any e2e assertion this plan does not list, halt; do not soften the new security assertions or the fake's path check.
5. If a `make check` coverage gate drops and tests cannot restore it, halt; the baseline in `testdata/coverage-baseline.txt` is never lowered.
6. If the first lines of `security.md`/`fixes.md` have to move to make the change work, halt — the fake harness identifies the seed by them.

## The report must include

- The `relevo` tail (`status`, `changed_paths`, `commands_run`, `not_done`) and the new commits, never amended.
- The mutation table above, filled with what actually failed for each mutation (especially #1, run at the MasterMind's request), and the focused command run per step.
- `make check` and `make e2e` results.
- The seed-vs-code findings: the exact old value the two seeds named (`DiffPath(builder, closedRound)` at `chain.go:405-413`), the spec lines (`docs/specs/2026-09-30-chains-design.md:171-172`) the fix brings the code back to, the proof that no migration is needed, the absence of template goldens, and the e2e answer: extended, because the span is assertable cheaply (one line of fake content plus post-run byte assertions; the fake's existing per-path readability check already covers the new lines).
- Which existing tests changed and why — including the reviewer-round-2 block's arm flip in `internal/e2e/chain_test.go` — and that no test moved otherwise.
- That the coverage baseline, the schema, and the spec are untouched.
