# Cleanup P0c (round 3) -- lint and comment tooling with a per-package ratchet

## Round 3: what changed since round 2's halt

Round 2 stopped at step 1, correctly, and changed nothing. Its two findings are
now resolved; they replace the plan text below wherever it disagrees:

1. **Exclusion rules need a second criterion.** golangci-lint v2 rejects a rule
   that names only `path`. Every per-package rule is:
   ```yaml
   - path: '^internal/jsonshape/'
     text: '.*'
   ```
   (no `linters:` key). Round 2 verified this excludes every linter for the path.
2. **The pinned version is v2.14.0**, not v2.12.2: v2.12.2's staticcheck panics
   on the Go 1.27 standard library. The planner has installed v2.14.0 on this
   machine's PATH (`golangci-lint version` should print 2.14.0; if it does not,
   stop and report). Pin v2.14.0 in CI.

Round 2 also found that `internal/jsonshape` lints clean with its rule removed:
keep its rule anyway (phase 3 removes rules), and say so in the report.

The tree is at `0583bea` with only the untracked spec file; start at step 1.


Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§4). **No production `.go` changes.** This round adds the enforcement that later
rounds switch on package by package, and commits the spec.

## 0. Rules for this round

- If a step is impossible as written, or the code contradicts this plan, **stop and
  report** -- do not improvise.
- Only files named in §2 may be created or changed.
- The spec file is already in the worktree as an untracked file
  (`docs/specs/2026-09-25-codebase-cleanup-design.md`); commit it as-is.
- Parallel rounds delete files and add test files. Every allow-list this round
  writes must tolerate a listed path that no longer exists (skip it silently),
  so those rounds never have to touch the lists.

## 1. System overview

The cleanup ends with every package passing golangci-lint (function length,
complexity, error handling, unused code), with no comment citing an issue number
or spec section, and with no non-test file over 600 lines. Today almost nothing
passes, so the rules start with every package excluded and every failing file
allow-listed; phase 3 removes one package's entries per round. `make check` runs
all of it, and CI requires the linter on one matrix leg.

## 2. File structure

```
.golangci.yml                         NEW  linters, settings, per-package exclusions
scripts/check-comments.sh             NEW  fails on #NNN or § inside a // comment
scripts/check-comments.allow          NEW  files exempt today, one path per line
scripts/check-comments_test.sh        NEW  runs the script against fixtures
scripts/check-filesize.sh             NEW  fails on a non-test .go file over 600 lines
scripts/check-filesize.allow          NEW  files exempt today
scripts/check-filesize_test.sh        NEW
scripts/testdata/check-comments/...   NEW  fixture .go files for the test script
scripts/testdata/check-filesize/...   NEW
Makefile                              EDIT `lint` target; `check` runs lint + both scripts
.github/workflows/ci.yml              EDIT install golangci-lint on ubuntu-latest/stable; require it there
CLAUDE.md                             EDIT "Code style" subsection under Conventions
docs/specs/2026-09-25-codebase-cleanup-design.md  ADD (already present, untracked)
docs/plans/2026-09-26-cleanup-p0c-tooling.md      NEW  a copy of this plan (last step)
```

## 3. Data structures (file formats)

**`.golangci.yml`** (golangci-lint v2 format; v2.14.0):
- `version: "2"`
- `linters.default: none`; `linters.enable`: `funlen`, `gocognit`, `errcheck`,
  `errorlint`, `unused`, `govet`, `staticcheck`, `ineffassign`.
