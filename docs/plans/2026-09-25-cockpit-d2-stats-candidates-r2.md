# Cockpit D2 stats, candidates tab round 2: roles from the registry, no empty clauses

Date: 2026-09-25. Worktree: ck-d2-stats. The branch is one commit, `d6d687d`. Build on it, and amend it at the end
(`git commit --amend --no-edit`). Do not rebase or push. This is one round.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/relevo/` (one new function and its test);
- `internal/ui/view_stats.go` and `internal/ui/view_stats_test.go`;
- the stats goldens;
- `docs/plans/`.

## 1. What is wrong

The planner ran the round 1 binary on real data. There are two defects in the detail block (`statsCandDetail`):

1. **Roles are missing.** Line 1 reads `deepseek-v4.1-flash   opencode · cline-pass`, but it should read
   `… opencode · cline-pass · builder`.
   - The code reads `candidate.Candidate.Roles`, which is empty for this user: their roles come from the actors config.
   - `relevo config` gets them from the role registry:
     - see `FormatCandidatesLatencyFor` (`internal/relevo/candidates_list.go:40-56`);
     - it iterates `reg.Names()`, keeping each name where `reg.Serves(name, c.Ref())`;
     - `reg` is `rt.RoleRegistry()` (`internal/relevo/runtime.go:287-292`), which never returns nil.
2. **An unknown value leaves an empty clause.** An unknown ttft reads `median 27m · ttft · · 1 halted` (the golden's line
   3). Fix it by leaving the clause out instead.

## 2. Contracts

### 2.1 `func CandidateRoles(rt Runtime, token string) []string` (new, in `internal/relevo/candidates_list.go`)

- **Returns** the role names that serve the candidate `token`, in `reg.Names()` order, where `reg` is `rt.RoleRegistry()`.
- **The fallback:** when no registry role serves it, return the candidate's own `Roles` from `rt.Candidates.Lookup`.
- **It returns nil** when the token does not parse, the set is nil, or neither source has any role.
- **Precondition:** none. It never panics on a zero `Runtime`.
- **Test** it in `internal/relevo/candidates_list_test.go`, or the existing test file for that source, with two cases:
  - a registry built the way the neighbouring tests build one (read them to see how), for a candidate that serves
    `builder` → `["builder"]`;
  - a zero Runtime → nil.
  - No network and no harness.

### 2.2 `statsCandDetail` line 1

- Line 1 uses `relevo.CandidateRoles(env.Src.Base(), token)` joined with `", "`, in place of `Candidates.Lookup(...).Roles`.
- With nil roles, leave out the ` · roles` segment, as today.

### 2.3 `statsCandDetail` line 3

- Build the line from clauses and join them with ` · `.
- Leave out any clause whose value is unknown:
  - `median X` when there is no median;
  - `ttft X` when there is no ttft.
- `N halted` and `N switches` always show.
- The `in … · cache … · out …` group and its three trailing spaces stay omitted when `Measured == 0`, as today.

## 3. Tests

1. `TestStatsCandidatesTabDetail` (existing):
   - add that with a row whose `HasTTFT` is false, line 3 contains no `ttft` and no `· ·`;
   - also that with `HasMedian` false it contains no `median`.
2. `TestStatsCandidatesTabRoles` (new):
   - **Setup:** a `statsTestEnv` whose `plannerSource` Runtime carries a registry, the same kind §2.1's test builds, serving
     the fixture's first token as `builder`.
   - **Assert:** detail line 1 ends with `· builder`.
   - If the test env cannot carry a registry without touching shared helpers, test only `CandidateRoles` (§2.1), and say
     so in the report.
3. Regenerate the goldens once:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`
   - Only `stats-candidates-132.golden` may change, and only its detail block. If any other golden changes, halt.

## 4. Steps

1. `CandidateRoles` and its test.
   - **Done when:** `go test ./internal/relevo/ -run CandidateRoles -count=1` passes.
2. `statsCandDetail` lines 1 and 3, and the tests in §3.
   - **Done when:**
     `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Stats|Golden' -count=1` passes after the
     golden regeneration.
3. The full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./internal/relevo/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/ ./internal/stats/ ./internal/relevo/`
   - Everything green, and `gofmt -l` empty.
4. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-stats-candidates-r2.md` into the worktree's
   `docs/plans/`. Then `git add -A && git commit --amend --no-edit`.
   - **Done when:** one commit on `origin/main..HEAD`, and a clean tree.

## 5. Report

Include:
- the tests added or changed;
- the new stripped detail block of `stats-candidates-132.golden`;
- `git diff --stat d6d687d HEAD`.
