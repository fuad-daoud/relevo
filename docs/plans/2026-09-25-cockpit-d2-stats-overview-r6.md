# Cockpit D2 stats round 6: the page scrolls when the pane is short

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `815bae7`, on origin/main 7db0ec7d. Build on it. At the
end, commit by **amending** that commit. Do not rebase or push. Line numbers below are exact in the worktree now. This is one
round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/view_stats.go`
- `internal/ui/view_stats_test.go`
- the stats goldens (only if they change, and they should not)
- `docs/plans/` (step 5)

## 1. System overview

The user ran `:stats` in a pane about 12 rows tall. The body shows the tiles and the top of the chart, and the tables cannot
be reached. `Body` (`view_stats.go:469-498`) already has a page offset, `v.top`, but three things are wrong with it:

1. **`pgup`/`pgdn` move one line** (lines 266-273), and nothing tells the user they exist.
2. **`pgdn` has no upper bound.** `v.top` grows past the end, and `Body` only clamps it at render time. After pressing
   `pgdn` 20 times, it takes more than 20 `pgup` presses before anything moves.
3. **The overview's `↑↓` move a table cursor that may be off screen.** The page does not follow it.

**The fix:** the page scrolls properly on every stats tab. `Body` is shared, so all tabs get it, and on the overview the page
follows the table cursor. The mouse is out of scope: the cockpit has no mouse support.

## 2. Working efficiently

- Read `internal/ui/view_stats.go` in full, `internal/ui/view_stats_test.go` in full, and `internal/ui/frame.go:118-127`
  (`bodyHeight`), all in one batched step.
- Make the changes to `view_stats.go` in as few edit calls as you can.
- The focused command:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Stats|Golden' -count=1`
- The full check, once at the end:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
- These are pure view tests: none may spawn a harness or reach the network.

## 3. Contracts (all in `internal/ui/view_stats.go`)

### 3.1 `tabLines` returns the selected line

- **Signature:** `func (v statsView) tabLines(env Env, width, height int) (lines []string, sel int)` (line 516).
- **`sel`:** the index in `lines` of the overview's selected table row, that is, the `ovFocus` table's `ovCursor` row. It is
  -1 when that table is empty, and -1 on every other tab.
- **`overviewLines`** likewise returns `(lines []string, sel int)`. It knows where each table's rows land, because it
  builds them:
  - **side by side:** `sel = fixed + 2 + (cur - first)` for either table;
  - **stacked:** the candidates rows are as above. The repos rows start after the candidates block, its blank line, and
    the repos section and header lines.
  - Compute `first` with the same `statsTableWindow` call the table uses. The simplest way is to have
    `statsOverviewCandidates` and `statsOverviewRepos` also return the visible offset of the selected row, or -1.
- Every existing caller of `tabLines` and `overviewLines`, in the code or in tests, is updated to the two results.

### 3.2 `func (v statsView) page(env Env) (lines []string, sel, avail int)`

- **Responsibility:** the body lines as `Body` would lay them out for the shell's current size.
- `avail = bodyHeight(env) - 1` (the one head line). `lines` and `sel` come from `v.tabLines(env, env.Width, avail)`.
- It returns before the `loaded`/error/empty checks matter. When `!v.loaded` or `Totals.Rounds == 0`, it returns
  `nil, -1, avail`.

### 3.3 `func statsMaxTop(n, avail int) int`

This is `max(0, n - avail)`.

### 3.4 Keys (`updateKey`, lines 221-282)

- **`pgdn`** and **`space`:**
  - `top = min(top + max(1, avail-1), statsMaxTop(len(lines), avail))`, using `page(env)`.
- **`pgup`:** `top = max(0, top - max(1, avail-1))`.
- **`home`:** `top = 0`. **`end`:** `top = maxTop`.
  - Do not bind `g`/`G`: `g` is the cockpit's gate key elsewhere.
