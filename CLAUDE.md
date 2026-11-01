# relevo

relevo automates the plan/report handoff between two AI coding agents: a MasterMind
hands work to a builder, and relevo moves the files between them. Builders are
headless or remote processes; relevo no longer integrates with herdr.

## Working with builders

The dispatch protocol -- `relevo send` not in-session subagents, headless by
default, one harness many worktrees, `relevo gate` on a usage limit,
stop rather than improvise -- is in the injected relevo guide
(`internal/mastermind/guide.md`), not here. What
follows is what is specific to this machine and this repo.

- Candidates, actors and policy live in relevo.db; `relevo config` shows and
  edits them. A builder's order is its actor's candidate list
  (`actors.builder.candidates`), and `relevo config` shows the current pick.
- relevo stops a builder *process* in exactly four places: `relevo done` and
  `relevo unbind` on a binding whose round is running, a mid-round switch of a
  builder whose provider you gated with `relevo gate`, and `relevo stop`.
  A binding's builder is a process relevo started, so there is no terminal to
  clean up afterwards.
- `relevo done` releases a clean worktree (the branch survives) so you can
  `gh pr checkout` in the main repo without `relevo unbind --done`; a dirty
  tree or an open round is kept and `relevo unbind --done` retries. `relevo
  bind --resume` restores a released worktree; rebind a DONE binding only
  after that restore.
- A headless round's output is its stream
  `~/.local/state/relevo/<name>/NNN-runner.jsonl` (stderr included; sealed into the
  database after the round). Read it rendered with `relevo show <name> --round N --transcript`.
- When a builder reports a usage limit mid-round, `relevo gate <token>` is
  enough: the daemon switches and resends. Do not rebind by hand unless
  `relevo status` says `NEEDS YOU`.

## Verifying a builder's work

Do not trust the report. Gate a builder round on `make check-affected` yourself
-- it is stricter than `go test ./...` alone, adding `gofmt` over every tracked
`.go` file, `go vet`, and a `go mod tidy` check, and it tests every package your
change reaches -- and compare `git diff --stat` against the plan's
declared scope. `make check` is the same static set over
`go test -race -count=1 -cover ./...` and the coverage check, and stays the
full local run. PR CI is the judge: `-race`, every package and
coverage. `make e2e` additionally runs one headless round end to end with
a fake harness binary; CI runs it, and it is not part of `make check`.

For anything subtle, mutation-test it: break the specific condition the change
turns on and confirm a named test fails. A test that passes both with and
without the logic is not pinning anything.

The step-by-step runbook -- temp space, where the suite runs, merging,
deploying, running chains -- is `docs/runbook.md`.

## Testing in isolation

The laptop, zen and contabo only ever run a tagged release (`docs/runbook.md`,
"Installing and deploying"); every untagged build -- a branch, main's head, a
test build -- runs only in a sandbox.

Never `make install` a test build over the developer's install: it shares one
database, one daemon and one binary path with production, so concurrent
versions corrupt each other and there is no rollback. Two flows exist:

- Default, no root: `scripts/relevo-sandbox.sh create <name>`, then
  `scripts/relevo-sandbox.sh shell <name>`. The sandbox carries an isolated
  binary, database, daemon and harness homes. See `docs/env-sandbox.md`.
- Full UID separation: `scripts/relevo-dev-user.sh`, which needs root. See
  `docs/dev-sandbox.md`.

## Conventions

- Plans and specs are local working files. `docs/plans/`, `docs/specs/` and
  `docs/superpowers/` are gitignored and never committed.
- State lives in `$XDG_STATE_HOME/relevo` (default `~/.local/state/relevo`);
  config resolves via `$XDG_CONFIG_HOME` (default `~/.config`).
  Compose relevo config paths through `userConfigRoot()` (`cmd/relevo/main.go`),
  never by hand -- see #42 for what hand-rolling one costs.

### Code style

The rules are in `docs/style.md`, the package layers and where new code goes
in `docs/architecture.md`, and how docs are written in `docs/docs-standard.md`.
The ones a round most often breaks:

- A comment says *why*, only where the code cannot, and never restates it. No
  history in code or tests: no issue or PR numbers, spec sections, "round N",
  "used to".
- Functions at most 70 lines, non-test files at most 600, one package per
  concept. A package imports only packages below it; a shared helper lives in
  the lower package, never copied.
- `make check` enforces lint, comments and file size. Packages not yet cleaned
  are listed as exclusions; a round that finishes one removes its entries, and
  no exclusion is ever added to get green.
- CI fails when a package's coverage drops more than a point below
  `testdata/coverage-baseline.txt`. A round that moves code between packages
  regenerates it with `sh scripts/check-coverage.sh --write` and says so; no
  round lowers a baseline to get green.

## Merging and CI

- Merge only after `gh pr checks <n> --watch` has finished with every job
  passing. Checking the first job to complete, or chaining `gh pr merge`
  behind an unconditional check, merged #68 with four jobs pending and broke
  `main` (#70 fixed it).
- CI runners have neither a harness binary nor network access. A test in
  `cmd/relevo` must not execute a subcommand that spawns a harness or reaches
  the network; test the rule as a pure function in `internal/relevo` instead.
  Say so in any plan step that adds a CLI test.
