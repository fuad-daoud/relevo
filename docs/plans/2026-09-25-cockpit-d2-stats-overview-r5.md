# Cockpit D2 stats, tab 1 round 5: finish round 4 (accept the grown goldens, fix test 11)

Date: 2026-09-25. Worktree: ck-d2-stats. HEAD is `74c23893`, and round 4's changes are **uncommitted** in the worktree. Keep
them all. This round only finishes round 4. At the end, commit by **amending** `74c23893`. Do not rebase or push.

**Stop rather than improvise.** If anything below contradicts the code, halt and report.

## 1. What happened

Round 4 halted correctly, on two false premises in its plan.

1. **The golden gate.** Round 4's plan said the 132×34 golden's chart "should stay 8 plot rows". That was wrong. The golden
   has 5 candidate rows and 3 repo rows, so its budget is 12, and a 12-row chart (`per` 3) is exactly what §4.6 intends:
   the chart takes rows the tables do not need. **Accept the grown goldens:** `stats-overview-132`, `stats-wide` and
   `stats-narrow`.
2. **Test 11.** `TestStatsOverviewChartUsesSpareRows` compares 132×34 with 132×60 on `statsFixture`. Both are already at the
   `per = 4` cap, which gives 12 rows each. **Change only its short height** from 34 to **22**. At 22, `Body` gets
   `avail = 21`, so `budget = 21 - 9 - 4 = 8`, `per = 2`, and there are 6 plot rows, fewer than 60's 12.
   - Keep the assertion "more plot rows at 60 than at the short height".
   - Also assert the exact counts: **6** and **12**.
   - If `Body`'s height arithmetic differs from the round 4 report's (`avail = height - 1` when `Body` is called directly),
     recompute from the code and use the counts it gives, as long as short < tall. Report the numbers.

## 2. Steps

1. Edit test 11 as §1.2 says.
   - **Done when:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Stats' -count=1` passes.
2. Regenerate the goldens once:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`
   - Read the stripped `stats-overview-132.golden` and check it:
     - the chart has 12 plot rows, with `┤` every third row;
     - no line is longer than 129 trimmed cells;
     - neither table has a caption;
     - the ROUNDS note is whole.
3. Run the full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
   - Everything must be green, and `gofmt -l` must print nothing.
4. Copy these into the worktree's `docs/plans/`:
   - `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview-r4.md`
   - this file, `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview-r5.md`

   Then `git add -A && git commit --amend --no-edit`.
   - **Done when:** `git log --oneline origin/main..HEAD` shows one commit and `git status` is clean.

## 3. Report

Include:
- the test 11 counts;
- the stripped `stats-overview-132.golden` in full;
- `git diff --stat 74c23893 HEAD`.
