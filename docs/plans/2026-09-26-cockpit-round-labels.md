# Cockpit labels: the round that reported, when the plan was sent, and the gated line's ellipsis (#507, #508, #509) (2026-09-26)

## 1. Overview

This round fixes three cockpit display bugs in `internal/ui`. None of them changes behaviour, only labels.

1. **#507: the fleet shows the next round's number on a row that reported.** Take a binding whose round 2 reported.
   `BindingStatus.Round` is 3, the next round, but the row reads `r3 · reported 2m` and the card reads
   `round 3 · reported 2m`. It should show the round that reported. The Claude status line already follows the rule
   (`internal/relevo/statusline.go` ~line 84, `StatusLineRows` ~line 352): show the round of the last payload to the
   planner (a report or question) when there is one, else `Round`. The fleet applies the same rule through a new helper.
2. **#508: the round page's plan and report tabs show when the tab loaded.** For example, `plan r2 · 22:36`, when the
   plan was sent at 22:32. The plan tab must show the plan log entry's timestamp. The report tab must show the report
   entry's timestamp. When the time is unknown the header shows no time at all (`plan r2`), never the load time. The
   terminal tab's `captured … ago` is a load time on purpose and does not change.
3. **#509: the fleet's gated footer line is cut mid-word with no `…`.** `gatedLine` ends in `fit(line, width)`, and
   `fit` hard-cuts. The gated line must end in `…` when it is cut, as card rows do through `clipName`. `fit` itself
   does not change: its no-ellipsis rule is deliberate for every other caller.

## 2. Files

All changes are in `internal/ui/`:

- `fleet_group.go`: new `shownRound`; `rowNow` uses it (~lines 99-102).
- `view_fleet.go`:
  - `cardLines` (~lines 669-678) uses `shownRound`;
  - `gatedLine` (~line 789) clips with an ellipsis.
- `fetch.go`:
  - `fetchPlan` (lines 98-165) and `fetchReport` (lines 174-260): the event time, not `time.Now()`;
  - `fetchShow` (~lines 661-700): zero `at` for the plan and report sections.
- `view_round.go`: the `pullMsg` case (~line 350) takes the report's event time.
- `round_pane.go` (~lines 403-406): omit the time when `at` is zero.
- Tests go in `fleet_test.go` and `pane_test.go`, and in `golden_test.go` only if a golden needs a fixture change.
  Goldens are under `testdata/`.
- `docs/plans/2026-09-26-cockpit-round-labels.md`: this plan, copied as the last step.

## 3. Contracts

### `shownRound(b relevo.BindingStatus) int` (new, unexported, `fleet_group.go`)

- Returns `b.LastPayload.Round` when all of these hold:
  - `b.LastPayload != nil`;
  - `b.LastPayload.Direction == store.DirToPlanner`;
  - `b.LastPayload.Kind` is `store.KindReport` or `store.KindQuestion`.
- Otherwise it returns `b.Round`.
- The doc comment says why in one line: a closed round's `Round` already names the next round, so the round that
  reported comes from the payload, the same rule as the status line.

### `rowNow` (`fleet_group.go` ~line 99)

- `rndPrefix` uses `shownRound(b)`. The guard stays `> 0`, now tested on the shown round.

### `cardLines` (`view_fleet.go` ~lines 669-678)

- Compute `sr := shownRound(b)` once.
- Use `sr` in place of `b.Round` for:
  - the `TrimPrefix` of `"r%d · "`;
  - `"round %d · %s"`;
  - the `== 0` check.
- Without this, the trim would miss the prefix `rowNow` now writes.

### `gatedLine` (`view_fleet.go` ~line 789)

- Replace `return fit(line, width)` with `return fit(clipName(line, width), width)`.
- `clipName` is at `view_fleet.go:104`. It uses the ANSI-aware `MaxWidth(width-1)` plus `…`, and leaves a line that
  fits untouched.

### Tab times (`fetch.go`)

- `tabContent.at` keeps its type. For the plan and report tabs its meaning becomes "when the event happened; zero when
  unknown". Update the field comment at `fetch.go:42` to say both meanings in one line: the event time for plan and
  report, the read time for the others.
