# Cleanup P3 -- finish packages: internal/harness, internal/agentsrc

Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§4, §6 and §7 phase 3). Base: `main` at `5a2856ae`. Packages in scope:
- `internal/harness` (0 non-test lines, 0 test lines)
- `internal/agentsrc` (0 non-test lines, 0 test lines)

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process, and when you end your turn the process exits and the round
  is lost. Do not end your turn until the report and the done marker exist.
- **Only these files may change:** files under the in-scope package directories
  (not their subdirectories unless listed), `.golangci.yml` (only the in-scope
  packages' exclusion rules), `scripts/check-comments.allow` and
  `scripts/check-filesize.allow` (only the in-scope packages' entries),
  `testdata/coverage-baseline.txt` (only if §4 allows), and the plan copy.
- **The exported API is frozen.** Every exported identifier keeps its name,
  signature and behaviour: other packages, and another planner's work in flight,
  use them. Unexported code is yours to restructure.
- **Keep every `t.Parallel()` call** in the tests you touch, and when you merge
  tests into a table, keep the table test and its subtests parallel if the
  originals were (another round is making tests parallel to speed up CI).
- **Golden files and contract tests** (`testdata/*.golden`, `*contract_test.go`)
  are not edited. If one fails, your change altered behaviour: undo it.
- **golangci-lint must actually run.** If `command -v golangci-lint` fails or
  `golangci-lint version` is not 2.14.0, install it with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`
  and put `$(go env GOPATH)/bin` on PATH. A `make check` that prints
  "skipping lint" does not count as passing.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report -- do not improvise. Never add a lint exclusion, a `//nolint`, or an
  allow-list entry to get green.

## 1. The standard (from CLAUDE.md "### Code style")

- A package comment of 1-3 lines saying what the package owns.
- A comment says *why*, and only where the code cannot: a non-obvious
  constraint, where a number comes from, an ordering that matters, a hazard.
  Keep that reasoning when you rewrite a comment -- cut the narration around it,
  not the reason.
- No history: no issue or PR numbers, no spec sections, no "round N", "used to",
  "pre-#NNN", "since 0.13".
- No restating the code. Doc comments on exported names only when they add
  something the name and signature do not.
- Functions at most 70 lines (aim 20-40), cognitive complexity at most 30,
  errors wrapped once with `%w`, no ignored errors. Files at most 600 lines.
- Tests follow the same comment rules; a test's name says what it pins.

## 1b. Hard targets (per package, against the base commit, `5a2856ae`)

The owner wants a **smaller** codebase with **far fewer comments**. For each
package's non-test files: total lines no higher than at the base, and comment
lines (`grep -cE '^\s*//'`) at most half the base count. Test files: lines no
higher, comment lines at most half. A helper with a clear name gets **no**
comment; a comment that restates a name, signature or body is deleted. If a
target cannot be met without changing behaviour or the exported API, stop and
report the numbers.

## 2. Tests

- Near-identical tests collapse into one table-driven test; each row's name says
  what it pins.
- Shared fixtures and fakes go in one `helpers_test.go` per package.
- A test is removed only when another test asserts the same thing: list every
  removed test with the test/row that now covers it.
- Coverage may not drop more than 1.0 point below `testdata/coverage-baseline.txt`
  (`make check` enforces it on this machine's Go 1.27 linux/amd64).

## 3. Steps

1. Read every non-test and test file in scope in one batch. Note the exported
   identifiers (the frozen API): `go doc -all ./<pkg>` before you start, saved to
   `/tmp/api-before-<pkg>.txt`.
2. Remove the in-scope packages' rules from `.golangci.yml` and their entries
   from both allow-lists. Run `golangci-lint run ./<pkg>/...`,
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` to get the
   work list.
3. Rewrite comments to §1, split long or complex functions into named helpers,
   fix every lint finding. Package by package; after each, `go build ./... &&
   go test ./<pkg>/... -count=1`.
4. Consolidate tests to §2.
5. API unchanged: `go doc -all ./<pkg>` again and `diff` against step 1's file,
   ignoring doc-comment text only (compare the declaration lines:
   `grep -E '^(func|type|var|const) '` on both). Must be identical.
6. **Mutation check:** for two consolidated tables, break the condition one row
   covers in production code, confirm that row fails by name, revert with
   `git checkout -- <file>`.
7. `make check` (foreground) passes -- lint, comments, file size and coverage
   guard included.
8. Copy this plan to `docs/plans/2026-09-26-cleanup-p3-harness-agentsrc.md`; commit once.

## 4. Coverage baseline

Do not edit `testdata/coverage-baseline.txt`, except: if an in-scope package's
coverage **rises**, you may raise its line with
`sh scripts/check-coverage.sh --write` and then restore every other package's
line to its old value (only in-scope lines may change). Never lower a line.

## 5. Report

Per package: non-test lines, test lines and comment lines before and after
(`wc -l`, `grep -c '^\s*//'`); lint findings fixed by linter; functions split;
tests removed with what covers them; the API diff result; the mutation checks;
coverage before and after.

## Package note

`internal/harness/agents/*` (the shipped agent definitions) and the agentsrc
goldens (`internal/agentsrc/testdata/*.golden`) are contracts: do not edit them.
If you touch anything that renders agent definitions, run
`sh scripts/agents-shipped.sh` and confirm it reports no drift. Only `.go` files
directly in `internal/harness/` and `internal/agentsrc/` are in scope.

---

# Cleanup P3 (round 2) -- rebase the `internal/harness` / `internal/agentsrc` cleanup onto #549 and #557

The branch `relevo/cl-p3-harness-agentsrc` (PR #559, checked out in this
worktree) holds one commit, `1b725568`, finishing `internal/harness`
(non-test 1,677 -> 1,519 lines, comments 410 -> 205) and `internal/agentsrc`
(417 -> 399, comments 52 -> 26), based on `5a2856ae`. Since then `main`
gained two features that changed `internal/harness`:

- #549 (`9745727c`) "statusline: one status column, the actor named, the clock last";
- #557 (`1d9a5dc6`) "transcript: show builders' thinking; limit and denial scans skip it".

The commit no longer applies (conflict in `internal/harness/harness.go`).

## Rules

- **Run every command in the foreground and wait for it**; never background a
  command or end your turn before the report and done marker exist.
- **#549's and #557's logic wins.** Read `git show 9745727c -- internal/harness`
  and `git show 1d9a5dc6 -- internal/harness` first. Every logic line they added
  or changed survives exactly; only their comments may be brought to the
  standard (why-only, no issue numbers).
- Keep every `t.Parallel()` call present on `main`.
- Scope, freezes and targets as round 1: only files directly in
  `internal/harness/` and `internal/agentsrc/`, their rules in `.golangci.yml`,
  their allow-list entries, and the plan copy. `internal/harness/agents/*` and
  `internal/agentsrc/testdata/*.golden` untouched; exported API unchanged.
  Targets, measured against `4b3115b4`: non-test lines no higher, non-test comment
  lines at most half, tests likewise.
- golangci-lint v2.14.0 must actually run (`--allow-parallel-runners` if its
  lock is held). If a step is impossible as written, stop and report.

## Steps

1. `git fetch origin && git rebase origin/main`. Resolve package conflicts as
   above; resolve `.golangci.yml` / allow-list conflicts by taking `main`'s
   lists and removing only harness's and agentsrc's entries.
2. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh`,
   `sh scripts/agents-shipped.sh --check` ok;
   `go test ./internal/harness/... ./internal/agentsrc/... ./internal/transcript/... -count=1` passes.
3. Confirm #549's and #557's logic lines are present:
   for each commit, the non-comment `+` lines of
   `git diff <c>^ <c> -- internal/harness ':!*_test.go'` all appear in the result.
4. Re-measure against `4b3115b4`; targets hold. `make check` (foreground) passes.
5. Append a "Round 2" section with this plan to
   `docs/plans/2026-09-26-cleanup-p3-harness-agentsrc.md`; commit once. Do not push.

Report: conflicts and resolutions, the step-3 result, the numbers.
