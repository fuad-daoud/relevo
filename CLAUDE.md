# relevo

relevo automates the plan/report handoff between two AI coding agents: a planner
hands work to a builder, and relevo moves the files between them. Builders are
headless or remote processes; relevo no longer integrates with herdr.

## Working with builders

The dispatch protocol -- `relevo send` not in-session subagents, headless by
default, one harness many worktrees, `relevo gate` on a usage limit,
stop rather than improvise -- is in the shipped `architect` definition
(`internal/harness/agents/architect.*.md`, "Handing off"), not here. What
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
  A round from before the stream rename is `NNN-builder.jsonl`, which readers fall back to.
  Rounds from before builder-log round 2 (#478) also have `NNN-builder.log`.
- When a builder reports a usage limit mid-round, `relevo gate <token>` is
  enough: the daemon switches and resends. Do not rebind by hand unless
  `relevo status` says `NEEDS YOU`.

## Verifying a builder's work

Do not trust the report. Run `make check` yourself -- it is stricter than
`go test ./...` alone, adding `gofmt` over every tracked `.go` file, `go vet`, and a
`go mod tidy` check -- and compare `git diff --stat` against the plan's
declared scope. `make e2e` additionally runs one headless round end to end with
a fake harness binary; CI runs it, and it is not part of `make check`.

For anything subtle, mutation-test it: break the specific condition the change
turns on and confirm a named test fails. A test that passes both with and
without the logic is not pinning anything.

## Conventions

- Specs live in `docs/specs/YYYY-MM-DD-<topic>-design.md`. Implementation
  plans live in `docs/plans/YYYY-MM-DD-<name>.md`, a directory introduced by
  #45 -- follow it or drop it, it has no history behind it yet.
- State lives in `$XDG_STATE_HOME/relevo` (default `~/.local/state/relevo`);
  config resolves via `$XDG_CONFIG_HOME` (default `~/.config`).
  Compose relevo config paths through `userConfigRoot()` (`cmd/relevo/main.go`),
  never by hand -- see #42 for what hand-rolling one costs.

### Code style

- A package comment is 1-3 lines saying what the package owns. A comment says
  *why*, and only where the code cannot: a non-obvious constraint, where a
  number comes from, an ordering that matters, a hazard. It never restates the
  code, and a doc comment on an exported name is written only when it adds
  something the name and signature do not.
- No history in the code: no issue or PR numbers, no spec sections, no
  "round N", "used to", "pre-#NNN". Git and the issues hold history. Tests
  follow the same rules, and a test's name says what it pins.
- The dexpace Go styleguide applies, with two exceptions: no "two assertions
  per function" rule, and no mandatory doc comment on an exported name.
  Functions are at most 70 lines; non-test files at most 600; one package per
  concept.
- `make check` enforces this with golangci-lint (`.golangci.yml`) and
  `scripts/check-comments.sh` / `scripts/check-filesize.sh`. Packages and files
  not yet cleaned are listed as exclusions; a round that finishes a package
  removes its entries, and a new exclusion is never added to get a round green.
- `make check` also fails when a package's statement coverage drops more than
  one point below `testdata/coverage-baseline.txt`; a round that moves code
  between packages regenerates the baseline with
  `sh scripts/check-coverage.sh --write` and says so in its report, and no
  round lowers a baseline to get green. The baseline is tied to the Go minor
  version and platform recorded in its header, is enforced on CI's
  ubuntu-latest/go-stable leg, and is skipped elsewhere; a Go upgrade on that
  leg means regenerating it.

## Merging and CI

- Merge only after `gh pr checks <n> --watch` has finished with every job
  passing. Checking the first job to complete, or chaining `gh pr merge`
  behind an unconditional check, merged #68 with four jobs pending and broke
  `main` (#70 fixed it).
- CI runners have neither a harness binary nor network access. A test in
  `cmd/relevo` must not execute a subcommand that spawns a harness or reaches
  the network; test the rule as a pure function in `internal/relevo` instead.
  Say so in any plan step that adds a CLI test.
- A cmd/relevo test never reads the user's real config, state or data, and
  never sees the calling harness: the package's TestMain points HOME,
  XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_DATA_HOME at a temp root and
  unsets CLAUDECODE, CLAUDE_*, RELEVO_*, ANTIGRAVITY_* and TYPESAFE_API_KEY
  (#235, #463). A test that needs its own config writes it under a
  t.TempDir() it sets as XDG_CONFIG_HOME; a test that needs a harness
  variable t.Setenv's it.
