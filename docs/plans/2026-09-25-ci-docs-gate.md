# CI docs gate: skip the heavy jobs on docs-only PRs

## 1. System Overview

Every PR today runs `check` (3 jobs on PRs since #455) and `cross-compile`,
even when it only touches planning documents. This round adds a cheap
`changes` gate job to `.github/workflows/ci.yml`. On a `pull_request` whose
changed files are all under `docs/plans/`, `docs/specs/` or
`docs/superpowers/`, the gate outputs `code=false`, and `check` and
`cross-compile` are **skipped**. GitHub counts skipped as passing, so
`gh pr checks <n> --watch` still finishes green and the merge routine in
CLAUDE.md is unchanged.

These three prefixes are the only docs-only paths. They are exactly the paths
`scripts/check-name.sh` already excludes, and nothing in the build reads them.
`README.md`, `docs/design.md`, `internal/harness/agents/*.md` and all other
`.md` files are code for this purpose: they are scanned by `check-name.sh` or
embedded in the binary.

The classification rule lives in a small POSIX `sh` script so that
`make check` tests it (every `scripts/*_test.sh` runs there, and shellcheck
lints `scripts/*.sh`).

## 2. File Structure

```
scripts/ci-code-changed.sh         NEW       reads changed paths on stdin, prints `true` or `false`
scripts/ci-code-changed_test.sh    NEW       table tests for the script (run by make check)
.github/workflows/ci.yml           MODIFIED  new `changes` job; `check` and `portability` gated on it
docs/plans/2026-09-25-ci-docs-gate.md  NEW   this plan, committed as the last step
```

Nothing else changes: not `release.yml`, not the Makefile, not `check-name.sh`.

## 3. Data Structures & Type Definitions

**Script I/O contract, `scripts/ci-code-changed.sh`:**
- stdin: zero or more repo-relative paths, one per line (the output of
  `git diff --name-only`). Blank lines are ignored.
- stdout: exactly one line, `true` or `false`.
- exit status: 0 whenever it printed an answer. No arguments, no env.
- Docs-only prefixes, a fixed list in the script: `docs/plans/`,
  `docs/specs/`, `docs/superpowers/`. Matching is by leading prefix only.
  `docs/plans` without the trailing slash, or `x/docs/plans/y`, does NOT match.

**Workflow output:** job `changes` exposes `outputs.code`, the string `true`
or `false`.

## 4. Interface Definitions & Component Contracts

**`ci-code-changed.sh` rule:**
- Prints `false` iff stdin contains at least one non-blank path AND every
  non-blank path starts with one of the three prefixes.
- Prints `true` in every other case, including empty input. Fail open: if the
  gate can't prove a PR is docs-only, the PR gets full CI.

**Job `changes`** (`runs-on: ubuntu-latest`, name `changes`):
- On `push`: output `code=true` without diffing (main always gets full CI).
- On `pull_request`: checkout with `fetch-depth: 0`, then
  `git diff --name-only "$BASE...$HEAD"` piped into the script, where `BASE` and
  `HEAD` come from `github.event.pull_request.base.sha` and
  `github.event.pull_request.head.sha`, passed through `env:`. Never
  interpolate `${{ }}` directly into the `run:` text. If `git diff` fails,
  output `code=true` (fail open), and don't let the step fail.
- Writes `code=<value>` to `$GITHUB_OUTPUT`, and echoes the value plus the
  changed-path count to the log for debugging.

**Jobs `check` and `portability`:** both gain `needs: changes` and
`if: needs.changes.outputs.code == 'true'`. Nothing else in them changes:
not the matrix, not the `exclude` from #455, not the steps.

## 5. High-Level Pseudocode

```
changes:
  if event == push:           code = true
  else:
    paths = git diff --name-only BASE...HEAD   (on failure: code = true, stop)
    code  = ci-code-changed.sh < paths
  emit code

check, portability:
  needs changes
  run only if code == 'true'   (otherwise GitHub reports them skipped)

ci-code-changed.sh:
  saw_any = no
  for each non-blank line:
    saw_any = yes
    if line does not start with docs/plans/ | docs/specs/ | docs/superpowers/:
      print true; exit 0
  print (saw_any ? false : true)
```

The test script follows `scripts/check-name_test.sh`: `#!/bin/sh`,
`set -eu`, locate `here` via `CDPATH= cd`. It needs no git repo, since the
script is a pure stdin filter. Cases, each asserting the exact stdout:

1. empty input → `true`
2. only blank lines → `true`
3. `docs/plans/a.md` → `false`
4. `docs/plans/a.md`, `docs/specs/b.md`, `docs/superpowers/plans/c.md` → `false`
5. `docs/plans/a.md`, `cmd/relevo/main.go` → `true`
6. `README.md` → `true`
7. `docs/design.md` → `true`
8. `internal/harness/agents/architect.claude.md` → `true`
9. `.github/workflows/ci.yml` → `true`
10. `x/docs/plans/a.md` → `true` (prefix match only)
11. `docs/plansx/a.md` → `true` (the trailing slash is part of the prefix)

## 6. Error Handling Strategy

- The script never fails closed. Any path it doesn't recognise means `true`.
- In the `changes` job, a failed `git diff` means `code=true` and a green step.
  An unexpected shell error may fail the job. If it does, `check` and
  `portability` are skipped and the whole run shows red on `changes`. That's
  acceptable: a red gate is visible and blocks the merge routine.
- If `actionlint` or `shellcheck` rejects something, fix it within this
  design. If the design itself can't pass them, halt and report the exact
  output.

## 7. Working Efficiently

Each model step costs a round trip, so:
- Create the two scripts and edit `ci.yml` in one step: parallel Write calls
  for the scripts, and one edit (or one Write) for `ci.yml`. Read `ci.yml` and
  `scripts/check-name_test.sh` once, together, first.
- Focused check:
  `sh scripts/ci-code-changed_test.sh && shellcheck scripts/ci-code-changed*.sh && actionlint .github/workflows/ci.yml`.
  Fix everything it reports before rerunning.
- Full check, once, at the end: `make check`. If this machine blocks heavy
  commands, use `dev run make check`. If `dev run` fails because `dist/` or
  `.git` is missing on the mirror, report that and rely on the PR's CI.

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan to fit.

## 8. Ordered Implementation Steps

**Step 0: sync.** `git fetch origin && git merge --ff-only origin/main`. HEAD
must include `828f6aad` (#455), and `ci.yml` must have a top-level
`concurrency:` block and no `race detector` step. If not, halt.

**Step 1: script and test.** Create `scripts/ci-code-changed.sh` (executable,
`#!/bin/sh`, POSIX only, a header comment saying what it does and naming the
prefix list) and `scripts/ci-code-changed_test.sh` with the 11 cases.
Verification: the test passes and shellcheck is clean on both.

**Step 2: gate the workflow.** Edit `.github/workflows/ci.yml` as in
section 4. Put the `changes` job first under `jobs:`, with a two-line comment
explaining the gate and pointing at the script. Verification:
`actionlint .github/workflows/ci.yml` exits 0, and `git diff --stat` shows only
the three files from section 2.

**Step 3: full check.** Run `make check` (see section 7). It must pass.

**Step 4: ship the PR.** Copy this plan to
`docs/plans/2026-09-25-ci-docs-gate.md` if it isn't there already. Commit
everything as one commit,
`ci: skip check and cross-compile on PRs that only touch docs/plans, specs or superpowers`,
push `relevo/<this branch>`, and open a PR against `main`. Verification: on
that PR's run, `changes` logs `code=true` and `check` (3 jobs) and
`cross-compile` start. Don't wait for them to finish.

**Step 5: prove the skip path with a throwaway probe PR.**
- From your branch, create branch `<your branch>-probe`, add one file
  `docs/plans/zz-ci-gate-probe.md` (one line of text), commit, push, and open
  a **draft** PR with its **base set to your branch** (not `main`). Its diff
  is then only that file, and its workflow file comes from your branch.
- Wait for that run to complete. Verification:
  `gh pr checks <probe-pr>` shows `changes` passed and `check`/`cross-compile`
  skipped, and `gh pr checks <probe-pr>` exits 0.
- Close the probe PR with `gh pr close <probe-pr> --delete-branch`. Make sure
  the probe file is NOT on your main branch or in the main PR.

The report states the main PR number, the `changes` log line from step 4,
the probe PR's `gh pr checks` output and exit status from step 5, and the
`git diff --stat` of the main PR. Don't merge anything.