- **`up`/`k`, `down`/`j`, `left`/`h` and `right`/`l` on the overview:**
  - after the existing cursor and focus change, **follow**: compute `page(env)`;
  - if `sel >= 0 && sel < top`, then `top = sel`;
  - if `sel >= top + avail`, then `top = sel - avail + 1`;
  - then clamp `top` to `[0, maxTop]`.
- **`tab`, `shift+tab` and the digit keys** reset `top = 0`, as `w` already does.
- The candidates and repos tabs' `↑↓` keep their current behaviour: no follow, because `sel` is -1 there.

### 3.5 `Body` (lines 469-498): the scroll hint

- Keep the render-time clamp, which covers a resize.
- When `len(body) > avail`, the head line (today `""`) becomes a right-aligned faint hint, ending at `width - 3`:
  - at the top: `more below · pgdn`;
  - at the bottom (`start == maxTop`): `more above · pgup`;
  - otherwise: `pgup · pgdn`.
- When everything fits, the head line stays blank. That keeps every golden unchanged: they all fit.

### 3.6 `Keys()`

No change. The hint lives in the body only when it applies, so the footer stays calm.

## 4. Error handling

There are no new error paths.
- **`bodyHeight` of 0:** `avail` is at most 0, `maxTop` is `len(lines)`, and nothing panics. Guard every slice.
- **A resize between keys:** `Body`'s clamp covers it.

## 5. Tests (`internal/ui/view_stats_test.go`)

Build the env with `statsTestEnv(t, 132, 12)`. Assuming `ErrRows` is 0, `bodyHeight` is `12-3-2 = 7`, so `avail` is 6. If
`bodyHeight` gives something else, use its value and say so. Render with `v.Body(env, 132, bodyHeight(env))`.

1. **`TestStatsPageScrollBounds`:**
   - 50 `pgdn` presses leave `top == statsMaxTop(len(lines), avail)`, never more.
   - One `pgup` then lowers `top` by `avail-1`, or to 0.
   - `end` gives `maxTop`; `home` gives 0.
2. **`TestStatsPageFollowsCursor`:**
   - On the overview at 132×12, press `l` (the repos table).
   - The stripped `Body` contains the top-tokens repo's short name, and its line is the band. Check this by the
     ANSI-stripped text containing the name, plus `v.top > 0`.
   - Then press `h` and `k`: the candidates' first row is visible.
3. **`TestStatsPageHint`:**
   - At 132×12 with `top == 0`, the stripped first body line ends with `more below · pgdn` at column `132-3`.
   - After `end`, it reads `more above · pgup`.
   - At 132×34 (everything fits), the first body line is blank.
4. **`TestStatsTabResetsTop`:** after `pgdn`, `tab` sets `top` back to 0.

**Update every existing test that calls `tabLines`/`overviewLines`** for the new second result. Change nothing else in them.

## 6. Ordered steps

1. `tabLines`/`overviewLines` return `sel`, and the table helpers report the selected offset (§3.1).
   - **Depends on:** none.
   - **Done when:** the existing tests pass unchanged apart from the call-site updates.
2. `page`, `statsMaxTop`, and the keys: bounds, home/end, the follow and the tab reset (§3.2-3.4).
   - **Depends on:** 1.
   - **Done when:** tests 1, 2 and 4 pass.
3. The `Body` hint (§3.5).
   - **Depends on:** 2.
   - **Done when:** test 3 passes, and `TestGoldenViews` passes **without** `-update`. If a golden changes, halt and report
     which one and why.
4. The full check (§2).
   - **Depends on:** 3.
   - **Done when:** all green, and `gofmt -l` is empty.
5. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-overview-r6.md` into the worktree's
   `docs/plans/`. Then `git add -A && git commit --amend --no-edit`.
   - **Depends on:** 4.
   - **Done when:** one commit on `origin/main..HEAD`, and a clean tree.

## 7. Report

Include:
- the tests added and changed;
- the `bodyHeight` value you used;
- `git diff --stat 815bae7 HEAD`;
- anything you halted on.
