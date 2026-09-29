# Cleanup P3 -- finish packages: internal/jsonshape, internal/legacy, internal/patch, internal/setup

Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§4, §6 and §7 phase 3). Base: `main` at `fa0e60e`. Packages in scope:
- `internal/jsonshape` (121 non-test lines, 69 test lines)
- `internal/legacy` (203 non-test lines, 238 test lines)
- `internal/patch` (305 non-test lines, 131 test lines)
- `internal/setup` (88 non-test lines, 131 test lines)

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
8. Copy this plan to `docs/plans/2026-09-26-cleanup-p3-leaves1.md`; commit once.

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

## 6. Package note

`internal/legacy` holds the relay-era names the relay->relevo migration still
reads; it stays until that migration is removed. Clean its comments and code to
the standard, but keep every name and value it exports.

## Round 2

Round 2 of this binding. Round 1 (the last commit on this branch) finished
`internal/jsonshape`, `internal/legacy`, `internal/patch` and `internal/setup`
to the lint rules, but missed the point of the cleanup: the owner asked for a
**smaller** codebase with **far fewer comments**, one he can read line by line.
Round 1 made the packages *bigger* and added comments -- mostly doc comments on
new helpers that restate the helper's name, for example:

```go
// checkRequired rejects the fields every candidate must carry.
// compilePatterns rejects a pattern list holding an invalid regex.
// providerNames is the set of provider names among entries, ...
```

A reader gets nothing from these that the name did not already say.

### 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process, and when you end your turn the process exits and the round
  is lost. Do not end your turn until the report and the done marker exist.
- Same scope and freezes as round 1: only files in `internal/jsonshape`,
  `internal/legacy`, `internal/patch` and `internal/setup` (plus the plan copy);
  exported names, signatures and behaviour unchanged; goldens and contract tests
  untouched; no lint exclusion, `//nolint` or allow-list entry added; coverage
  guard holds. If a step is impossible as written, stop and report.

### 1. Hard targets (measured against `fa0e60e`, the base before round 1)

For each package, over its **non-test** `.go` files:
1. **Lines: no growth.** `wc -l` total at or below the `fa0e60e` total.
2. **Comment lines: at most half** of the `fa0e60e` count
   (`grep -cE '^\s*//'`).
For test files: lines at or below `fa0e60e`, and comment lines at most half.

Measure the `fa0e60e` numbers first with
`git show fa0e60e:<path>` for each file (files renamed in round 1: use the old name).

### 2. How to get there

- **Delete** every comment that restates the name, signature or body. A helper
  with a clear name gets **no** comment.
- A doc comment on an exported name stays only if it says something the name
  and signature do not (a constraint, a unit, a hazard) -- then one or two lines.
- Keep genuine *why*: a number's origin, an ordering constraint, a hazard, a
  compatibility reason. State it in the fewest words; drop the story around it.
- Struct field comments: only where the field's meaning is not obvious from its
  name and type.
- Reduce code where it is equivalent and clearer: merge tiny helpers that are
  called once and add no clarity back into their caller (staying under 70 lines),
  drop dead branches, simplify. Do not change behaviour.
- Tests: delete comments that narrate the test; the test and row names carry it.

### 3. Steps

1. Record the `fa0e60e` numbers (§1) per package.
2. Tighten (§2). After each package: `go build ./... && go test ./<pkg>/... -count=1`
   and `golangci-lint run ./<pkg>/...`.
3. Re-measure; every §1 target must hold. If one cannot be met without changing
   behaviour or the exported API, stop and report the numbers and why.
4. API unchanged: `go doc -all` declaration lines identical to round 1's
   (`grep -E '^(func|type|var|const) '`).
5. `make check` (foreground) passes.
6. Append a "Round 2" section with this plan to the plan copy in `docs/plans/`
   that round 1 wrote; commit once.

Report: per package, a table of non-test lines / non-test comment lines / test
lines / test comment lines at `fa0e60e`, after round 1, after round 2; and five
examples of comments deleted and five kept (with why each kept one earns its place).

### Package notes

- Round 1 left `jsonshape` and `setup` unchanged and `legacy` nearly so; the
  §1 targets apply to all four packages.
- `internal/legacy` holds the relay-era names the relay->relevo migration still
  reads; keep every exported name and value, but its comments go to the
  standard like any other package.

### Round 2 result

Every §1 target was met (see the report table). `golangci-lint` is not
installed on this machine, so `funlen`/`gocognit` compliance was checked by
hand (every function counted, none over 70 lines) instead of by running the
linter; `make check` skips the lint step the same way and still passed. The
exported API is byte-identical to round 1's (`go doc -all` declaration lines,
diffed via a throwaway `git worktree` at round 1's commit). Two mutation
checks (one in `legacy`, one in `patch`) confirmed the surviving tests still
pin real behaviour.

## Fix round

# Cleanup P3 (fix) -- bring `patch.Annotate` under the complexity limit

The branch `relevo/cl-p3-leaves1` (PR #534, checked out in this worktree)
finished `internal/jsonshape`, `legacy`, `patch` and `setup`. Its builder ran on a
machine without golangci-lint, so `make lint` skipped, and one finding slipped
through:

```
internal/patch/anchors.go:13:1: cognitive complexity 33 of func `Annotate` is high (> 30) (gocognit)
```

## Rules

- **Run every command in the foreground and wait for it**; never background a
  command or end your turn before the report and done marker exist.
- Only `internal/patch/*.go` may change (plus the plan copy). `Annotate`'s
  signature and behaviour are frozen; its tests and goldens must pass unchanged.
- Do not add a `//nolint`, a lint exclusion or an allow-list entry.
- Keep the package's §1 targets from its plan: non-test lines at or below 309,
  non-test comment lines at or below 13 (today 295 and 13).
- If a step is impossible as written, stop and report.

## Steps

1. `golangci-lint version` must print 2.14.0; if not, stop and report.
2. Reduce `Annotate`'s cognitive complexity to 30 or less by extracting one or two
   named helpers for its nested branches (no comments on helpers whose names say
   what they do).
3. `golangci-lint run ./...` reports 0 issues; `go test ./internal/patch/... -count=1`
   passes; `make check` (foreground) passes with lint actually running (its
   output shows golangci-lint's result, not "skipping lint").
4. Append a "Fix round" section with this plan to
   `docs/plans/2026-09-26-cleanup-p3-leaves1.md`; commit once. Do not push.

Report: the helpers extracted, the new complexity, and the patch package's line
and comment counts.
