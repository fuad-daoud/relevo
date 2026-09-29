# Cockpit D2 stats, repos/reliability round 2: honest share bars, % TOKENS, a (no feature) row, short gate reasons

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `52ca005`. Build on it, and amend it at the end
(`git commit --amend --no-edit`, adding only the files this plan names; there must be no stray files). Do not rebase or
push. Line numbers are exact in the worktree now. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/stats/stats.go` and `internal/stats/stats_test.go` (§2.3 only);
- `internal/ui/view_stats.go` and `internal/ui/view_stats_test.go`;
- the stats goldens;
- `docs/plans/`.

## 1. The user's review of the repos and reliability tabs

1. **The share bar disagrees with its number.** Today the bar is scaled to the table's largest row, so the features table's
   top row (`cockpit`, 11%) draws a full 10-cell bar. Every row with tokens also draws at least one cell. **The bar must be
   the percentage:** 10 cells = 100%, so 90% draws 9 cells, 11% draws 1, and under 5% draws none.
2. **Rename the `SHARE` header to `% TOKENS`,** everywhere it appears: the overview's repos table and both repos-tab tables.
3. **Add a `(no feature)` row.** Features are an optional label, so the features table covers about 11% of the window's
   tokens and never adds up. A last row for the rounds with no feature closes the gap. The repos table already has
   `(no repo)`.
4. **Shorten the gate reasons.** The reliability tab's REASON column shows the provider's raw error, for example:
   - `RESOURCE_EXHAUSTED (code 429): Individual quota reached (re-gated after provider rename …)`
   - `error: Individual quota reached. Please upgrade your subscription…`
   - `Codex usage limit: try again at Oct 19th, 2026 10:14 PM (seen by relay candidates --probe)`

   Keep only the readable part: `Individual quota reached` and `Codex usage limit`.

## 2. Changes

### 2.1 Share bars (`internal/ui/view_stats.go:1462-1500`)

`statsShareParts(tokens, maxTokens, total)`:
- the bars become `round(pct / 10)`, where `pct = 100 * tokens / total`, clamped to 0..10;
- there is **no** minimum of 1;
- `maxTokens` is no longer used. Remove the parameter from `statsShareParts`, `statsSharePlain` and `statsShareCell`, and
  from every caller.

The percent text is unchanged (`%3.0f%%`), and so is the 17-cell layout.

### 2.2 The header rename

Replace the header text `SHARE` with `% TOKENS`, left-aligned in the same 15-cell field:
- line 1433 (the overview's repos header);
- line 2401 (the repos tab's header);
- the features header, if it builds its own.

Update the comments that name `SHARE` (lines 1462, 1482, 1489, 1505, 2319, 2363, 2373, 2392, 2435) to say `% TOKENS`, or
"the share cell".

### 2.3 `(no feature)`: `stats.Report.NoFeature`

**In the stats engine:**
- Add `NoFeature GroupRow` to `stats.Report` (`internal/stats/stats.go:44-55`), after `Features`.
- Compute it as `groupRows(in, rows, noFeatureKey)`, where `noFeatureKey` returns `("(none)", true)` when `r.Feature` is nil
  and `("", false)` otherwise. It yields at most one row, which is `NoFeature`; with no such rows, it is the zero value.
- `Features` and `render.go` are **unchanged**, so the CLI output does not move.

**In the repos tab:**
- When `len(features) > 0` and `NoFeature.Rounds > 0`, the features table gets a **last** row for `NoFeature`, named
  `(no feature)`. It is not sorted in with the others; it always comes last.
- It joins the combined cursor list after the features.
- **`enter` on it** returns `notice("rounds with no feature cannot be filtered")`.
- **Its detail block** uses `statsGroupDetail`, whose line 1 has no key text: like `(none)` for repos, the name then nothing.

### 2.4 `func statsGateReason(note string) string`

This is used for REASON in `reliabilityTabLines` (line 2245), in place of the raw `g.Note`. The steps, in order:
1. If the note contains `"short_error":"…"` (the agy JSON form), take that string's value.
2. Repeatedly strip, case-insensitively, a leading `AGY_ERROR:`, `error:`, or `RESOURCE_EXHAUSTED (code 429):`, plus the
   surrounding spaces.
3. Remove every parenthesised segment `( … )`, and the space before it.
4. Cut at the first `. ` or `: `, and drop a trailing `.`.
5. Trim. When the result is empty, return `·`.

Expected results:

| note | reason |
|---|---|
| `RESOURCE_EXHAUSTED (code 429): Individual quota reached (re-gated after provider rename antigravity -> agy-extra)` | `Individual quota reached` |
| `error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h37m3s.` | `Individual quota reached` |
| `Codex usage limit: try again at Oct 19th, 2026 10:14 PM (seen by relay candidates --probe)` | `Codex usage limit` |
| `AGY_ERROR: {"short_error":"RESOURCE_EXHAUSTED (code 429): Individual quota reached. Please upgrade…","status":"RESOURCE_EXHAUSTED"}` | `Individual quota reached` |
| `` (empty) | `·` |

## 3. Tests

**`internal/ui/view_stats_test.go`:**
1. **Port** `TestStatsShareCell` (line 927) to the new rule:
   - 90% → 9 cells, 54% → 5, 11% → 1, 4% → 0, 0 tokens → 0;
   - the cell is always 17 cells.
2. **Port** every test that asserts the text `SHARE` to `% TOKENS`. List them in the report.
3. **New:** `TestStatsReposNoFeatureRow`, on a fixture with one feature and a `NoFeature` of 5 rounds:
   - the last features row is `(no feature)`;
   - `enter` on it gives the notice and no push;
   - with `Features` empty, there is no `(no feature)` row, and no features table.
4. **New:** `TestStatsGateReason`: the §2.4 table.

**`internal/stats/stats_test.go`:**

5. **New:** `TestNoFeatureGroup`:
   - 3 rows, one with a feature and two without, gives `NoFeature.Rounds == 2` and `Key == "(none)"`;
   - `Features` holds only the labelled one.

**Goldens:** regenerate once. The overview, repos and reliability goldens change: bars, header and reasons. The candidates
and tokens goldens must not change. If they do, halt.

## 4. Steps

1. §2.1 and §2.2, with tests 1 and 2.
2. §2.3, with tests 3 and 5.
3. §2.4, with test 4.
4. The goldens, then the full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/`
5. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-repos-reliability-r2.md` into `docs/plans/`, then
   `git add` the named files, and `git commit --amend --no-edit`.

## 5. Report

Include:
- the tests added and ported;
- the stripped `stats-repos-132.golden` and `stats-reliability-132.golden`;
- `git diff --stat 52ca005 HEAD`.
