# MasterMind runbook: verify, merge, deploy

Maintainer runbook for relevo's own development; it names the maintainer's
machines and is not a guide to using relevo. How a MasterMind verifies a
builder's branch, merges it, and deploys relevo.
`CLAUDE.md` states the rules; this page states the steps. Prefer the `make`
target over a hand-rolled command when both appear.

## Temp space

Export this before test builds, send bundles, or anything touching turso temp
files:

```sh
export TMPDIR=$HOME/.cache/relevo-chains/tmp GOTMPDIR=$HOME/.cache/go-tmp
```

`/tmp` is a usrquota tmpfs on the laptop and on zen, and parallel builds fill
it. `relevo-serve` on zen carries a systemd drop-in that sets TMPDIR/GOTMPDIR
under `~/.cache`; keep it on any rebuild.

The state tmp root is now the default and needs no drop-in: every round and
check process gets its own directory under `<state root>/tmp`, and the daemon
sweeps the leftovers there on its ticks. Where such a drop-in is no longer
needed, remove it by hand -- zen's
`relevo-serve.service.d/tmpdir.conf` is the example -- because an inherited
`TMPDIR` keeps pointing processes at a directory the sweep never sees.
The `relevo-serve-tmp` and `relevo-serve-gotmp` directories live outside the
root and are never swept; clear them on the same schedule you always used.
This is an ops step, not code.

## Verifying a branch

- Static checks run locally: `make check-static` (gofmt over tracked and
  untracked Go files, `go vet`, golangci-lint, the comment/size/name guards,
  and a `go mod tidy` check).
- The default local gate is `make check-affected`: `make check-static`, then
  `go test -cover` over only the packages the change reaches, then
  `scripts/check-coverage-delta.sh`. That script tests each package whose own
  files changed once more at the base commit, on the same machine, and fails
  a drop of more than one point. It costs one extra test run of those packages,
  and it is the only coverage check off CI: the baseline file is CI's. CI is
  the judge.
- The full local run is optional -- the static checks, then `make check-test`,
  the modernc leg, and `make e2e`. The suite runs on zen, not on contabo:
  contabo (7G) OOM-kills it, and its `dev run` output vanishes silently. `git
  archive` the tree to
  `zen:~/.cache/mm-*`, `git init` a snapshot there, then `make check-test`,
  the modernc leg, and `make e2e`. The modernc leg is:
  `go vet -tags modernc ./internal/db/...` and
  `go test -race -count=1 -tags modernc ./internal/db/... ./internal/store/...`.
  CI runs that leg without `-race` on pull requests, so the command above is
  the stricter one.
- Mutation-test one of your own per PR: break the condition the change turns
  on and confirm a named test fails.
- `TestHelpJSONDocumentsTheSurface` is not selected by `-run Contract`.
  Regenerate its golden with
  `go test ./cmd/relevo -run TestHelpJSONDocumentsTheSurface -update`.
- For a TUI change, capture the real screen on live data with the
  capturing-tui-screens skill. Goldens can pass while the live list is wrong.
- Builders do not commit unless told to, and a close with uncommitted work
  halts instead of advancing. Check `git status` in the served worktree before
  trusting "done".

## Merging

- Before merging main into a branch, check
  `git ls-tree origin/main internal/db/migrations/`. Expect semantic
  conflicts where main changed a signature the branch also touched. Resolve a
  `go.sum` conflict with `LC_ALL=C sort`.
- Main moves constantly because several sessions merge. Loop
  `gh pr update-branch`, then `gh pr checks <n> --watch` until every job
  passes, then `gh pr merge --match-head-commit`.
- A test that fails on main with no code cause is a flake: add it to a suite
  in `.github/workflows/flake-hunt.yml`, which runs each suite nightly with
  `-race -count=20` on ubuntu and macOS (`gh workflow run flake-hunt -f
  count=50` on demand), and fix the test rather than rerunning it green.

