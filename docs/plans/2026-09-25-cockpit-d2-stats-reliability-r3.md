# Cockpit D2 stats, reliability round 3: the gates and hours tables fill the width

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `56c7753`, on origin/main 8e26a1f, and it is pushed as
PR #500. Build on it, and amend it at the end (`git commit --amend --no-edit`, adding only the named files). Do **not** push:
the planner pushes. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- `internal/ui/testdata/stats-reliability-132.golden`
- `docs/plans/`

## 1. The user's review

On a wide screen, the reliability tab's two tables use the left half and leave a big blank space on the right. The tiles
already fill the width.
- **The gates table** has fixed columns: `CANDIDATE` 24, `PROVIDER` 13, `UNTIL` 16, `LEFT` 5, `REASON`.
- **The hours table** has 3-cell hour columns: `statsHourLines`, `view_stats.go:2333-2355`.

**The user's rule:** the tables are responsive and fill `[3, width - 3)`, as every other stats table does.

## 2. Changes (in `internal/ui/view_stats.go`)

### 2.1 The gates table (`statsGatesLines`, lines 2287-2331)

The columns, left to right, span exactly `width - 6` cells from column 3:

| column | width | alignment |
|---|---|---|
| `CANDIDATE` | the rest: `nameW = width - 6 - 14 - 16 - 6 - 3 - reasonW`, at least 16 | left |
| `PROVIDER` | 14 | left |
| `UNTIL` | 16 | left |
| `LEFT` | 6 | right |
| (gap) | 3 | |
| `REASON` | `reasonW = 30` | left, cut with `statsCut`; its cell is padded so the row ends at exactly `width - 3` |

**When `nameW` would be under 16:** shrink `reasonW` down to a minimum of 12 first, and only then let `nameW` fall to its
minimum of 16.

The header uses the same widths. `FitKey` clips the names. This replaces the fixed `24/13/16/5` and `noteW` arithmetic at
lines 2296-2311. Everything else in the function stays, including the empty-state line and `statsGateReason`.

### 2.2 The hours table (`statsHourLines`, lines 2333-2355)

- **The new signature:** `statsHourLines(width int)`. `reliabilityTabLines` passes `width`.
- **The cell width:** `labelW = 16` and `cellW = max(3, (width - 6 - labelW) / 24)`. At 132 columns that is 4, and at 200 it
  is 7.
- **The header:** `LIMITS BY HOUR` padded to `labelW`, then each hour right-aligned in `cellW`.
- **The rows:** the provider padded to `labelW`, then each cell. `statsHourCell` takes the width: its `·` or count is
  right-aligned in `cellW`, with the same styles as today (grid dot, text, red bold at 3+).
- **The right margin:** at most `(width - 6 - labelW) % 24` spare cells remain at the right of the table.

## 3. Tests

1. **New:** `TestStatsReliabilityFillsWidth`. At 132 and 200 columns, with the fixture from `TestStatsReliabilityTab`:
   - a gate row's trimmed length is exactly `w - 3`, because its reason cell is padded to the edge;
   - the hours header's last hour label `23` ends at `3 + 16 + 24*cellW - 1` (0-based), with `cellW` 4 at 132 and 7 at 200;
   - a count sits under its own hour: the hour-9 label and the hour-9 count end in the same column.
2. **Port** `TestStatsReliabilityTab` and `TestStatsReliabilityEmpty` if their column arithmetic moves. Keep what they
   assert.
3. **Regenerate the goldens once.** Only `stats-reliability-132` may change. If another one changes, halt.

## 4. Steps

1. §2 and §3.
2. The full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
   - Everything green, `gofmt -l` empty.
3. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-reliability-r3.md` into `docs/plans/`. Then
   `git add` the named files and `git commit --amend --no-edit`. Do not push.

## 5. Report

Include:
- the tests;
- the stripped `stats-reliability-132.golden`;
- `git diff --stat 56c7753 HEAD`.