- **`fetchPlan`:**
  - After a successful `ReadFile`, call `rt.Store.ReadLog(name)`.
  - Scan newest-first for the entry with `Round == round`, `Direction == store.DirToBuilder` and
    `Kind == store.KindPlan`. Set `at` to its `TS`.
  - If `ReadLog` fails or finds no such entry, `at` stays zero. A log read error never fails the plan tab.
  - Every other branch of `fetchPlan` sets no `at` (zero). Remove each `at: time.Now()` in lines 98-165.
- **`fetchReport`:**
  - The branch that returns the found entry (with a body or the "has no payload" empty) sets `at: e.TS`.
  - Every other branch sets no `at`. Remove each `at: time.Now()` in lines 174-260.
- **`fetchShow`:**
  - When `t == tabPlan || t == tabReport`, leave `at` zero on both the error and success paths. `relevo.Show`
    carries no event time for those sections.
  - Other sections keep `time.Now()`.
- If removing the `time.Now()` calls leaves `time` unused in `fetch.go`, drop the import. It is still used elsewhere
  in that file, so expect it to stay.

### `pullMsg` (`view_round.go` ~line 350)

- `at` is taken from `row(env.Report, r.pane.detail.name)`. When that row is non-nil and its `LastPayload` is a
  `DirToPlanner` report whose `Round == r.pane.detail.round`, `at` is `LastPayload.TS`. Otherwise `at` is zero.
- `row` is at `view_round.go:89`. If `env.Report` is not how the round view reaches the report, use the same report
  the pane already renders from (`r.pane.report`). If neither exists, halt.

### Source line (`round_pane.go` ~lines 403-406)

- `tabPlan`: `plan r%d · 15:04` when `!c.at.IsZero()`, else `plan r%d`.
- `tabReport`: the same, with `report`.
- The terminal/transcript branch is unchanged.

## 4. Steps

### 0. Working efficiently

**How to work:**
- In one batch, read:
  - `internal/ui/fleet_group.go` (whole, 145 lines);
  - `internal/ui/view_fleet.go:1-30,100-115,660-700,750-800`;
  - `internal/ui/fetch.go:30-60,95-265,655-715`;
  - `internal/ui/view_round.go:80-100,330-360`;
  - `internal/ui/round_pane.go:390-440`;
  - `internal/ui/fleet_test.go` (whole);
  - `internal/ui/pane_test.go:280-330`;
  - `internal/ui/golden_test.go:1-60,100-130,970-1010`.
- Make each file's change in one edit. The `time.Now()` removals in `fetch.go` are mechanical, so do them in the same
  edit pass as the `ReadLog` lookup.