## Installing and deploying

- Only a tagged release is ever installed on the laptop, zen or contabo. A
  merge to main is not a deploy; the next release carries it. A branch, a main
  head or any other untagged build runs only in a sandbox.
- Testing a branch or main: `scripts/relevo-sandbox.sh` points the `XDG_*`
  and harness homes at a directory, needs no root, and
  [env-sandbox.md](env-sandbox.md) has the recipe. Separate Unix users are the
  stronger flow for UID-separation tests; create, list and destroy those with
  `scripts/relevo-dev-user.sh`, and see [dev-sandbox.md](dev-sandbox.md).
- Laptop: `relevo update --to vX.Y.Z` installs the checksum-verified release
  binary atomically; `--release` is needed once to replace a local build.
  `relevo version` must print the bare tag afterwards.
- zen: run the same `relevo update` over ssh (its shell is fish), then
  `systemctl --user restart relevo-serve`. Serve reads config only at start, so
  a config change alone never lands without the restart.
- contabo: download the tag's linux archive from the GitHub release, verify it
  against `checksums.txt`, and deploy that binary with
  `~/projects/servers/contabo/srv.fish deploy relevo-serve <binary>`.
- Deploy only when no chain round runs on that server.
- Ownership rollout: every machine runs this build before anyone hands a
  binding over. `relevo handover` refuses a target whose newest log entry was
  written below schema 31, naming it, because a machine on an older build
  cannot read the `writer` column the hand-over sets.
  A hand-over pushes to origin; `--remote` is accepted only for a remote with
  origin's URL, because the target's `bind --resume` fetches origin.
- `relevo db sync enable --as <installation> --yes` takes over a dead machine's
  identity on a fresh machine. It leaves sync off: restart the relevo daemon,
  then run `relevo db sync enable` again to finish. If interrupted or crashed
  during the adoption, rerun `relevo db sync enable --as <installation> --yes` to
  resume and finish. `relevo db sync status` says when another machine adopted the
  same identity; run `relevo db sync disable` on one of the two. Adoption trusts
  the remote and the joiner's clock, so run it only on a machine you control and
  after the old machine is truly off.

## Running chains

- Build big slices as `relevo chain --server zen`, on this repository's agents
  (CONTRIBUTING.md, "Developing relevo with relevo").
- Gate each chain round with `relevo chain --gate 'make check-affected'`, and
  a single binding with the same `--gate` flag. relevo exports
  `RELEVO_BASE` (the bind's base) to a round and to its gate, so
  `--gate 'make check-affected'` tests only the affected packages on a served
  worktree too; `BASE=<sha>` still overrides.
- After a halt, finish with manual rounds on the chain's builder, then
  `relevo chain --resume`.
- Fetch a served head with `git fetch zen:<served worktree> HEAD:refs/heads/X`.
  Served worktrees live at
  `~/.local/state/relevo-serve/serve/bindings/*/.worktrees/<name>`.
- After a slice, bind a reviewer and a security reader locally on a detached
  worktree of the head (`--cwd`, seed under 4096 bytes). They catch what the
  builders did not.
- End plans for builders that skip gofmt with: "`gofmt -l` changed files,
  `make check-affected`, commit, then report".
- A change that only edits comments still changes meaning: a trimmed comment
  can drop the reason behind a rule, a consequence, or what a nil or empty
  value means. Read the whole diff before merging one. Chain reviewers and the
  builder's own audit passed every such loss in the comment pass, and the
  compiler and tests cannot see it.
- A lane that brings a package under a check (lint, comments, file size)
  deletes that package's exclusion or allow-list entry in its first plan, not
  its last. While the entry stands, the chain gate cannot see what is left, so
  the lane can report done with the package still failing.
- Keep at most two whole-repo gates running on zen at once. Above that its
  load passes 60 and the slowest suites time out, which reads as a failed
  change.
