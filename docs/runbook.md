# MasterMind runbook: verify, merge, deploy

How a MasterMind verifies a builder's branch, merges it, and deploys relevo.
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

## Verifying a branch

- Static checks run locally: `make check-static` (gofmt over tracked and
  untracked Go files, `go vet`, golangci-lint, the comment/size/name guards,
  and a `go mod tidy` check).
- The suite runs on zen, not on contabo: contabo (7G) OOM-kills it, and its
  `dev run` output vanishes silently. `git archive` the tree to
  `zen:~/.cache/mm-*`, `git init` a snapshot there, then `make check-test`,
  the modernc leg, and `make e2e`. The modernc leg is:
  `go vet -tags modernc ./internal/db/...` and
  `go test -race -count=1 -tags modernc ./internal/db/... ./internal/store/...`.
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

## Running chains

- Build big slices as `relevo chain --server zen`.
- After a halt, finish with manual rounds on the chain's builder, then
  `relevo chain --resume`.
- Fetch a served head with `git fetch zen:<served worktree> HEAD:refs/heads/X`.
  Served worktrees live at
  `~/.local/state/relevo-serve/serve/bindings/*/.worktrees/<name>`.
- After a slice, bind a reviewer and a security reader locally on a detached
  worktree of the head (`--cwd`, seed under 4096 bytes). They catch what the
  builders did not.
- End plans for builders that skip gofmt with: "`gofmt -l` changed files,
  `make check`, commit, then report".
