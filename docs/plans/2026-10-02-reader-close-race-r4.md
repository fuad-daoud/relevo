# Plan: a reader must not close on its marker with an empty stream (issue #841)

Base is `origin/main` at the binding's HEAD (v0.15.0-52-g3d22ab1b).

## The defect

`TestHeadlessE2EReaderRound` (`internal/e2e/reader_test.go:67`) is flaky (~15% on
a loaded machine): the reader round closes on the fake harness's done marker but
has no report, so its report entry carries `note=noreport` instead of an empty
note. The builder log does not exist yet when the tick sees the marker.

Verified ordering (the race):

- The fake harness writes the done marker first, sleeps 0.2s, then prints the
  stream-json final message, then exits
  (`internal/e2e/headless_test.go:493`, `:494`, `:501-504`).
- The daemon tick sees the marker (`internal/relevo/headless.go:748`, then
  `holdReaderOnMarker` at `:1118`, `os.Stat` at `:1119`).
- The only barrier is `held` from `rt.Runner.Alive` (`headless.go:1127-1139`). If
  `b.Builder.PID == 0`, `rt.Runner == nil`, or `Alive` reports the process gone,
  `holdReaderOnMarker` returns `present=true, held=false` and
  `markerClose` (`:1156`, `:1169-1180`) calls `closeOnMarker`.
- `closeOnMarker` (`internal/relevo/reconcile.go:316`) calls `writeReaderSummary`
  (`internal/relevo/summary.go:48`) at `:374`; with an empty stream
  `readerOutputText` yields `""` and `summary.go:78-80` writes nothing. The stat at
  `reconcile.go:378` is false, so the `noreport` branch at `:387-389` records the
  entry.

Commit `2f6e07c4` (PR #856) already removed the disagreement between the two
marker reads (`os.Stat` vs `Store.StatFile`). It does not remove the
"marker present but not held while the stream is still empty" window; its
regression test (`TestReaderRoundDoesNotCloseOnAMarkerItDidNotSee`,
`internal/relevo/reader_close_test.go:595`) covers the sealed-row case only.

## Required behaviour

A reader whose marker is present but whose round stream carries no text yet must
not close with `noreport` while the runner could still be writing its final
message. Hold the close and retry on the next tick, up to the existing
`readerFinalMessageGrace` (`internal/relevo/headless.go:1094`, 2 min). After the
grace, a genuinely empty reader round still closes (with `noreport`/the early
note) so a dead runner is not waited on forever. A reader that wrote its own
report file, or whose stream already carries a block, closes exactly as today.

## Fix direction

Prefer the smallest change at the reader-close decision:

- In `markerClose`'s reader branch (`internal/relevo/headless.go:1169-1180`),
  before `closeOnMarker`: if the marker is present but the round's stream yields
  no reader text and the marker is within `readerFinalMessageGrace`, return
  `b, false, false, nil` (hold).
- Equivalently, in `holdReaderOnMarker` (`:1124-1134`): when the marker is present
  and `PID == 0`/`Alive` false and the stream is empty, return `held=true` until
  the grace expires.
- Do not change the grace value or the `stopProcess`/early-exit behaviour.

Reuse the existing helper that decides "does this reader have text"
(`readerOutputText`, `summary.go:138`) rather than inventing a second rule.

## Tests to add

- `internal/relevo/reader_close_test.go`: a deterministic unit test with an empty
  stream, a present disk marker, and a runner reported not alive; assert the round
  stays open and no `KindReport` entry is written. Use the existing `fakeRunner`
  (`internal/relevo/fake_test.go:809`, `script`, `onAlive`) and `reconcile`
  (`fixture_test.go:176`), mirroring `TestReaderRoundDoesNotCloseOnAMarkerItDidNotSee`
  (`:595`) and `TestReaderRoundWaitsForExitAfterMarker` (`:552`).
- A second case: past the grace, the same empty round does close (so the fix does
  not hang a dead runner).
- Keep `TestHeadlessE2EReaderRound` as the end-to-end check; it must pass in a
  `-race -count=20` run on the build box.

## Mutation for the MasterMind

Revert the hold (let the empty-stream close proceed immediately). The new
deterministic unit test must fail, and `TestHeadlessE2EReaderRound -count=20`
should flake again.

## Constraints

- `make check` and `make e2e` green. Focused first:
  `go test -race -count=20 -run '^TestHeadlessE2EReaderRound$' ./internal/e2e` and
  `go test ./internal/relevo/ -count=1`.
- `internal/relevo` and `internal/e2e` coverage baselines are strict; never run
  `scripts/check-coverage.sh --write`, never lower a line. Add tests instead.
- Files ≤ 600 lines, functions ≤ 70 lines. Comments say why; no issue numbers,
  `§`, "round N" or "used to" in code or tests (`scripts/check-comments.sh`).
- `go mod tidy -diff` clean.
- Commit as new commits; save this plan's round section to
  `docs/plans/2026-10-02-reader-close-race-r4.md` and commit it with the code.

## Halt if

- the hold cannot be added without changing writer or chain-reader close
  behaviour (report which and stop);
- the deterministic test cannot be written without a timing sleep.

## Report includes

- the chosen fix site and why;
- `git diff --stat`, focused and full command outputs, and the `-count=20` result;
- new tests by name, the mutation, and any deviation.
