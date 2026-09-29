# Cleanup P3 -- finish packages: internal/db

Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§4, §6 and §7 phase 3). Base: `main` at `5eb39a8`. Packages in scope:
- `internal/db` (3847 non-test lines, 3367 test lines)

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
- **Golden files and contract tests** (`testdata/*.golden`, `*contract_test.go`)
  are not edited. If one fails, your change altered behaviour: undo it.
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

## 1b. Hard targets (per package, against the base commit, `5eb39a8`)

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
8. Copy this plan to `docs/plans/2026-09-26-cleanup-p3-db.md`; commit once.

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

The schema, migrations and stored values are a contract
(`internal/db/testdata/schema.golden`): no SQL, table, column or migration
changes. Only comments, function structure and tests.

---

## Round 2

# Cleanup P3 (round 2) -- rebase the `internal/db` cleanup onto #530

Round 2 of `cl-p3-db`. Round 1 (commit `0e9a9ce`, base `5eb39a8`) finished
`internal/db`: non-test 3,847 -> 3,459 lines, comments 523 -> 261; tests
3,367 -> 3,113. Meanwhile `main` gained #530, "fix(db): open a current database
without the write lock" (commit `dcf60d9`), which changed `internal/db/db.go`
and `internal/db/db_test.go`. The round-1 commit no longer applies.

## Rules

- **Run every command in the foreground and wait for it**; never background a
  command or end your turn before the report and done marker exist.
- **#530's behaviour wins.** Read `git show dcf60d9` first. Every line of logic
  it added or changed survives exactly; only its comments may be brought to the
  standard (why-only, no issue numbers -- #530 added at least one: see
  `internal/db/db_test.go` around line 128).
- Same scope, freezes and targets as round 1: only `internal/db/`,
  `.golangci.yml` (the db rule), the two allow-lists (db entries), the plan copy.
  Schema, SQL, migrations and `internal/db/testdata/schema.golden` untouched.
  Exported API unchanged. Targets, measured against `dcf60d9`: non-test lines no
  higher, non-test comment lines at most half, tests likewise.
- golangci-lint v2.14.0 must actually run (`golangci-lint version`); a
  `make check` that prints "skipping lint" does not count.
- If a step is impossible as written, stop and report.

## Steps

1. `git rebase origin/main` (fetch first). Resolve every conflict in
   `internal/db` by keeping #530's logic and round 1's cleanup of everything
   else; resolve `.golangci.yml` / allow-list conflicts by taking `main`'s
   version and removing only db's entries.
2. Bring any comment #530 added to the standard.
3. `golangci-lint run ./...` 0 issues; `sh scripts/check-comments.sh` and
   `sh scripts/check-filesize.sh` ok; `go test ./internal/db/... -count=1` passes,
   including #530's tests.
4. Re-measure against `dcf60d9`; every target holds.
5. `make check` (foreground) passes.
6. Append a "Round 2" section with this plan to
   `docs/plans/2026-09-26-cleanup-p3-db.md`; the branch ends as the rebased
   round-1 commit plus one round-2 commit. Do not push.

Report: how each conflict was resolved, #530's logic lines confirmed intact
(`git diff dcf60d9^ dcf60d9 -- internal/db` vs the result), and the numbers.

---

## Round 3

# Cleanup P3 (round 3) -- rebase the `internal/db` cleanup onto #541

Round 3 of `cl-p3-db`. The branch holds the finished cleanup rebased onto #530
(`6029341b` + `e4671295`). Since then `main` gained #541 (`df619f43`,
"cap the WAL"), which changed `internal/db/db.go` and `db_test.go`:

- a new constant `journalSizeLimit = 64 << 20` with a three-line *why* comment;
- `Open`'s DSN gains `&_pragma=journal_size_limit(%d)` with `journalSizeLimit`;
- `Open`'s doc comment mentions the cap;
- a new test `TestOpenSetsJournalSizeLimit`.

## Rules

- **Run every command in the foreground and wait for it**; never background a
  command or end your turn before the report and done marker exist.
- #541's constant, DSN change and test survive exactly. Its constant comment is a
  genuine *why* -- keep it (tighten wording only if it stays accurate).
- Same scope, freezes and targets as rounds 1-2 (only `internal/db/`, the db
  rule in `.golangci.yml`, db entries in the allow-lists, the plan copy; schema
  and SQL otherwise untouched; exported API unchanged). Targets measured against
  `ac743896`: non-test lines no higher, non-test comment lines at most half.
- golangci-lint v2.14.0 must actually run; use `--allow-parallel-runners` if
  another run holds its lock.
- If a step is impossible as written, stop and report.

## Steps

1. `git fetch origin && git rebase origin/main`. Resolve `internal/db`
   conflicts keeping #541's lines and the cleanup elsewhere; resolve list
   conflicts by taking `main`'s lists and removing db's entries only.
2. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok;
   `go test ./internal/db/... -count=1` passes, including
   `TestOpenSetsJournalSizeLimit`.
3. Re-measure against `ac743896`; targets hold. `make check` (foreground) passes.
4. Append a "Round 3" section with this plan to
   `docs/plans/2026-09-26-cleanup-p3-db.md`; commit once. Do not push.

Report: conflicts and resolutions, #541's lines confirmed present, the numbers.