**Commands:**
- First run `mkdir -p $HOME/.cache/go-tmp`, then `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`.
- Focused: `go test ./internal/ui/ -run 'Fleet|Gated|Pane|Round|Golden|Source' -count=1`
- Regenerate goldens: `go test ./internal/ui/ -run Golden -update -count=1`. Then read `git diff internal/ui/testdata`.
- Final:
  - `go test ./internal/ui/... -count=1 -race`
  - `go vet ./internal/ui/...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
  - `make lint`
- If `git ls-files` fails in this worktree, run `gofmt -l` on the touched files by hand, and say so.
- Skip `make check`; the planner runs it. **No test in `cmd/relevo`.**
- Comments say *why*, with no issue numbers, `§` or plan references. Test names say what they pin. Do not add
  entries to any `scripts/*.allow` file or to `.golangci.yml`.

### 1. #507: `shownRound`, `rowNow`, `cardLines` (§3)

### 2. #509: `gatedLine` (§3)

### 3. #508: tab times in `fetch.go`, `view_round.go`, `round_pane.go` (§3)

### 4. Tests

Add these to `fleet_test.go`. Build the fixtures the way the existing tests there do (see `TestFleetRowDropOrder`'s
`BindingStatus`). Use a fixed `now`.

- **a. `TestFleetReportedRowShowsTheRoundThatReported`:**
  - The binding is idle (whatever `groupOf` needs for `groupIdle`).
  - `Round: 3`; `LastPayload: {Round: 2, Direction: DirToPlanner, Kind: KindReport, TS: now-2m}`.
  - `stripANSI(rowNow(b, now))` starts with `r2 · reported`.
  - `stripANSI` of the card title line from `fleetView{}.cardLines(env, b, 140)` contains `round 2 · reported` and
    does not contain `round 3`.
- **b. `TestFleetWorkingRowShowsTheRoundInFlight`:**
  - The binding is working.
  - `Round: 3`; `LastPayload: {Round: 3, Direction: DirToBuilder, Kind: KindPlan}`.
  - `rowNow` starts with `r3 · `.
- **c. `TestGatedLineEndsInEllipsisWhenCut`:**
  - `env.Report.Gated` has at least three tokens with long provider names. The line must exceed 80 cells.
  - At width 80, `lipgloss.Width(gatedLine(env, 80)) == 80`, and the stripped line, right-trimmed, ends in `…`.
  - At a width wide enough to fit, the line has no `…`.

Add this to `pane_test.go`, next to the test at ~line 314 that asserts `report r2 · 13:02`:

- **d. `TestSourceLineOmitsTimeWhenUnknown`:** a plan tab and a report tab with a zero `at` render `plan r2` and
  `report r2`, with no ` · `.

Add these two fetch tests to `pane_test.go`, or to a new `fetch_test.go` if one does not exist. Use a real
`store.Store` in a `t.TempDir()`, set up the way other ui tests build one (`golden_test.go` or `fleet_test.go:196`).

- **e. `TestFetchPlanTimeIsWhenThePlanWasSent`:**
  - Append a plan entry for round 2 with a fixed `TS` well in the past, and write the round-2 plan file.
  - Run `fetchPlan(...)()`. The `tabMsg`'s `content.at` equals that `TS`.
- **f. `TestFetchReportTimeIsWhenTheReportArrived`:** the same for a round-2 report entry and `fetchReport`.

If the store's API makes e/f much larger than ~40 lines each, drop them and say so. Test d, plus the golden diffs,
then carry #508.

**Goldens.** Regenerate them. Then check each changed golden:
- `round-archived` should lose its time, since it is a hist row read through `fetchShow`.
- `round-real-*` and `round-needs-you` keep a time only if their fixture goes through `fetchPlan` with a plan log
  entry. Their time must now be the entry's time.

In the report, list every golden that changed and the one line that changed in each. A golden changing anywhere except
the source line and the fleet's round labels is a halt.

**Required mutations.** Run each, report the failing test names, then revert:
1. Make `shownRound` return `b.Round` always. Test a must fail.
2. In `gatedLine`, go back to `fit(line, width)`. Test c must fail.
3. In `round_pane.go`, always print the time. Test d must fail.
4. In `fetchPlan`, set `at: time.Now()` on the success path. Test e must fail (skip this if e was dropped).

### 5. Checks, the plan, the commit

1. Run the final commands listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-cockpit-round-labels.md`.
3. Make a new commit:

   ```
   git add -A && git commit -m "fix(cockpit): the round that reported, when the plan was sent, and an ellipsis on the gated line (#507, #508, #509)"
   ```

## 5. Deletions

These are the only deletions:

1. Each `at: time.Now()` inside `fetchPlan` and `fetchReport`.
2. `at: time.Now()` on the plan/report paths of `fetchShow`, done by setting it conditionally.

Nothing else is removed. `fit` keeps its behaviour and its comment.

## 6. Stop rather than improvise

Halt and report if any of these happens:
- `BindingStatus.LastPayload` is not populated for the fleet's rows. That is, the cockpit's `Report` does not carry it.
- A plan entry in the log is not `DirToBuilder`/`KindPlan`.
- The round view cannot reach the binding row in the `pullMsg` case.
- A golden changes outside the lines named in step 4.
- An existing test asserts the old wrong round or a load-time header. Report it; do not bend it.
