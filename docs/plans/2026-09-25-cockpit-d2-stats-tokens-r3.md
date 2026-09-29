# Cockpit D2 stats, tokens tab round 3: arrow keys scroll a tab that has no table

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `86ad5eb`. Build on it, and amend it at the end
(`git commit --amend --no-edit`). Do not rebase or push. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- the stats goldens, only if the footer shows in one
- `docs/plans/`

## 1. The bug

On the tokens tab, the stacked charts are taller than the screen, and the hint reads `more below · pgdn`. The user tried to
scroll and could not.
- `pgdn`/`space` do scroll: the planner confirmed it in tmux.
- `↑`/`↓` and `j`/`k` do nothing. In `updateKey` (`view_stats.go:336-343`) they only move a table cursor
  (`moveStatsCursor`, then `follow`), and the tokens tab has no table, so nothing moves.

## 2. The fix (in `internal/ui/view_stats.go`)

**Add** `func (v statsView) tabHasCursor() bool`: true on `overview`, `candidates` and `repos`, false on `tokens` and
`reliability`.

**`up`/`k` and `down`/`j` in `updateKey`:**
- **When `tabHasCursor()`:** unchanged.
- **Otherwise:** scroll the page one line, `v.top = clamp(v.top ± 1, 0, statsMaxTop(len(lines), avail))`, using
  `v.page(env)` for `lines` and `avail`.

**`home`/`end`** already work on every tab. Leave them.

**`Keys()`:** the tokens case (and reliability, if it has its own case) gets `{"↑↓","scroll"}` first. Today the tokens case is
`{"s","split"}, {"tab","next tab"}, {"w","window"}`; it becomes `{"↑↓","scroll"}, {"s","split"}, {"tab","next tab"}, {"w","window"}`.

## 3. Tests

1. `TestStatsArrowsScrollWithoutCursor`:
   - **Setup:** the tokens tab at 132×20 on a report whose kind split is taller than the screen. Reuse the setup of
     `TestStatsTokensKindOwnScale`, with `split = 3`.
   - **Assert:**
     - `j` raises `top` by 1, and `k` lowers it by 1;
     - `k` at `top 0` stays 0;
     - 200 `j` presses stop at `statsMaxTop`.
2. `TestStatsArrowsMoveCursorWithTable`: on the overview, `j` still moves `ovCursor[0]` and does not change `top` beyond
   what `follow` does. Assert `ovCursor[0] == 1`.
3. `Keys()` on the tokens tab contains `↑↓`.
4. **Mutation check (report the result):** make `tabHasCursor` return true for tokens, and confirm test 1 fails. Then
   restore it.

## 4. Steps

1. §2 and §3.
2. The full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
   - Only a golden that renders the tokens or reliability footer may change.
3. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-tokens-r3.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 5. Report

Include:
- the tests;
- the mutation check's result;
- `git diff --stat 86ad5eb HEAD`.
