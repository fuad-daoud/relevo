# govulncheck as its own CI job

One new file, `.github/workflows/govulncheck.yml`. Nothing else is touched. The
job runs `govulncheck ./...` (default source/symbol scan) when:

- **weekly** on `schedule` (Monday 05:23 UTC),
- on **pull requests whose diff touches `go.mod` or `go.sum`** (`paths` filter),
- on **manual `workflow_dispatch`**.

It has network for exactly two things: the pinned scanner install (module proxy
+ checksum DB) and the vuln-DB fetch from vuln.go.dev at scan time — which is
why it cannot live in `make check`. Findings make the job red (govulncheck exits
3; a tool/DB failure exits 1); because the job is not required and PRs without a
`go.mod`/`go.sum` change never run it, a red run informs rather than blocks.

## Where the job lives: its own file — and why

In `ci.yml`, the `changes` gate (`.github/workflows/ci.yml:20-57`) special-cases
only `push` (`:37-41`) and otherwise diffs `BASE...HEAD` (`:43-47`); its
checkout is PR-only (`:26-29`). On a `schedule` event BASE/HEAD are empty, the
diff fails in a workspace that was never checked out, and the gate answers
`code=true` — so every gated job (lint `:62`, test `:123`, test-macos `:189`,
coverage `:249`, owner-mode `:286`, portability `:303`) would run weekly. Any
fix inside `ci.yml` (special-casing `schedule` in the gate, or `if:` guards on
six jobs) buys nothing: the govulncheck job shares no inputs/outputs with
`ci.yml`'s jobs, and its PR `paths` filter is a per-workflow property anyway. A
separate file makes the interaction impossible instead of handled; `ci.yml`
keeps `push`/`pull_request` only.

## 1. The exact YAML (the bytes are the decision)

`.github/workflows/govulncheck.yml` — new, ~46 lines, validated with
`actionlint` (exit 0) on this host:

```yaml
name: govulncheck

# Its own workflow, not a job in ci.yml: ci.yml's `changes` gate answers
# code=true on every non-PR event (its `git diff` has no BASE/HEAD and its
# checkout is PR-only), so a schedule there would run the whole suite weekly.
# This job also reaches vuln.go.dev, which `make check` must not: a new
# advisory must not redden unrelated PRs. Nothing here is a required check.

on:
  schedule:
    # Weekly, Monday 05:23 UTC; off the :00 queue spike.
    - cron: '23 5 * * 1'
  pull_request:
    paths: ['go.mod', 'go.sum']
  workflow_dispatch:

permissions:
  contents: read

jobs:
  govulncheck:
    name: govulncheck
    runs-on: ubuntu-latest
    timeout-minutes: 15
    env:
      GOVULNCHECK_VERSION: v1.8.0
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          # stable, not go-version-file: golang.org/x/vuln v1.8.0 needs Go
          # 1.26.0, and stable also reports advisories for what CI ships.
          go-version: stable

      # golang/vuln publishes no release binaries (v1.8.0 has no release and
      # even the older releases carry no assets), so the pinned install is
      # `go install`; the module checksum database, not a curl's sha256sum,
      # is what verifies the bytes.
      - name: install govulncheck
        run: |
          go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"

      - name: run govulncheck
        run: |
          govulncheck -version
          govulncheck ./...
```

Decision notes: `workflow_dispatch:` is added (post-merge proof without waiting
a week; on-demand re-scan after a DB refresh; no inputs, no privilege beyond
`contents: read`). `permissions: contents: read` sits at workflow level, same
shape as `ci.yml:8-9`. `timeout-minutes: 15` bounds a hung DB fetch (the
360-minute default is a bad failure mode); it is strippable with nothing
depending on it. `GOVULNCHECK_VERSION` as job env mirrors
`GOLANGCI_LINT_VERSION` (`ci.yml:68`). No `concurrency` block — a weekly run and
a PR run can overlap harmlessly. `govulncheck -version` warms the DB cache and
writes the Go/Scanner/DB date into the log, which is the run's
self-documentation; `check-name.sh` greps tracked files, so the comments above
carry no relay-name tokens and no issue numbers.

## 2. Pinned version and install — prebuilt archives do not exist

Checked 2026-09-30 from this worktree:

- `gh api repos/golang/vuln/tags` → newest tag `v1.8.0`; `gh api
  repos/golang/vuln/releases/tags/v1.1.4` → `assets: []`, and v1.8.0 has no
  release object at all. **golang/vuln publishes no binaries**, so the
  golangci-lint "prebuilt archive + checksums.txt" pattern has nothing to
  download.
- `golang/govulncheck-action` (last tag `v1.1.0`) exists, but its `action.yml`
  installs `govulncheck@latest` — no input pins the tool. **Rejected**: the pin
  is the point.
- Therefore `go install golang.org/x/vuln/cmd/govulncheck@v1.8.0` in a setup
  step, integrity via the module checksum database
  (proxy.golang.org/sum.golang.org), which is the same guarantee a `sha256sum
  --check` gives without a tarball. Evidence on this host: `go run
  golang.org/x/vuln/cmd/govulncheck@v1.8.0 -version` → `Scanner:
  govulncheck@v1.8.0`, `DB updated: 2026-09-28`, and `go.mod`/`go.sum` stayed
  untouched.
