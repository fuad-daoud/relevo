# Cockpit D2 stats, tab 2: the candidates tab

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `3f853cb`, on origin/main 7db0ec7d. Build on it. At the end,
commit by **amending** that commit (`git commit --amend --no-edit`). Do not rebase or push. Line numbers below are exact in
the worktree now. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, or a step cannot be done as written, halt and
report. Change only:
- `internal/stats/stats.go` and `internal/stats/stats_test.go` (§3.1 only);
- `internal/ui/view_stats.go`, `internal/ui/view_stats_test.go` and `internal/ui/golden_test.go`;
- the stats goldens;
- `docs/plans/` (step 7).

## 1. System overview

`:stats` has five tabs. The **overview** tab is done and approved. This round rebuilds the **candidates** tab (tab 2). Today
it draws the old C2 scorecard panel (`candidatesLines`, `view_stats.go:1425-1458`), a titled rule, and a footnote reading
`unrecorded: N rounds`.

The new tab is one full-width table of every configured candidate, followed by a detail block for the selected row. The
approved design board, drawn at 132 columns on real data (`·` means no value):

```
   CANDIDATE                              RNDS  DONE  HALTED   MED   TTFT   IN/RND  OUT/RND  CACHE  MEASURED   STATUS
   deepseek-v4.1-flash                     215  100%      24    7m   2.5s     6.2M    53.4k    98%   212/215   ready
   gemini-3.8-flash-high                    86   99%       3    6m   7.0s     9.6M    95.9k    88%     35/86   gated 7m
   sonnet                                   69  100%       0   12m   2.8s     8.1M    33.1k   100%      7/69   ready
   claude-sonnet-4-6                         0     ·       ·     ·      ·        ·        ·      ·         ·   gated 1d
   glm-5.3-flash                             0     ·       ·     ·      ·        ·        ·      ·         ·   ready


   deepseek-v4.1-flash   opencode · cline-pass · builder
   215 rounds on 130 bindings   1.32B tokens, 77% of the window
   in 19.9M · cache 1.29B · out 11.3M   median 7m · ttft 2.5s · 24 halted · 6 switches
```

The user's rules, carried over from the overview:
- **The two tabs match.**
  - The candidate rows and their order are exactly the overview's (`overviewCandRows`).
  - HALTED is the **report** count, the same source as the overview's ROUNDS tile (`… halted`).
- **No captions and no footnotes.** Drop the `unrecorded: N rounds` line entirely. Candidate-less rounds only come from the
  retired pane mode, and new rounds always record a candidate.
- **Three cells of margin on each side:** nothing extends past `width - 3`.
- **Idle candidates** (configured, no rounds) are faint rows with `0` and `·`.
- **The page scrolls** with the cursor, as the overview does since round 6.

## 2. Working efficiently

