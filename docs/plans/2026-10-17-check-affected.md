# Plan: `make check-affected`, a light local gate

One builder round, one PR, new commits only. Last builder step saves this plan
to `docs/plans/2026-10-17-check-affected.md`.

## Behaviour

`make check-affected` = `check-static`, then `go test` (no `-race`, no
`-count=1`, so Go's test cache applies, like `check-quick`) over only the
packages a change reaches. The package list comes from a new script,
`scripts/affected-packages.sh`.

### Script contract

- Usage: `affected-packages.sh [--base REF]`. `BASE` in the environment is the
  same override; the flag wins. Exit 2 on bad usage.
- Stdout is the answer, one line each:
  - one import path per affected package, or
  - the single line `./...` for "all", or
  - nothing for "no package affected".
- Stderr carries a one-line reason when it falls back to all.
- The default base is `git merge-base HEAD origin/main`. If `origin/main` or
  the merge-base is missing, or `go list` fails, print `./...` (fail open) and
  say why on stderr.
- Changed set = `git diff --name-only --no-renames <base>` (working tree
  against base, so staged and unstaged) plus
  `git ls-files --others --exclude-standard`. Use `git -c core.quotepath=off`.
  Run both from the toplevel (`cd "$(git rev-parse --show-toplevel)"`) so
  paths are repo-relative. `--no-renames` lists both ends of a rename.
- Without `go.mod` at the toplevel, print `./...`.

### Mapping a changed path (first match wins)

1. `go.mod`, `go.sum`, `Makefile`, `.golangci.yml`: all.
2. Docs-only paths affect nothing: `docs/**`, root `*.md`, `LICENSE`.
   Nothing else is exempt. `cmd/relevo/release_test.go` reads
   `.github/workflows/release.yml`, so `.github/`, `claude-plugin/`,
   `scripts/` and the rest map to all.
3. Otherwise take the path's directory and walk up:
   - the directory is a package directory of the module: that package is
     seeded (covers `testdata/` and embedded files under it);
   - the directory exists but holds `*.go` files and is not a listed package
     (all files build-tag excluded): all;
   - go up one level and repeat;
   - reaching `.` with no package: all.
4. A path whose directory no longer exists on disk (deleted file or whole
   package) is all. A deleted file in a surviving directory maps normally.

### Reverse closure

- Get the package graph from one
  `go list -e -f '{{.ImportPath}}<TAB>{{join .Imports ","}},{{join .TestImports ","}},{{join .XTestImports ","}}' ./...`.
  Use a literal tab via `{{"\t"}}`.
- Take the module path from `go list -m`. Compute a package's relative dir as
  its import path with `modpath/` stripped (root = `.`). Do not use `.Dir`:
  macOS `/tmp` is a symlink and `.Dir` can disagree with the git toplevel.
- Keep only edges between module packages. `.Imports` includes the main
  package's non-test deps, and Test/XTest imports are what make a package whose
  tests import a changed package affected.
- Compute the transitive reverse closure of the seeded set, seeds included, with
  an awk fixpoint or BFS over "importer of X" edges. Sort with `LC_ALL=C` for
  deterministic output.
- A seeded package that `go list ./...` cannot see is covered by rule 3's
  `*.go` clause.

## Why sh + awk, not a Go tool

`go list` already supplies the graph, including test imports. What is left is
path-to-directory walking and a ~25-line reverse-closure loop. That is robust in
POSIX sh + awk. It follows the existing `scripts/*.sh` pattern
(`test-shard.sh`, `ci-code-changed.sh`), is covered by `check-scripts` and
shellcheck, and needs no build step. A Go tool would add a package, a
coverage-baseline entry and a `go build` step to the gate for no gain.
Portability limits:
- POSIX awk only: no gawk extensions, no `asort`, no `length(array)`; the
  macOS awk is BSD one-true-awk. The dev machine's awk is GNU 5.4, so avoid
  anything not in POSIX.
- No GNU-only options for `sort`, `sed`, `mktemp` or `git`.
- No `xargs -r`.
- No `read -d`.

Keep every function at most 70 lines, split as: base resolution, changed-path
listing, path mapping, closure. Comments say why only; no issue numbers or
history.

## Seams (files)

1. **New `scripts/affected-packages.sh`**: the contract above. Model the header
   comment on `scripts/test-shard.sh` (usage, env, exit codes). Use
   `set -eu`, `CDPATH=` scoping with the same shellcheck disables as the
   sibling scripts.
2. **New `scripts/affected-packages_test.sh`** (auto-run by `check-scripts`'s
   `scripts/*_test.sh` loop; shellcheck covers it via `scripts/*.sh`).
   Fixture built in `mktemp -d`, trap-cleaned, no network:
   - `git init`; set `GIT_AUTHOR_*`/`GIT_COMMITTER_*` via env; a
     `go.mod` with module `example.test/fx` and `go 1.22`; `GOTOOLCHAIN=local`,
     `GOFLAGS=` and `GOPROXY=off` exported so nothing is fetched.
   - Seed the remote ref with
     `git update-ref refs/remotes/origin/main HEAD` after the first commit.
   - Packages: `a` (leaf), `b` imports `a`, `c` imports `b` (transitive),
     `d` whose in-package `_test.go` imports `a` (TestImports),
     `e` with an external `package e_test` importing `a` (XTestImports),
     `f` independent, `a/testdata/x.txt` and an embedded file in `f`.
   - Each case runs the script from inside the fixture and compares stdout.
   - Cases to pin (names say what they pin):
     - edit in `a` gives `a b c d e`;
     - edit in `f` gives only `f`;
     - testdata file under `a` gives the same as `a`;
     - docs-only change (`docs/x.md`, root `README.md`) gives empty output;
     - `go.mod` / `Makefile` / `.golangci.yml` / `.github/x.yml` / unknown top
       dir gives `./...`;
     - untracked new `.go` file in `f` is included;
     - staged-only and unstaged-only edits are both included;
     - a base override (flag and `BASE=`) restricts the diff to changes since
       that ref: commit an edit to `a`, then base = `HEAD` gives empty;
     - missing `origin/main` gives `./...` plus a stderr note;
     - a deleted whole package directory gives `./...`;
     - a file renamed out of `a` maps both ends;
     - a path with a space in its name maps;
     - bad flag gives exit 2.
   - Skip all cases with a message if `go` or `git` is absent, as sibling tests
     do for missing tools. The suite already needs both, so this is a
     courtesy only.
3. **`Makefile`**:
   - Add `check-affected` to `.PHONY`.
   - Add a comment block next to `check-quick` saying why: the full gate is
     CI's job (race, every package, coverage); this one is the builder's
     inner gate. `BASE` overrides the base.
   - Recipe:
     - `$(MAKE) check-static`;
     - then a shell recipe that runs
       `pkgs=$$(sh scripts/affected-packages.sh)`, `exit`s on script failure,
       prints "no Go package affected; tests skipped" when empty, else
       `go test $$pkgs`;
     - forward `BASE` to the script by exporting it only if set, e.g.
       `BASE="$(BASE)"` with the script treating empty as unset.
   - `check` and `check-quick` stay unchanged.
4. **Docs**, the following changes only, kept short:
   - `CLAUDE.md` "Verifying a builder's work": builder rounds are gated by
     `make check-affected`; PR CI is the full judge (`-race`, every package,
     coverage); `make check` stays for a full local run. Keep the sentence
     about comparing `git diff --stat` and `make e2e`. Also fix the "stricter
     than `go test ./...`" wording so it stays true for both targets.
   - `docs/runbook.md`:
     - "Verifying a branch": add `make check-affected` as the default
       local gate; keep the zen `make check-test` / modernc / `make e2e` recipe
       as the full local run, labelled optional; CI is the judge.
     - "Running chains": add that `relevo chain --gate 'make check-affected'`
       (and per-binding `--gate`) is the gate for chain rounds.
     - The last bullet "End plans for builders that skip gofmt with ...
       `make check`, commit, then report" becomes `make check-affected`.
   - `CONTRIBUTING.md` "Getting set up": say `make check-affected` is the day to
     day gate and `make check` the full run CI repeats on both OSes; fix line
     34-37 accordingly.
   - `.github/pull_request_template.md`: the checkbox allows
     `make check-affected` (or `make check`).
   - `README.md` lines ~75 and ~2847: one added sentence pointing at
     `make check-affected`. Leave the config examples at ~1749 and ~2209-2229
     alone: they show a configured gate string, not a recommendation.
   - Do not touch `.github/workflows/ci.yml` or `scripts/test-shard.sh`.
5. **No agent or guide change needed.** Checked: neither
   `internal/mastermind/guide.md` nor any `internal/harness/agents/*` file
   names `make check` as the builder gate (the agents only say "the plan's
   check line"). So `scripts/agents-shipped.sh --write` is not needed; the
   builder confirms with `git diff --stat` that nothing under
   `internal/harness/agents/` changed. If one did change by accident, run
   `sh scripts/agents-shipped.sh --write`.
6. **Out of scope, reported:** the configured gate default is data in
   `relevo.db` (`relevo config`, `gate.default`) and the Go tests use
   `"make check"` only as sample strings. The PR does not change them. The
   report tells the owner to set `gate.default` / per-repo gate to
   `make check-affected` after merge.

## Ordered steps

1. Write `scripts/affected-packages.sh`. Check:
   `shellcheck scripts/affected-packages.sh` clean; running it in the repo with
   no changes prints nothing; with `BASE=HEAD~3` prints sensible packages.
2. Write `scripts/affected-packages_test.sh`. Check:
   `sh scripts/affected-packages_test.sh` passes. Then mutate: make the
   closure skip `TestImports` (or `XTestImports`) and confirm the matching case
   fails; make deletion map to the parent package and confirm the deleted
   package case fails; restore.
3. Edit the `Makefile`. Check: `make -n check-affected` shows the intended
   recipe; `make check-affected BASE=HEAD` on a clean tree runs `check-static`
   and skips tests.
4. Update the docs (item 4). Check: `sh scripts/check-name.sh` passes (it scans
   tracked Markdown too) and `git diff --stat` touches only the declared files.
5. Run the focused commands first, then the gate on the builder's own change:
   - `sh scripts/affected-packages_test.sh`;
   - `make check-scripts`;
   - `make check-static`;
   - `make check-affected`: this PR touches no Go package, so it should run
     static plus "no Go package affected" or only fall back to `./...` if a
     file lands in an unmapped path. Report which, and the printed package
     list. If it falls back to all, do NOT run the full `-race` suite; report it.
6. Save this plan to `docs/plans/2026-10-17-check-affected.md`, run
   `make check-static` once more, and report.

## Constraints and cautions

- Do not run `make check`, `make check-test` or any `go test -race ./...`
  locally.
- CLAUDE.md note for the one CLI-adjacent concern: no `cmd/relevo` test is
  added, so no harness or network is touched. The fixture test uses a throwaway
  git repo and a no-dependency Go module only.
- Do not add exclusions to `.golangci.yml`, `check-comments.allow` or
  `check-filesize.allow`. No Go files change, so no coverage baseline changes.
- Compatibility: the closure and path code must run under macOS `sh`/BSD awk;
  there is no `sed -i`, no `\+` in `sed`, and no `readlink -f`.
- Make new commits only; nothing is amended or rebased.

## Report must include

- Files changed, with the `git diff --stat` compared against this scope.
- The output of `sh scripts/affected-packages_test.sh`, `make check-scripts`,
  `make check-static` and `make check-affected` (including which packages it
  chose).
- Which mutations were tried and the named test case that failed each.
- Confirmation that `internal/harness/agents/` and `guide.md` needed no change.
- The owner follow-up: set the configured gate default to
  `make check-affected`, and note that the CI shard/ci.yml round is separate.
- Anything that fell back to "all" unexpectedly while running on this repo
  (e.g. an unmapped top-level directory), as a candidate for the exempt list.