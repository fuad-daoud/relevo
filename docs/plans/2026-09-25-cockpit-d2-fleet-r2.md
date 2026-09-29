# Cockpit D2 fleet, round 2: the card goes above the list

Date: 2026-09-25. Worktree: the ck-d2-fleet worktree, which holds round 1's
**uncommitted** changes (the D2 theme and the grouped `:fleet`, per
`docs/plans/2026-09-24-cockpit-d2-fleet.md`). Build on them; do not reset or
commit anything. One round.

**Stop rather than improvise.** If a step is impossible as written, or the code differs
from what is quoted here, halt and report. Do not bend a test to make it pass.

## 1. What changes and why

The user looked at the real screen. The selected binding's card reads better **above**
the grouped list: the eye lands on one fixed place, and the list scrolls beneath it.
Two small fixes ride along:
- The card border (`#22262e`) is nearly invisible on the user's terminal.
- The card prints `+1 commits`.

## 2. Layout (the rule)

The `:fleet` body, top to bottom:

1. One blank line.
2. The filter input line, while the filter is open.
3. **The card** (5 lines, unchanged content), only when a row is selected and the body
   height is at least 16. Otherwise nothing.
4. One blank line, only when the card is drawn.
5. **The list window**: the grouped sections and the done fold line. It scrolls to keep the
   cursor's lines visible and is padded with blank lines to its height.
6. **The gated line**, as the body's last line, only when there are gates. There's no blank
   line between the window and it, because the window's own padding separates them.

listH = height - (1 + filterLine) - (card ? 6 : 0) - (gated ? 1 : 0), floored at 0.

Reference, 132×34, the round-1 fixture:

```
  ◆ relevo    fleet                                                          ● 1 needs you     v0.13.0-28    14:02
   7 live   1 needs you   2 working   4 idle   27 done                              sort attention · refreshed 0s ago

 ╭─ fix-433  round 2 · question · 3m ─────────────────────────────────────────────────────────────╮
 │  planner architect-3  ·  deepseek-v4.1-flash  ·  relevo/fix-433  ·  +2 commits  ·  $0.03       │
 │                                                                                                │
 │    enter  open round       s  send the next plan       o  shell       x  stop                  │
 ╰────────────────────────────────────────────────────────────────────────────────────────────────╯

     needs you   1    a builder is waiting on an answer
 ▍ ●  fix-433             r2 · question · 3m      deepseek-v4.1-flash     architect-3    $0.03
        ╰ “Should the dedupe also cover archived bindings, or only live ones?”
 … (groups, done fold line, blank padding) …
   ◌ gated  antigravity 1d  ·  openai 25d  ·  the pick skips them

 enter  open      s  send      x  stop      D  done      g  gate      /  filter      :  command      ?  all keys      q  quit
```

## 3. Changes, by location (worktree state after round 1)

**internal/ui/view_fleet.go**
- `bottomLines` (about lines 914-925): split it into two functions and delete it.
  - `cardBlock(env Env, width, height int) []string` returns the card's 5 lines plus one
    trailing blank line (`fit("", width)`), or nil when there is no selected row or `height < 16`.
  - `gatedBlock(env Env, width int) []string` returns `[]string{gatedLine(env, width)}`, or nil
    when `gatedLine` returns "".
- `tableHeight` (about lines 927-944): `h := bh - fixedH - len(cardBlock(env, env.Width, bh)) - len(gatedBlock(env, env.Width))`.
  Drop the `gap` variable.
- `Body` (about lines 985-1036): build
  `out = fixed + cardBlock + window + gatedBlock`, with
  `listH = height - len(fixed) - len(cardBlock) - len(gatedBlock)`, floored at 0. Delete the
  `gap` logic and the blank line inserted before `bottom`.
- `cardLines`, `+%d commits` (about line 722): print `+1 commit` when Commits == 1, otherwise
  `+N commits`.
- Comments that say "bottom" (the `cardLines` doc comment at about line 681, the `gatedLine` one at
  about line 803, the `Body` doc comment): say "top" for the card and "last line" for the gated line.

**internal/ui/styles.go**
- `borderStyle` (line 11): `#22262e` → `#3a4150`. No other colour changes.

`tableHeight` and `Body` must compute the same listH, or scrolling misplaces the cursor.
Both go through the two block functions, so they cannot drift.

## 4. Tests

- Regenerate the goldens with `-update`. Every fleet golden changes (the card moves). Other
  goldens must not change. A non-fleet golden diff is a halt.
- New `TestFleetCardAboveList` (internal/ui/fleet_test.go): with the round-1 `realFleetReport()` at
  132×34, the body line index of `╭─ fix-433` is less than the index of the `needs you`
  section line. The gated line is the last non-blank body line.
- New `TestFleetCardCommitPlural`: `LastClose.Commits` of 1 renders `+1 commit` (and not
  `commits`); 2 renders `+2 commits`.
- The existing scroll tests (`TestFleetKeepsCursorVisibleWhenScrollingDown/Up`,
  `TestFleetRewindowsOnResize`, `TestFleetRewindowsWhenBindingRemoved`) must pass unchanged. If
  one fails, the listH arithmetic is wrong: fix the code, not the test.

## 5. Working efficiently

- Read view_fleet.go lines 680-1040, styles.go and fleet_test.go in one step. Make each
  file's edits in one call.
- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`.
  Goldens: add `-run Golden -update`, then read the fleet golden diffs.
- Final: `gofmt -l $(git ls-files '*.go') internal/ui/*.go`, `go vet ./internal/ui/`,
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`.
  Do not run `make check`: its release guard fails for an unrelated reason (feat-commit count).

## 6. Steps

1. Edit view_fleet.go and styles.go per §3. Verify: `go build ./internal/ui/`.
2. Add the two tests, then regenerate the goldens and read the diffs. Verify: the §5 loop is green.
3. Run the final checks (§5).

## 7. Report

List the files changed, the goldens that changed with one line each, the new
`fleet-real-132.golden` verbatim, and the check results.
