# Cockpit D2 stats, candidates tab round 3: MEASURED moves from the table into the detail block

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `69a1087`, rebased onto origin/main 753aa4e. Build on it,
and amend it at the end (`git commit --amend --no-edit`). Do not rebase or push. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- the stats goldens
- `docs/plans/`

## 1. The user's review

The candidates tab's `MEASURED` column (`215/218`, `45/47`, `3/3`) is unclear. It counts the candidate's rounds that
reported token usage, out of all its rounds. The user approved these changes:
- drop the column;
- say the same thing in plain words in the detail block.

## 2. Changes (in `internal/ui/view_stats.go`)

### 2.1 The column set (the `statsCandCol` table and its drop logic)

- **Remove** the `MEASURED` column entry.
- **The drop order** becomes TTFT first, then MED. Renumber the ranks: TTFT 1, MED 2.
- **Update the comments** that list the drop order.

### 2.2 The detail block (`statsCandDetail`)

For a non-idle row, add a **fourth line**: `"   "` plus a muted sentence.
- `"<Measured> of <Rounds> rounds reported token usage"` when `0 < Measured < Rounds`;
- `"every round reported token usage"` when `Measured == Rounds`;
- `"no round reported token usage"` when `Measured == 0`.

Use `round` when `Rounds == 1`, e.g. `1 of 1 round…`. The `every round` case is fine as it stands. An idle row is unchanged.

## 3. Tests (`internal/ui/view_stats_test.go`)

1. **Port** `TestStatsCandidatesTabRows`: the header no longer contains `MEASURED`. Keep every other assertion.
2. **Port** `TestStatsCandidatesTabDropsColumns`:
   - at 100 columns, every remaining column shows, including `TTFT` and `MED`;
   - at 80, `TTFT` and `MED` are gone;
   - at both, the name column is at least 16 cells and no line passes `w-3`.

   If the arithmetic gives a different set at either width, assert what it gives, and report the numbers.
3. **Extend** `TestStatsCandidatesTabDetail` with three rows:
   - `Measured 4`, `Rounds 5` → `4 of 5 rounds reported token usage`;
   - `Measured 5`, `Rounds 5` → `every round reported token usage`;
   - `Measured 0` → `no round reported token usage`.
4. **Regenerate the goldens once.** Only `stats-candidates-132` may change. If another one changes, halt.

## 4. Steps

1. §2 and §3.
2. The full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
   - Everything green, `gofmt -l` empty.
3. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-candidates-r3.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 5. Report

Include:
- the tests changed;
- the stripped `stats-candidates-132.golden`;
- `git diff --stat 69a1087 HEAD`.