- `go-version: stable` rather than `go-version-file` because x/vuln v1.8.0's
  `GoVersion` is 1.26.0 while `go.mod` says 1.25.0 — stable avoids a silent
  GOTOOLCHAIN switch in the install step and matches the lint/coverage jobs.

## 3. What is verifiable locally vs only in CI

**Local, offline** (focused command: `actionlint
.github/workflows/govulncheck.yml`; full gate: `make check`):

- `actionlint` — installed on this host (not a repo dependency; it lints the
  exact file above: event/cron/`paths` schema, expressions, and the `run:`
  scripts via shellcheck): exit 0.
- YAML key check: `yq 'keys' .github/workflows/govulncheck.yml` prints `on`;
  note PyYAML and Ruby/Psych resolve a bare `on` to boolean `true` (YAML 1.1),
  so those two are not the right assertion.
- `git add .github/workflows/govulncheck.yml && sh scripts/check-name.sh` →
  `check-name: ok` (the guard reads tracked files only, so the file must be
  staged first).
- `make check` (offline) → green; nothing in it reads workflows, so this is a
  no-regression check.
- Optional network probe: `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0
  -version` proves the pin resolves for a fresh runner.

**Only a CI run can confirm**: GitHub's own parser/registration (`gh workflow
view govulncheck.yml`), that the runner reaches proxy.golang.org/sum.golang.org/
vuln.go.dev, that the `paths` filter fires exactly on `go.mod`/`go.sum` PRs and
never on docs-only PRs, and that the cron fires weekly.

## 4. Ordered steps

1. Branch off `372f0de…`; `git rev-parse HEAD` matches, tree clean — branch
   exists at the base commit.
2. Create `.github/workflows/govulncheck.yml` with the bytes in §1 — file
   present; `actionlint .github/workflows/govulncheck.yml` exits 0.
3. `git add` the file, then `sh scripts/check-name.sh` — prints `check-name:
   ok`.
4. `make check` — every target green (no Go/shell touched; proves no
   regression).
5. Optional, network: `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0
   -version` — prints `Scanner: govulncheck@v1.8.0`; `git status` shows no
   `go.mod`/`go.sum` change.
6. Commit the workflow file and this plan doc. Commit diff is the workflow file
   plus `docs/plans/2026-09-30-govulncheck-ci.md`, nothing else.
7. Push and open the PR; `gh pr checks --watch` until every job passes. Note:
   **this PR will not run govulncheck itself** (its `paths` filter matches only
   `go.mod`/`go.sum`), so absence of a govulncheck check on the PR is correct,
   not a failure.
8. After merge: `gh workflow list` shows `govulncheck`; `gh workflow run
   govulncheck.yml`; `gh run watch`; the run log shows `Scanner:
   govulncheck@v1.8.0`, the DB date, and a clean exit 0. `gh run list
   --workflow=govulncheck.yml` for the record.
9. Confirm the PR trigger on the next real PR touching `go.mod`/`go.sum` (or a
   throwaway draft PR that bumps a comment in `go.mod`); confirm the negative
   with any docs-only PR — no govulncheck check-run appears.
10. Confirm the scheduled run after the next Monday 05:23 UTC (`gh run list
    --workflow=govulncheck.yml --event schedule`; queueing adds minutes).

## 5. Risks, touched files, open decisions

**Touched files**: `.github/workflows/govulncheck.yml` (NEW — the whole
change); `docs/plans/2026-09-30-govulncheck-ci.md` (NEW). Nothing modified.

**Risks**:

1. The pin ages; bump `GOVULNCHECK_VERSION` deliberately. There is no
   `dependabot.yml`, and none would see an env-var pin. Staleness costs scanner
   fixes, not false reds.
2. x/vuln v1.8.0 needs Go ≥ 1.26.0; `stable` provides it. If someone later
   switches the setup step to `go-version-file: go.mod` (1.25.0), the install
   silently downloads a newer toolchain instead of failing.
3. Exit-3-on-findings is by design, but the job must stay non-required: if it
   is ever added to branch protection, unrelated PRs will hang on "Expected —
   waiting for status".
4. The DB fetch is a live network dependency on a weekly timer; a transient
   failure is a red rerun, bounded by `timeout-minutes: 15`.
5. Cold-cache runs compile the scanner (~1 min); setup-go's module cache makes
   later runs cheap. Fallback if it ever regresses: an explicit GOMODCACHE
   cache keyed on the pin.
6. The PR that adds the workflow does not exercise it (paths filter); a bad
   YAML is caught by local `actionlint`, not by GitHub, until after merge.

**Open decisions**: (a) plan doc committed — yes, precedent
`docs/plans/2026-09-25-ci-docs-gate.md`; (b) cron slot kept at `23 5 * * 1`
(Monday 05:23 UTC); (c) the workflow file is **not** added to its own `paths`
(trigger set kept exactly as decided); (d) SARIF upload / code-scanning alerts
as a later follow-up, not now.

## Deleted behaviour

1. **Nothing.** The work is purely additive: no job, Makefile target, script,
   test, or file is removed or altered, and `make check`'s offline contract and
   `ci.yml`'s event set are untouched.
