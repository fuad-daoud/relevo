# Cockpit D2, `:rounds` round 2: band, spacing, fit (polish after the planner's real-screen pass)

Date: 2026-09-25. Worktree: `ck-d2-rounds`. The branch is one commit, `64f70d7` (round 1) on `9471682`. Build on
it and AMEND it at the end (`git commit --amend --no-edit`). Do not rebase, push or open a PR.

**Stop rather than improvise.** If the code differs from what this plan quotes, halt and report.

**Scope fence.** Change only:
- `internal/ui/dash/render.go`, `internal/ui/dash/model.go`
- `internal/ui/dash/d2_test.go`, `internal/ui/dash/testdata/*.golden`
- `internal/ui/view_rounds.go`, `internal/ui/dash_host_test.go`, `internal/ui/testdata/rounds.golden`
- `docs/plans/`

## 1. What the real screen showed (132×34, 100×30, 200×40, live relevo.db)

1. **The cursor band covers only the first cell.** On a round row only the `19:31` cell has the band. On a
   group row only the key cell has it.

   Cause: `roundLine` (render.go:352-462) and `groupLine` (render.go:622-714) render each cell with its own
   style, join the cells, then wrap the whole line in `m.styles.Selected.Render(content)`. Each inner cell's
   reset sequence ends the outer background, so only the text before the first reset keeps it.
2. **No blank row between the context row and the column header.** The approved board has one, as `:fleet`
   and `:stats` do. `View()` in embedded mode (model.go:437-447) returns `thirdLine()` directly.
3. **At 100 columns the summary overflows.** `6m median` is cut and the right side, `sort newest`, is gone.
   `SummaryLine()` (render.go:123-146) has no width budget.
4. **Archived rows italicise only their STARTED cell** (`16:52`, `16:39`): the same reset problem as 1. The
   italic carries no useful meaning in D2, so it goes (§3, deletion 1).
5. **The group table prints `0` tokens** for groups whose rounds recorded none. The round rows print a faint
   `·` in that case. Make the group rows match.

## 2. Changes

### 2.1 The band (render.go, `roundLine` and `groupLine`)

- On the cursor row, every cell is rendered with its own style plus the band background:
  `s.Background(m.styles.Selected.GetBackground())`. That applies to the `tokensBar` pieces too, so give
  `tokensBar` a `band bool` parameter.
- The 2-cell gaps between cells, the 2-cell indent of an expanded group's round, and the trailing fill up to
  `cw` are rendered with `m.styles.Selected`.
- The 3-cell margins stay unstyled.
- Build the cursor row so no text between column 3 and `width-3` ever sits outside a band span. The simplest
  way: have `add` take the background, join with a band-styled `"  "`, and replace the final
  `fit(line, cw)` + `Selected.Render` with a band-styled right pad of `cw - lipgloss.Width(line)` cells, after
  clipping the line to `cw`.

### 2.2 The blank row (model.go)

- Embedded `View()` becomes `"" + "\n" + thirdLine() + "\n" + grid`. The first body line is empty.
- `gridHeight()` subtracts 2 when embedded (it was 1).
- Non-embedded mode is unchanged.

### 2.3 The summary fits (render.go + view_rounds.go)

- `SummaryLine()` becomes `SummaryLine(maxWidth int) string`. It builds the same items as today, then drops
  items from the END until `lipgloss.Width(joined) <= maxWidth`. It never drops the first two
  (`rounds`, `tokens`); if even those do not fit, it returns them clipped.
  `maxWidth <= 0` means no limit (used by non-embedded `View`, which passes `cw`).
- `roundsView.Context` (view_rounds.go:86-109) builds `right` first. Then it calls
  `SummaryLine(env.Width - 3 - lipgloss.Width(right) - 3)`: the left margin, the right side (which includes
  its own trailing 3), and at least 3 cells of gap.

### 2.4 Archived styling (render.go:454-456)

Delete the `if r.Archived { line = m.styles.Archived.Render(line) }` branch. `Styles.Archived` stays as a
field; dash no longer uses it.

### 2.5 Group TOKENS (render.go, `groupLine`)

When `g.Tokens == 0`, the TOKENS cell is `·` in Faint.

## 3. Deletions (closed list)

1. The archived-row italic in `roundLine`. A test asserting it, if any, is ported to assert that the row
   renders without it, citing this item.

## 4. Tests (`internal/ui/dash/d2_test.go` unless noted)

- `TestCursorBandSpansRow`:
  - Set `lipgloss.SetColorProfile(termenv.TrueColor)` and restore it with defer, as `internal/ui/fleet_test.go:345`
    does.
  - Give the model Styles with a distinct `Selected` background (e.g. `#123456`) and coloured Fg, Dim, Ok and
    Faint foregrounds.
  - Render the flat View at 132×20, then the `by:repo` View.
  - For the cursor line of each, walk the ANSI string and track whether a background is active (SGR `48;…`
    sets it; `0`, `49` or a bare `m` clears it).
  - Assert that every printable rune at display columns `[3, 129)` is drawn with the background active, and
    the 3 margin cells on each side are not.
  - Write the tracker as a small helper in the test file.
- `TestEmbeddedBlankRowAboveHeader`: embedded at 132×20, View line 0 is blank (spaces only) and line 1 has
  `STARTED`. The View has exactly 20 lines.
- `TestSummaryLineFits`: with rows that yield every item, `SummaryLine(60)` is at most 60 wide, starts with
  `N rounds`, and does not contain `median`. `SummaryLine(0)` contains `median`.
- Host, in `internal/ui/dash_host_test.go`, `TestRoundsContextFitsAt100`: at width 100, Context's
  `lipgloss.Width(left) + lipgloss.Width(right) <= 100`, and `right` contains `sort newest`. Use the rows of
  `goldenRoundsModel`, plus enough outcomes to make the summary long: add rows in the test, and do not change
  the shared fixture.
- `TestGroupZeroTokensDot`: a `by:binding` group whose rows have no tokens shows `·` in the TOKENS column.
- Regenerate the goldens (dash and `rounds.golden`), and read them: expect only the blank row, the dot, and the
  row count to change.

## 5. Mutation check

Report its result, and check the mutated build compiles:

1. Remove the band background from the cells, keeping it only on the gaps. `TestCursorBandSpansRow` must fail.

Restore it afterwards.

## 6. Working efficiently

- Read `render.go` lines 115-160, 300-470 and 600-720, and `model.go` 375-450, once, in one step.
- Make each file's change in one edit.
- Iterate with `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/dash/ ./internal/ui/ -count=1`,
  and add `-run TestGoldenViews -update` for the goldens.
- Full check, once:
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/... ./internal/stats/ ./cmd/relevo/ -count=1 && go vet ./internal/ui/... && test -z "$(gofmt -l $(git ls-files '*.go'))" && sh scripts/check-name.sh`
- Do NOT run `make check`.

## 7. Steps

1. §2.1-§2.5.
2. §4 tests and goldens.
3. §5 mutation check.
4. Full check.
5. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-rounds-r2.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 8. Report

Include:
- the tests;
- the mutation result, with the build line;
- the changed goldens, one line each;
- `git diff --stat 9471682 HEAD`.
