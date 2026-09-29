# Cockpit D2 stats, tab 1 round 2: totals into the overview, tabs on the context row, a full-width grid

Date: 2026-09-25. Worktree: ck-d2-stats, whose branch is round 1's commit 9cc38322 rebased onto origin/main e2218b3f. Build
on it. You may commit at the end, **amending** 9cc38322 (`git commit --amend --no-edit`) so the branch stays one commit. Do not
rebase or push. Line numbers are exact in the worktree now. One round.

**Stop rather than improvise.** If the code differs from what is quoted, halt and report. Change only `internal/ui` (and
`internal/stats` only if a helper there needs a new exported function).

## 1. The user's review of the overview tab

1. "We don't need those over the tabs; include them in the overview page." That is the totals line (`472 rounds  1.7B tokens
   96% cached  14M out  6m median  23 halted`), which `Context` draws above the tabs row. It repeats the tiles. Remove it;
   the median, the one fact the tiles lack, joins the ROUNDS tile.
2. "The grid should fill the width; the columns need to be correctly aligned."
   - The tiles are fixed 30-cell columns and leave the right half empty.
   - The CANDIDATES section has a one-string caption (`done · in/rnd · out/rnd · cache`) instead of a header row aligned over the
     numbers.
   - The two tables do not use the width either.

The planner's own list, from round 1's notes, rides along:
- the chart title says `30d` instead of `30 days`;
- `esc back` appears twice in the footer;
- dead code: `statsWide` (view_stats.go:36-38), `statsTextWidth` (line 713), and `rowTokens` in internal/stats if unused.

## 2. Changes

### 2.1 The context row is the tabs row

- `Context` (view_stats.go:149) returns:
  - **left:** the tabs row (the current `statsTabsRow` content, starting with three spaces instead of its current indent);
  - **right:** faint `window  `, then the window chips as today, plus ` · refreshing` or the error, and two spaces.
- **No totals.**
- `Body` (line 375) no longer draws the tabs row itself. `head` becomes one blank line, so the body opens with a blank line under the context row.

### 2.2 Tiles fill the width

- The tile width is `tileW = (width - 3) / 4`. Each tile starts at `3 + i*tileW`.
- Below 96 columns, lay them out 2×2 with `tileW = (width - 3) / 2`.
- The ROUNDS note becomes `<halted> halted · <open> open · <median> median`, where median is `stats.Duration(MedianMS)`.
- Each line of a tile is cut to `tileW - 2` with an ellipsis if it is longer.

### 2.3 Chart heading

`   TOKENS` (faint bold), then faint `   per day, <N> days` for 7d, 30d and 90d, or `   per day, all time` for `all`.

### 2.4 Two tables side by side, each filling half the width, with real header rows

- `half = (width - 6) / 2`. The left table starts at column 3, the right at `3 + half + 3`.
- Below 96 columns, stack them, each `width - 6` wide.

**CANDIDATES** (left):
- The section line: `CANDIDATES` faint bold + faint `   top by rounds, 5+ rounds`.
- Then a header row in faint bold, with columns right-aligned over their numbers: `CANDIDATE | RNDS | DONE | IN/RND | OUT/RND | CACHE`.
- Fixed widths from the right: RNDS 6, DONE 6, IN/RND 9, OUT/RND 9, CACHE 7. CANDIDATE takes the rest of `half`
  (left-aligned, clipped with `…`).
- Up to 5 rows.
- IN/RND is `(In+Cache)/Measured` via `ShortTokens`. OUT/RND is `Out/Measured`. `·` when nothing is measured.

**BUSIEST REPOS** (right):
- The section line: `BUSIEST REPOS` faint bold + faint `   by tokens`.
- A header row: `REPO | RNDS | TOKENS | SHARE`.
- Fixed from the right: RNDS 6, TOKENS 9, SHARE 12. SHARE is a bar in accent: `round(tokens / maxTokens * 10)` `▇` cells, plus a
  space and `<pct>%` of the window's total, right-aligned within 12. REPO takes the rest (`shortRepo`, clipped with `…`).
- Up to 5 rows, by Tokens desc.

The two tables' header rows and body rows sit on the same lines.

### 2.5 Footer

Overview `Keys()`: `tab next tab`, `w window`. No `esc back`, which the shell's tail already adds.

### 2.6 Dead code

Delete:
- `statsWide` and its comment;
- `statsTextWidth`, if it has no caller;
- `rowTokens` in internal/stats/stats.go, if it has no caller.

Check with grep before each deletion.

## 3. Tests

- `TestStatsContextIsTabs`: `Context` left contains `overview` and `candidates`, and no `rounds` or `tokens`. Right contains `30d`.
- `TestStatsOverviewFillsWidth`: at 132 and 180 columns:
  - the ROUNDS tile's value starts at column 3 and the OUTPUT tile's at `3 + 3*((w-3)/4)`;
  - the last cell of the right table's header row (SHARE) ends within 3 cells of `w`.
- `TestStatsOverviewColumnsAligned`: the column index where the header word `DONE` ends equals the index where each candidate row's done %
  ends. The same holds for `TOKENS` in the repos table.
- `TestStatsOverviewNarrowStacks`: at 90 columns the tiles are 2×2, and the tables stack.
- Ports: round 1's tests that asserted the totals in the context row or the tabs row in the body (cite §2.1).
- **Goldens:** regenerate `stats-overview-132`, `stats-wide`, `stats-narrow` and `stats-empty`. No other golden may change.

## 4. Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/stats/ ./internal/ui/ -count=1`, plus `-run Golden -update`.
- Final: `gofmt -l $(git ls-files '*.go')`, `go vet ./internal/stats/ ./internal/ui/`, and the same test command plus
  `./cmd/relevo/`. Not `make check`.
- Then amend the commit (see the top of this plan).

## 5. Report

List:
- the files changed;
- the ports;
- the golden diffs;
- `stats-overview-132.golden` verbatim;
- the check results.
