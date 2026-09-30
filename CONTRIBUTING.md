# Contributing to relevo

Thanks for looking. relevo is small and intends to stay that way, so the most
useful thing you can do before writing code is open an issue and check the
change is in scope.

## What relevo is, and is not

relevo moves files between a MasterMind and a builder and reports what each round
did. It makes no judgements: whether a report is good, whether a question needs
a human, whether the work is done — all of that stays with the MasterMind (or the
person at the keyboard). Changes that ask relevo to decide something on the
human's behalf are almost always out of scope, however convenient they look.

Two rules the code protects, and that a change must not weaken:

- **One writer per tree.** Exactly one builder works a tree at a time; relevo
  never starts two on the same directory.
- **Never destroy a record.** A binding's round log and worktree are the record
  of what happened, so relevo archives by default and removes nothing a human did
  not ask for.

## Getting set up

You need Go 1.22 or newer. The test suite fakes the harnesses, so you can build,
test and run relevo without any agent CLI installed.

```
git clone https://github.com/fuad-daoud/relevo
cd relevo
make check
```

`make check` is the whole gate: `gofmt -l .`, `go vet ./...`, and
`go test -count=1 ./...`. CI runs exactly that on Linux and macOS against both
the Go 1.22 floor and current stable, plus a cross-compile sweep. If
`make check` passes locally it will almost certainly pass there.

## Making a change

- **Write the test first.** Nearly every behaviour in `internal/relevo` is
  covered by a test that drives a fake runner — add to it rather than reaching
  for a real harness.
- **Say why in a comment, not what.** The existing comments explain the
  reasoning behind a decision, especially where the obvious implementation
  would be wrong. Match that; skip comments that restate the code.
- **Keep it stdlib.** relevo has no third-party dependencies and that is a
  feature. A PR that adds one needs to argue for it.
- **Run `gofmt`.** CI fails on unformatted files.
- **One change per PR.** It makes review, and reverting, far easier.

## Platform support

relevo targets Linux and macOS. State locking is behind a build tag
(`internal/store/lock_unix.go`), and the tree must keep compiling for other
platforms even though they are unsupported — the cross-compile job in CI is
what enforces that. If you touch anything platform-specific, put it behind a
build tag rather than a `runtime.GOOS` check.

## Releasing

The Claude Code plugin manifests (`claude-plugin/.claude-plugin/plugin.json`
and `.claude-plugin/marketplace.json`) are bumped and merged *before* the tag is
created. Tagging first leaves that tag's own manifest pointing at a release
that does not exist. `make release` does both in the right order.

## Reporting bugs

Run `relevo bugreport`: it assembles a redacted diagnostic bundle locally, writes
it under the state root, and prints the `gh issue create` line that files it. Add
`--logs` for a round's report, diff and transcript when those can be shared.
`relevo version` and `relevo status --json` remain the short answers a maintainer
may ask for.

## License

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE) that covers the project.
