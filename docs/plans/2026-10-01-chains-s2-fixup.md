# Chains slice 2 · merge fix-up: slice 2's tests onto #781's seed inputs

One-shot builder round on branch `chains-s2-final` (763e2abd): slice 2 rounds 1-5
plus main merged in, including PR #781 (55bf3655, plan
`docs/plans/2026-10-01-chain-seed-files.md`). Commit, never amend. Ends with
`make check` AND `make e2e` green. This is the last round of slice 2; the slice
lands as one PR.

## What broke, and why

#781 made every seed input a path that exists. An input that is not a regular file
on disk as the store reads it (a sealed round file: a round diff, a plan diff, an
older round's report, gate log or prompt) is copied to
`<state>/.chains/<chain>/inputs/<binding>-<NNN>/<name>` (`chainSeedInput`,
`Store.ChainInputPath`, `internal/relevo/chain_seed.go`), and the seed names the
copy. An unreadable input reads `not available: <why>`. Slice 2's tests were written
before #781 and still assert the raw paths:

- `internal/relevo/chain_test.go`, `TestChainRemoteRedGateAfterTheBudgetReachesTheReviewer`
  (line ~1643): expects `Check result: red; its output is at <state>/shop/001-gate.log.`
- `TestRemoteBuilderCloseSeedsTheReviewerWithThePulledRound` (line ~1689): expects
  `<state>/shop/001-diff.patch`.
- `internal/e2e/chain_remote_test.go`, `TestChainRemoteBuilderE2E` (line ~262): expects
  `<state>/s2r5shop/002-plan-diff.patch`; the seed names
  `.chains/s2r5shop/inputs/s2r5shop-002/002-plan-diff.patch`.

## The fix

Change the three tests' expectations to #781's contract, the way #781's own tests
assert it (`TestChainReviewerSeedNamesACopyOfTheSealedDiff`,
`TestChainReviewerSeedNamesACopyOfThePlanDiff` in `internal/relevo/chain_test.go`;
`internal/e2e/chain_test.go`'s round-2 seed assertions): the seed names a path that
exists on disk, and that file's bytes equal what the store holds for the original
key (`rt.Store.ReadFile(<original path>)`). For the gate line, assert the red result
and that the named log path exists with the gate log's bytes (whether it is the
original path or a copy depends on whether the log was sealed: accept either, by
checking the file the seed names, not a fixed path). Do not change production code:
if a test cannot be satisfied without a production change, halt and report what the
seed actually names and why it is wrong.

Also: `internal/e2e/chain_remote_test.go:256` has a comment that
`scripts/check-comments.sh` refuses (it cites history: `#NNN` or `§`). Rewrite that
comment without the citation.

## Checks

Run `sh scripts/check-comments.sh` yourself (a builder's worktree `make check` misses
it), then `make check` and `make e2e`. Report each test's old and new assertion. Save
this text as `docs/plans/2026-10-01-chains-s2-fixup.md` in the same commit.
