# Cockpit D2 stats: clear the name guard (PR #500's CI failure)

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `222ffc6`. Amend it at the end
(`git commit --amend --no-edit`, adding only the named files). Do **not** push. This is one small, mechanical round.

**Stop rather than improvise.** If `sh scripts/check-name.sh` lists any hit other than the 9 below, halt and report it.
Change only:
- `internal/ui/golden_test.go`
- `internal/ui/view_stats_test.go`
- the goldens that regenerate
- `docs/plans/`

## 1. The failure

PR #500's `check` job failed on every platform in `sh scripts/check-name.sh`. The script refuses the pre-rename name
`relay` in tracked files, per `docs/specs/2026-09-23-rename-relevo-design.md` §4. It reports 9 hits, all test data this
branch added:

1. `internal/ui/golden_test.go:468` — `relay := s("https://github.com/fuad-daoud/relay")`, in `overviewRows`.
2. `internal/ui/golden_test.go:499, 506, 507, 510, 515` — the rows that use that variable.
3. `internal/ui/testdata/stats-overview-132.golden:24` and `stats-repos-132.golden:7`, the rendered `fuad-daoud/relay`.
   These follow from items 1-2.
4. `internal/ui/view_stats_test.go:2672` — a gate note, `"… (seen by relay candidates --probe)"`.

## 2. Changes

1. **`overviewRows`:** rename the variable `relay` to `money`, and its URL to `https://github.com/fuad-daoud/money`. Change
   every use on lines 499, 506, 507, 510 and 515. Nothing else in the fixture changes.
2. **`view_stats_test.go:2672`:** `(seen by relay candidates --probe)` → `(seen by relevo candidates --probe)`. The test's
   expected reason (`Codex usage limit`) is unchanged.
3. **Regenerate the goldens once:**
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`
   - Only `fuad-daoud/relay` → `fuad-daoud/money` may change, plus any column realignment the shorter or longer name
     causes. If anything else changes, halt.
4. **Search for tests that assert the string** `fuad-daoud/relay` or `relay`, with `grep -n 'relay' internal/ui/*_test.go`,
   and update them to match. Item 4 in §1 is the only one known.

## 3. Verify (all on the laptop; these are light)

- `sh scripts/check-name.sh` exits 0.
- `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Stats|Golden' -count=1` passes.
- `test -z "$(gofmt -l $(git ls-files '*.go'))"` passes.

Do **not** run `make check`: a hook refuses it on this laptop, and the planner runs the full suite on the server.

## 4. Commit

Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-name-guard.md` into `docs/plans/`. Then `git add` the
named files, and `git commit --amend --no-edit`.

## 5. Report

Include:
- the `check-name.sh` output;
- `git diff --stat 222ffc6 HEAD`.
