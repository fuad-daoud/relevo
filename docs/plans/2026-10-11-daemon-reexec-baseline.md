# Daemon re-exec baseline (slug: daemon-reexec-baseline)

Fixes #1104

## Premise

`cmd/relevo/daemon.go:251-253` captured the watcher's baseline with
`upgrade.ExeIdentity(exe)` — a stat of the path — after the lock, the listener
bind, the DB open, the runtime build and the scope logging.
`cmd/relevo/daemon.go:431-436` then handed that value to `Watcher.Started`.

A rename-over landing between exec and line 252 therefore becomes the baseline
while the running image is still the old file. The watcher then compares every
poll of the path against the replacement, finds no change, and never re-execs.

## Behaviour and cases

- The baseline comes from the running image on Linux (dev/ino of the file this
  process exec'd), never from a later stat of the path.
- Case 1 (the bug): a replacement lands between exec and baseline capture →
  first tick `Wait`, second tick `Reexec` (the existing two-tick debounce in
  `watch.go:60-95`, unchanged).
- Case 2: no replacement → `None`, no spurious re-exec, exactly as today.
- Case 3: a self-image read fails → re-exec disabled with the existing warning
  and the daemon runs on (`daemon.go:253-260` path, unchanged).
- Case 4: non-Linux unix and non-unix builds behave exactly as today (path
  stat; disabled off unix via `exe_other.go`), so they keep building and
  working; Linux-only is where the fix bites.

## Seams

- `cmd/relevo/daemon.go:248-260` — baseline capture. The comment there claimed
  the capture happened "before any replacement can land"; that claim was the
  bug, so the comment now says what the identity actually is.
- `cmd/relevo/daemon.go:431-436` — `Watcher{Path, Started, Stat, Preflight}`
  wiring. `Stat` stays `upgrade.ExeIdentity`: polling the path is correct once
  the baseline is the running image.
- `internal/upgrade/exe_unix.go` — `//go:build unix`, which covers Linux too, so
  a new `//go:build linux` file defines a *different* symbol rather than a
  second `ExeIdentity`.
- `internal/upgrade/exe.go:15-27` — `ResolveExe`, unchanged; it still supplies
  the re-exec target path and the `daemon.json` `Exe`.
- `internal/upgrade/exe_other.go:11-14` — off-unix stays `ErrUnsupported`, so
  re-exec is disabled there.
- `internal/upgrade/watch.go:48-95` — `Watcher` with injected `Stat`/`Preflight`.
- `internal/store/daemoninfo.go:9-17` — `FileID{Dev, Ino, Size, ModTime}`; the
  dev/ino pair is what tells the old image from the rename-over replacement.

## Ordered steps

1. Add a self-image identity source in `internal/upgrade` (Linux stats the
   running image; unix-non-Linux falls back to current path-stat semantics;
   non-unix stays unsupported) with a build-tag layout that compiles alongside
   `exe_unix.go` — deliverable: new symbol, and `go build` green for `linux`,
   `darwin`, `windows`.
2. Switch the `daemon.go` baseline to the self-image identity, keeping the
   resolve/stat-failure → disabled-with-warning path — deliverable: minimal edit
   in `cmdDaemon`; check: existing `cmd/relevo` tests and `go vet` pass.
3. Add a test that pins the baseline source itself. On Linux: copy the test
   binary to a temp path, start it as a child (the `os.Args[0]` + env-var
   helper-process pattern; no harness, no network, so it is CI-safe), rename a
   different file over that path while the child runs, then have the child
   report its running-image identity and the path's identity: the running-image
   identity must equal the original file's identity, and the path's must differ.
   Keep a small injected watcher test for `Wait`→`Reexec`. Mutation to kill:
   make the daemon's baseline call the path stat again. Skip the helper test
   off Linux with a reason.
4. Add `internal/upgrade` tests for Case 2 (`None` on unchanged identity) and
   Case 3 (self-image error → disabled baseline, daemon runs) — deliverable:
   edge tests each naming the guard-removal mutation they kill; check: each
   fails when its guard is removed.
5. Run focused `go test ./internal/upgrade/...`, then full `make check`.
6. Save this plan as `docs/plans/2026-10-11-daemon-reexec-baseline.md` and
   commit plan + code as one new commit messaging `Fixes #1104` (never
   amend/rebase a pushed branch).

## Deleted behaviour

None — no observable behaviour is removed; the path-stat baseline remains the
fallback and the off-Linux behaviour.

## Scope (touched)

`internal/upgrade/selfimage_linux.go`, `internal/upgrade/selfimage_unix.go`,
`internal/upgrade/selfimage_other.go`, `internal/upgrade/selfimage_linux_test.go`,
`internal/upgrade/selfimage_test.go`, `cmd/relevo/daemon.go` (baseline lines
only), `docs/plans/2026-10-11-daemon-reexec-baseline.md`. No other-lane files
(`internal/relevo/*`, `internal/serve`, `internal/remote`, `internal/sync`,
`cmd/relevo/gate.go`, `cmd/relevo/db_sync.go`) are touched. No CLI-subcommand
test is added, so the harness/network CI rule needs no special pleading; the
regression test is pure/injected in `internal/upgrade` apart from the Linux
helper process, which forks a copy of its own test binary and opens no socket.

## Note on the regression test

The plan's original step 3 injected `Started` into the watcher, which passes
whether or not the daemon takes its baseline from the running image — it pins
the debounce, not the bug. Step 3 above therefore pins the baseline source
directly, and the small injected `Wait`→`Reexec` test is kept alongside it for
the watcher's half of the same question.