- `linters.settings.funlen`: `lines: 70`, `statements: -1`, `ignore-comments: true`.
- `linters.settings.gocognit.min-complexity: 30`.
- `linters.exclusions.rules`: one rule per Go package directory in the module
  **as it is today**, of the form `- path: '^internal/relevo/'` (anchored to
  the directory, trailing slash, so `internal/ui/` does not also match
  `internal/ui/dash/` -- give nested directories their own rule and make the
  parent's path regexp not match them, e.g. `'^internal/ui/[^/]+\.go$'`), with
  `text: '.*'` as its second criterion and no `linters:` key, so every linter
  is excluded for that path. Generate the list
  with `go list -f '{{.Dir}}' ./...` made relative to the repo root. Keep the
  rules sorted, one per line, with a single comment above the block saying
  that phase 3 deletes one rule per finished package.
- `issues.max-issues-per-linter: 0`, `issues.max-same-issues: 0`.
- `run.tests: true` (tests are linted once their package leaves the list).

**`check-comments.allow` / `check-filesize.allow`**: one repo-relative path per
line, sorted, no comments, generated from the scripts' own failures today.

## 4. Interfaces (script contracts)

`scripts/check-comments.sh [--list]`
- Input: `git ls-files '*.go'`, minus every path in `check-comments.allow`.
- A violation: a line containing `//` whose text after the first `//` matches
  `#[0-9]+` or `§` (POSIX `grep -E`; `§` as a literal UTF-8 byte sequence).
  Lines where `//` occurs only inside a string literal are rare; accept the
  false positive and do not parse Go.
- Output: `path:line: comment cites history (#NNN or §)` per violation;
  exit 1 if any, else exit 0. `--list` prints only the distinct failing paths
  (used once to generate the allow-list).
- A listed path that does not exist is skipped. POSIX `sh`, shellcheck-clean.

`scripts/check-filesize.sh [--list]`
- Input: tracked non-test `.go` files (`git ls-files '*.go' ':!:*_test.go'`),
  minus `check-filesize.allow`. Violation: more than 600 lines (`wc -l`).
- Output: `path: N lines (max 600)`; exit 1 if any. `--list` as above.

`make lint`
- If `golangci-lint` is on PATH: `golangci-lint run ./...`.
- If not, and `RELEVO_REQUIRE_LINT=1`: print `golangci-lint is required
  (RELEVO_REQUIRE_LINT=1) but not installed` and exit 1.
- If not, otherwise: print `golangci-lint not installed; skipping lint` and
  continue (the same pattern the Makefile uses for shellcheck).

`make check` gains, after `go vet`: `$(MAKE) lint`, `sh scripts/check-comments.sh`,
`sh scripts/check-filesize.sh`. The existing loop over `scripts/*_test.sh`
already picks up the two new test scripts.

CI (`.github/workflows/ci.yml`, job `check`): add a step before `make check`,
only when `matrix.os == 'ubuntu-latest' && matrix.go == 'stable'`, that installs
`github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0` with `go install`
and adds `$(go env GOPATH)/bin` to `GITHUB_PATH`; set `RELEVO_REQUIRE_LINT: '1'`
in that leg's environment only. Other legs run `make check` with lint skipped.
Use whatever expression style the file already uses for matrix conditions.

## 5. Pseudocode

```
generate exclusions: for dir in go list dirs: emit rule(path regexp for dir only)
golangci-lint run ./...        -> must exit 0 with every package excluded
sh check-comments.sh --list > check-comments.allow ; sh check-comments.sh -> exit 0
sh check-filesize.sh --list > check-filesize.allow ; sh check-filesize.sh -> exit 0
test scripts: fixture dir with (clean.go, cites-issue.go, cites-section.go, in-string.go)
              assert exit codes and output lines; allow-list with a missing path is fine
```

## 6. Error handling

- If `golangci-lint run ./...` reports anything with every package excluded
  (for example a config error or a typecheck failure), stop and report the
  output. Do not add further exclusions beyond the per-package rules.
- If golangci-lint v2.14.0 cannot be installed with the CI leg's Go version,
  pin the newest v2 release that can, and say which in the report.

## 7. CLAUDE.md addition

Add a `### Code style` subsection at the end of `## Conventions`, 10-16 lines:
- The comment rules, verbatim in substance: a package comment of 1-3 lines; a
  comment says *why*, only where the code cannot; no issue or PR numbers, no
  spec sections, no "round N", "used to", "pre-#NNN" (git and the issues hold
  history); no restating the code; doc comments on exported names only when they
  add something; tests follow the same rules and a test's name says what it pins.
- The code rules: the dexpace Go styleguide applies, except no "two assertions
  per function" rule and no mandatory exported doc comments; functions at most 70
  lines; non-test files at most 600 lines; one package per concept.
- Enforcement: `make check` runs golangci-lint (`.golangci.yml`) and the two
  scripts; packages and files not yet cleaned are listed as exclusions, and a
  round that finishes a package removes its entries. Never add a new exclusion
  to get a round green.

## 8. Working efficiently

- Read `Makefile`, `.github/workflows/ci.yml`, `CLAUDE.md` and one existing
  `scripts/*_test.sh` (for the test-script pattern) in one batch.
- One edit per file. Generate both allow-lists and the exclusion list by
  running the scripts / `go list` once, not by hand.
- Focused loop: `golangci-lint run ./...`, `sh scripts/check-comments.sh`,
  `sh scripts/check-filesize.sh`, `sh scripts/check-comments_test.sh`,
  `sh scripts/check-filesize_test.sh`, `shellcheck scripts/*.sh`.
- Full check once at the end: `make check`.

## 9. Ordered steps

1. `.golangci.yml` with exclusions for every package. Done when
   `golangci-lint run ./...` exits 0 **and** deleting any one exclusion rule
   (try `internal/jsonshape`) makes it report issues or still pass cleanly --
   record which in the report, for the phase 3 planner.
2. `check-comments.sh` + allow-list + test script. Done when all three pass.
3. `check-filesize.sh` + allow-list + test script. Same.
4. Makefile `lint` target and `check` wiring. Done when `make check` passes
   with and without golangci-lint on PATH (`PATH=` trick for the second), and
   `RELEVO_REQUIRE_LINT=1 make lint` fails without it.
5. CI step and env (ubuntu-latest / stable only).
6. CLAUDE.md `### Code style`.
7. `git add` the spec. `make check` passes; `git diff --stat` shows only §2 files.
8. **Save the plan.** Copy this plan to `docs/plans/2026-09-26-cleanup-p0c-tooling.md`
   and commit it with the rest.

Report: the exclusion count, both allow-list sizes, the golangci-lint version
pinned in CI, and the step 1 finding for `internal/jsonshape`.
