# Cockpit D2, `:log` round 4: blank row, gate reasons, page size

Date: 2026-09-25. Worktree: `ck-d2-rounds`. The branch is one commit, `5ae5dfe`, on `9471682`. Build on it and
AMEND it at the end (`git commit --amend --no-edit`). Do not rebase, push or open a PR.

**Stop rather than improvise.** If the code differs from what this plan quotes, halt and report.

**Scope fence.** Change only:
- `internal/ui/view_log.go`, `internal/ui/view_log_test.go`, `internal/ui/testdata/log-132.golden`
- `internal/ui/view_stats.go` (only `statsGateReason`, lines 2225-2280) and `internal/ui/view_stats_test.go`
- a stats golden only if a gate reason in it changes
- `docs/plans/`

## 1. What the real screen showed (132×34, live relevo.db)

1. **No blank row between the context row and the column header.** `:rounds`, `:fleet` and `:stats` all have
   one. `logView.Body` (view_log.go:651) puts the header on body line 0.
2. **A rate-limited switch reads `to sonnet (#5) · Error 429`.**
   - The switch note's reason is `rate-limited: Error 429: weekly Clinepass limit reached, resets in 1d 4h`.
   - `statsGateReason` (view_stats.go:2242) strips only the prefixes in `statsGateReasonPrefixes` (line 2236):
     `AGY_ERROR:`, `error:` and `RESOURCE_EXHAUSTED (code 429):`.
   - `Error 429:` is not among them, so the reason is cut at its first `: ` and only `Error 429` is left.
   - The reliability tab's REASON cell has the same bug for such notes.
3. **`pgdown` jumps too far.** `pageSize := env.Height - 1` (view_log.go:567) is the terminal height, but the
   list is `bodyHeight(env) - 2` rows (with the blank row this plan adds, `- 3`).

## 2. Changes

1. **The blank row (`logView.Body`):**
   - Body line 0 is blank (`"   " + spaces(cw) + "   "`). Line 1 is the header, or the `/` editor while
     editing.
   - The list gets `height - 2` rows: `visibleHeight := height - 2` at line 812.
   - The empty-state message moves down one line with them.
2. **Gate reasons (`statsGateReason`):**
   - Before the prefix loop, strip one leading HTTP-status prefix matching `(?i)^(error\s+)?\d{3}:\s*`, via a
     new package var `statsGateReasonStatus = regexp.MustCompile(...)`.
     - `Error 429: weekly Clinepass limit reached, resets in 1d 4h` → `weekly Clinepass limit reached, resets in 1d 4h`.
     - `429: Too Many Requests` → `Too Many Requests`.
   - Keep everything else as is.
3. **Page size (`logView.Update`):** `pageSize := bodyHeight(env) - 3` (floor 1). `bodyHeight` is at
   `frame.go:121`.

## 3. Tests

- **`view_stats_test.go`, `TestStatsGateReasonHTTPStatus`:**
  - `statsGateReason("Error 429: weekly Clinepass limit reached, resets in 1d 4h")` is
    `weekly Clinepass limit reached, resets in 1d 4h`.
  - `statsGateReason("error: Individual quota reached. Please upgrade")` is still `Individual quota reached`.
- **`view_log_test.go`:**
  - `TestLogSwitchRateLimitedReason`: a switch note
    `switched builder (rate-limited: Error 429: weekly Clinepass limit reached, resets in 1d 4h): picked claude/anthropic/sonnet for builder: order #5; skipped …`
    gives detail `to sonnet (#5) · weekly Clinepass limit reached, resets in 1d 4h`.
  - `TestLogViewBlankRowAboveHeader`: Body at 132×30 has a blank line 0 (spaces only) and `TIME` on line 1.
  - `TestLogViewPageDown`, at height 34:
    - after `pgdown` from the top, the cursor index is `bodyHeight(env) - 3`;
    - after a second `pgdown`, it is twice that, clamped to the last entry.
- Regenerate `log-132.golden` and read it: only the blank row and one fewer entry row change. A stats golden
  may change only in a REASON cell.

## 4. Mutation check

Report its result, and check the mutated build compiles:

1. Delete the `statsGateReasonStatus` strip. `TestStatsGateReasonHTTPStatus` and
   `TestLogSwitchRateLimitedReason` must fail.

Restore it afterwards.

## 5. Working efficiently

- Read `view_log.go` lines 555-610 and 645-850, and `view_stats.go` 2225-2285, once, in one step.
- Make each file's change in one edit.
- Iterate with `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`, and add
  `-run TestGoldenViews -update` for the golden.
- Full check, once:
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/db/ ./internal/ui/... ./internal/stats/ ./cmd/relevo/ -count=1 && go vet ./internal/ui/... && test -z "$(gofmt -l $(git ls-files '*.go'))" && sh scripts/check-name.sh`
- Do NOT run `make check`.

## 6. Steps

1. §2.
2. §3.
3. §4.
4. Full check.
5. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-log-r2.md` into `docs/plans/`. Then
   `git add -A && git commit --amend --no-edit`.

## 7. Report

Include:
- the tests;
- the mutation result, with the build line;
- the changed goldens, one line each;
- `git diff --stat 9471682 HEAD`.