- Read these once, in one batched step:
  - `internal/ui/view_stats.go` in full;
  - `internal/ui/view_stats_test.go` in full;
  - `internal/ui/golden_test.go:455-620`;
  - `internal/stats/stats.go:100-125` and `:253-340`;
  - `internal/stats/stats_test.go` (skim it for the existing scorecard test's shape);
  - `internal/ledger/ledger.go:228-250`;
  - `internal/candidate/candidate.go:20-60` and `:110-160`.

  Every location you need is named here.
- Make the changes to `view_stats.go` in as few edit calls as you can.
- The focused commands:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/stats/ ./internal/ui/ -run 'Score|Stats|Golden' -count=1`
- Regenerate the goldens once, at the end:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`
- The full check, once:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
- These are pure tests: none may spawn a harness or reach the network.

## 3. Data structures

### 3.1 `stats.ScoreRow` gains three fields (`internal/stats/stats.go:106-123`), filled in `buildScorecard` (`:255-`)

| field | type | meaning |
|---|---|---|
| `Bindings` | `int` | The number of distinct `RoundRow.BindingID` among the candidate's rows. |
| `ReportHalted` | `int` | The rows whose `ReportOutcome` is non-nil and equals `"halted"`. |
| `Switches` | `int` | The sum of `RoundRow.Switches` over the candidate's rows. |

- Add them to the `acc` struct (a `map[string]bool` for the bindings) and set them on the `ScoreRow`.
- Leave `Halted`/`HaltPct` (round outcome) alone.
- `render.go` does not use the new fields, so the CLI text output and its golden are **unchanged**.

### 3.2 `statsView` (in `internal/ui/view_stats.go`)

There are no new fields.
- The candidates tab uses the existing `cursor[0]`, with `focus` 0.
- `cursor[0]` now indexes `v.overviewCandRows(names)`, not `v.rep.Scorecard`.

### 3.3 Column set: `statsCandCol`, unexported

| field | type | meaning |
|---|---|---|
| `Head` | `string` | The header label. |
| `W` | `int` | The cell width. Numbers are right-aligned in it. |
| `Drop` | `int` | The drop rank: 0 never drops. The columns with higher ranks drop first when the name column would fall under 16 cells. |

The columns, left to right, after the name column:

| head | W | drop | value |
|---|---|---|---|
| `RNDS` | 6 | 0 | `Rounds` |
| `DONE` | 6 | 0 | `stats.PctText(DonePct, Closed)` |
| `HALTED` | 8 | 0 | `ReportHalted` |
| `MED` | 6 | 3 | the median: `stats.Duration(MedianMS)` when `HasMedian && Closed > 0`, else `·` |
| `TTFT` | 7 | 2 | `stats.TTFTText(s)`, but `·` in place of `-` |
| `IN/RND` | 9 | 0 | `ShortTokens((In+Cache)/Measured)`, `·` when unmeasured |
| `OUT/RND` | 9 | 0 | `ShortTokens(Out/Measured)`, `·` when unmeasured |
| `CACHE` | 7 | 0 | `CachePct`, as `%.0f%%`, `·` when unknown |
| `MEASURED` | 10 | 1 | `Measured/Rounds`, e.g. `212/215` |

Then three spaces and `STATUS`, left-aligned in 10 cells.

- **Table width:** `width - 6`, starting at column 3.
- **Name column:** `nameW = width - 6 - sum(W of the visible columns) - 3 - 10`.
- **Dropping columns:** while `nameW < 16` and a droppable column remains, drop the highest rank still visible (MEASURED,
  then TTFT, then MED) and recompute.
- **Clipping:** the name is clipped with `stats.FitKey`, and so is the header's `CANDIDATE` label.
- **An idle row:** name, `0` under RNDS, `·` in every other visible column, and its status.

## 4. Contracts (all in `internal/ui/view_stats.go` unless named)

### 4.1 `func statsGateLeft(g ledger.Gate, now time.Time) string`

- **Responsibility:** a gate's STATUS text.
- `"gated"` when `g.Until` is zero.
- Otherwise `"gated " + d`, where `d = Until - now` rounded down:
  - under 1h: `<n>m`, minimum `1m`;
  - under 24h: `<n>h`;
  - else `<n>d`.

### 4.2 `func (v statsView) statsCandStatus(token string, now time.Time) (text string, style lipgloss.Style)`

- The first gate in `v.rep.Reliability.Active` whose `Token == token` and whose `Role` is `""` or `"builder"` gives
  `statsGateLeft(g, now)` in `redStyle`.
- With no such gate, it is `"ready"` in `greenStyle`.
- On an idle or `Few` row, the status keeps its colour. Only the name and the numbers dim.

### 4.3 `func (v statsView) candidatesTabLines(env Env, width int) (lines []string, sel int)`

This replaces `candidatesLines` (1425-1458). The layout, in order:

1. **The header row:** `"   "` + faint bold, the name label then each visible column's head right-aligned in its `W`, then
   `"   STATUS"`.
2. **One line per `overviewCandRows(names)` row** (`names = env.Src.Base().Candidates.NameOf`), all rows with no windowing.
   The page scrolls instead. Each row is `"   "` + name + numbers + `"   "` + status:
   - **normal:** the name in `fgStyle`, the numbers in `mutedStyle`;
   - **`Few` or idle:** the name and the numbers in `faintStyle`;
   - **selected** (`i == clamp(v.cursor[0])`): the name and the numbers rebuilt as plain text in one
     `selBandStyle.Foreground(textStyle.GetForeground()).Bold(true)` span. The status follows in its own colour on the same
     band background (`selBandStyle.Inherit(style)`), fitted so the band ends at `width - 3`.
3. **Two blank lines.**
4. **The detail block** for the selected row (§4.4).

`sel` is the selected row's index in `lines`, which is `1 + i`.

### 4.4 `func (v statsView) statsCandDetail(env Env, c statsOverviewCand) []string`

Three lines, each starting `"   "`, each cut to `width - 6`.

- **Line 1:** the name, faint bold, then `"   "`, then muted `harness · provider · roles`.
  - `candidate.ParseRef(token)` gives the harness and provider.
  - The roles come from `env.Src.Base().Candidates.Lookup(ref)`: its `Roles`, joined with `", "`. They are omitted, with
    their ` · `, when the lookup fails or `Roles` is empty.
  - An unparsable token shows only the name.
- **Line 2** (non-idle): text bold `<Rounds>`, muted ` rounds on `, bold `<Bindings>`, muted ` bindings   `, bold
  `ShortTokens(TokenKinds.Total())`, and muted ` tokens, <p>% of the window`.
  - `p = round(100 * total / Totals.TokenKinds.Total())`, and the clause is omitted when the window total is 0.
  - Use `binding` and `round` for a count of 1.
- **Line 3** (non-idle), all muted:
  `in <ShortTokens(In)> · cache <…Cache> · out <…Out>   median <med> · ttft <ttft> · <ReportHalted> halted · <Switches> switches`.
  - `med` and `ttft` are `·` when unknown.
  - `in … out` is omitted, with its three trailing spaces, when `Measured == 0`.
- **An idle row:** line 1 as above, then one muted line, `no rounds in this window`. There is no third line.

### 4.5 `tabLines` (line 553)

The `"candidates"` case returns `v.candidatesTabLines(env, width)`, with no `panel` rule.

### 4.6 Cursor, enter and follow

- **`panelRows`** (331-336), focus 0: `len(v.overviewCandRows(identity))`. Focus 1 is unchanged.
- **`enter`** (403-420), focus 0:
  - index `overviewCandRows(names)` with `statsClamp(v.cursor[0], n)`;
  - an idle row returns `notice(name + " has no rounds in this window")`, the same text as `enterOverview`;
  - otherwise `roundsForCandidate(env, token)`.
  - Focus 1 is unchanged.
- **`follow`** (592-612) already acts on any tab whose `tabLines` returns `sel >= 0`, so the candidates tab gets it for
  free. Check that `up`/`down` call `follow` on every tab (lines 248-255 show they do).
- **Tab switching** already resets `top`.

### 4.7 Dead code

- Delete `candidatesLines`.
- Delete the constants `statNameW`, `statRndsW`, `statPctW`, `statMedW`, `statTTFTW` and `statCostW` (lines 65-73), and
  their comment, **if** nothing else uses them after the deletion. `statGroup` stays: the repos tab uses it at lines 1720
  and 1722.
- Remove any import that becomes unused.

## 5. Error handling

This is pure rendering. The degenerate inputs are defined:
- **An empty scorecard with no configured candidates:** the header row only, no detail block, and `sel` is -1.
- **A nil candidate set:** there are no idle rows, and line 1 has no roles.
- **A gate for a token that is no longer configured:** it is ignored, since no row carries it.

## 6. Tests

**`internal/stats/stats_test.go`:**
1. `TestScorecardBindingsHaltsSwitches`:
   - **Setup:** one candidate with 4 rows over 3 binding IDs, where one row has `ReportOutcome "halted"`, one has `"done"` and
     two have nil, and `Switches` are 0, 2, 1 and 0.
   - **Expect:** `Bindings 3`, `ReportHalted 1`, `Switches 3`.

**`internal/ui/view_stats_test.go`:**

2. `TestStatsCandidatesTabRows`:
   - `statsTestView("30d")` with `configured` = the fixture's scorecard tokens plus two extra, tab 2, at 132×40.
   - **Assert:**
     - no `──` rule and no `unrecorded`;
     - the header contains `HALTED`, `MEASURED` and `STATUS`;
     - the rows are in `overviewCandRows` order, the same order the overview's table shows;
     - an idle row reads `0` then `·`s;
     - every stripped line's trimmed length is `≤ 129`.
3. `TestStatsCandidatesTabHaltedIsReportCount`:
   - Set the fixture's first ScoreRow `ReportHalted = 7` and `HaltPct = 0`.
   - Its row shows `7` in the HALTED column, found by column alignment with the header's `HALTED`, reusing
     `statsColEnd`.
4. `TestStatsCandidatesTabStatus`:
   - **Setup:** add `ledger.Gate{Token: <second scorecard token>, Until: railNow.Add(26*time.Hour)}` to
     `rep.Reliability.Active`, and render with the env's `Now = railNow`.
   - **Assert:**
     - that row ends with `gated 1d`, and the others end with `ready`;
     - `statsGateLeft` gives `gated 7m` for 7m30s, `gated 3h` for 3h59m, `gated 24d` for 24d5h, and `gated` for a zero
       Until.
5. `TestStatsCandidatesTabDetail`:
   - With the cursor on row 0, the body contains `rounds on`, `bindings`, `% of the window` and `switches`.
   - With the cursor moved to an idle row with `j` presses, it contains `no rounds in this window`.
   - `enter` on the idle row gives the notice and no push. Reuse the pattern `TestStatsOverviewConfiguredCandidates` uses.
6. `TestStatsCandidatesTabDropsColumns`:
   - At 100 columns, `MEASURED` is gone and `TTFT` still shows.
   - At 80, `TTFT` and `MED` are gone too.
   - At both, the name column is at least 16 cells, and no line passes `w-3`.
7. `TestStatsCandidatesTabFollows`:
   - At 132×12 (`bodyHeight` 7), with 12 configured tokens, press `j` 11 times.
   - The last row's name is in the stripped `Body`, and `v.top > 0`.
8. **Update** every existing test that asserted the old panel: `NAME`, `$/RND`, `unrecorded`, or `v.enter` indexing
   `Scorecard` on focus 0.
   - Port the assertion to the new table. Delete none.
   - List each one you port in the report.

**Goldens** (`internal/ui/golden_test.go`):
- Add a case **`stats-candidates-132`**, 132×34: `goldenStatsModel(t, 132, 34, statsOverviewReport())`, then send it the key
  `2` before rendering.
  - If the helper cannot send a key, build the model, then `Update` it with `statsKey('2')`, as the view tests do.
  - Give `statsOverviewReport` one active gate on its gemini-like token, `Until: railNow.Add(26*time.Hour)`, through
    `stats.Inputs.Gates` (the field `StatsInputs` fills).
- Regenerate once. Read the stripped `stats-candidates-132.golden`, and check it against the board in §1: the column order,
  the margins, the detail block, and no rule or footnote.
- Other goldens may change only where the gate appears, which is none on overview, or through the ScoreRow additions,
  which are not rendered. If `stats-overview-132` changes, halt and report.

## 7. Ordered steps

1. **`stats.ScoreRow`** fields and `buildScorecard` (§3.1).
   - **Depends on:** none.
   - **Done when:** test 1 passes, and `go test ./internal/stats/` is green, with the CLI golden unchanged.
2. **`statsGateLeft`, `statsCandStatus`, the column set and dropping** (§3.3, §4.1, §4.2).
   - **Depends on:** 1.
   - **Done when:** test 4's `statsGateLeft` cases pass.
3. **`candidatesTabLines` and `statsCandDetail`,** wired into `tabLines` (§4.3-4.5).
   - **Depends on:** 2.
   - **Done when:** tests 2, 3, 5 and 6 pass.
4. **Cursor, enter and follow, and the dead code deleted** (§4.6, §4.7).
   - **Depends on:** 3.
   - **Done when:** test 7 and the ported tests (8) pass.
5. **Goldens** (§6).
   - **Depends on:** 4.
   - **Done when:** `TestGoldenViews` passes without `-update`, and `stats-overview-132` is unchanged.
6. **The full check** (§2).
   - **Depends on:** 5.
   - **Done when:** all green, and `gofmt -l` is empty.
7. **Plans and commit.**
   - Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-candidates.md` into the worktree's `docs/plans/`.
   - Then `git add -A && git commit --amend --no-edit`.
   - **Depends on:** 6.
   - **Done when:** one commit on `origin/main..HEAD`, and a clean tree.

## 8. Report

Include:
- the tests added and ported;
- the stripped `stats-candidates-132.golden` in full;
- `git diff --stat 3f853cb HEAD`;
- anything you halted on.
