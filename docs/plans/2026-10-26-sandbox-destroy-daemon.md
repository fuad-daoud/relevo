# Plan: stop the sandbox daemon in `destroy` (2026-10-26, issue #1393)

Scope: `scripts/relevo-sandbox.sh` and `scripts/relevo-sandbox_test.sh` only.
No Go changes. `destroy <name>` must stop the sandbox's relevo daemon before
`rm -rf`, so no daemon survives listening on the deleted sandbox's socket.

## Behaviour

1. Daemon running in the sandbox: `destroy` asks it to exit via the sandbox
   binary's `daemon stop` under the sandbox exports, waits until the daemon's
   lock has cleared (the bounded wait is inside `daemon stop`), then proceeds
   to `chmod`/`rm -rf`.
2. No daemon running: `destroy` succeeds exactly as today; the stop step is a
   no-op (`daemon stop` prints `no daemon is running` and exits 0).
3. Sandbox binary absent (`--no-build` sandbox, empty `bin/`): the stop is
   skipped and `destroy` succeeds (see Q1).
4. Stop fails or times out: without `--force`, `destroy` dies and leaves the
   directory; with `--force`, it warns and proceeds to `rm -rf`.
5. `--dry-run` prints the stop action (the full sandbox-exported command) and
   writes/removes nothing.
6. Marker refusal still runs first: a directory with no `sandbox` marker is
   refused before any stop is attempted, and no signal is ever sent to a
   process outside `<root>/<name>` (marker + `sandbox_dir` prefix guards).

## Open questions, settled from code (and verified against a live daemon)

- **Q1: can `destroy` always stop via `<sb>/bin/relevo daemon stop` under the
  sandbox exports?** Yes, when the binary exists, and no Go change or
  `ss`/`fuser`/`kill` fallback is needed.
  - `relevo daemon stop` (`cmd/relevo/daemon.go:70-74`) runs `daemonStop()`
    (`cmd/relevo/startdaemon_unix.go:137-193`): it resolves the state root
    through `store.DefaultRoot()` (`internal/store/store.go:156-169`,
    `$XDG_STATE_HOME/relevo`), so under the exports `sb_relevo` already builds
    (`scripts/relevo-sandbox.sh:356-364`, `XDG_STATE_HOME=$_sb/state` at
    `:360`) it targets exactly this sandbox's daemon; no-ops with
    `no daemon is running`, exit 0, when the lock is free (`:152-159`) or the
    recorded pid is already gone (`:168-174`); else SIGTERMs the pid read over
    the daemon's own socket (`:160-167`, `:195-204` — a stop can never start a
    daemon) and waits bounded for the lock to clear (5 s bound, 50 ms poll,
    `:124-130`, `:181-192`; no SIGKILL escalation).
  - Live check (2026-10-26): a real `relevo daemon` under a sandbox-shaped
    `XDG_STATE_HOME` held `state/relevo/.daemon.lock` and served
    `state/relevo/relevo.sock`; `relevo daemon stop` printed
    `stopped daemon <pid>`, the process exited, the socket file went away and
    the lock freed. With no daemon, `daemon stop` printed `no daemon is
    running` and exited 0.
  - **Binary absent**: skip the stop. `create --no-build` never runs the seed
    and doctor steps (`scripts/relevo-sandbox.sh:271-273`) that are the only
    paths through which this script auto-starts a daemon (the CLI spawns its
    daemon from the binary that was run — `cmd/relevo/startdaemon_unix.go:78-111`,
    `daemonArgv` `:64-67`; the sandbox shell only prepends `$_sb/bin` to PATH,
    `:401`), so with an empty `bin/` the sandbox itself started no daemon that
    its own binary could stop. Any foreign daemon holding this sandbox's state
    lock is still caught by the (corrected, Q2) held-lock refusal below. No
    signal fallback is added: `daemon stop` needs none of `ss`/`fuser`/`lsof`
    (the pid comes from the daemon record, `startdaemon_unix.go:195-204`), and
    a from-sh pid lookup without the binary is unnecessary and unbounded.
- **Q2: is the existing lock probe path wrong?** Yes. The script probed
  `$_sb/state/.daemon.lock` (`scripts/relevo-sandbox.sh:460`), but the daemon's
  lock is `filepath.Join(s.root, ".daemon.lock")`
  (`internal/store/daemonlock.go:65`; `daemonLockFileName = ".daemon.lock"`,
  `internal/store/store.go:49-52`) where `s.root` is `store.DefaultRoot()`
  (`cmd/relevo/daemon.go:120,137`), i.e. under the sandbox exports
  `$_sb/state/relevo/.daemon.lock` — one level deeper. Verified live: with a
  running daemon, `flock -n` on the shallow path reported free while the true
  path reported held. The probe and the stop-wait are aligned on the true
  path, and the existing held-lock test (`scripts/relevo-sandbox_test.sh:306-307`)
  is updated to hold the true path (correction of a wrong path, not a deleted
  behaviour). The socket is `SocketPath(root)` = `<root>/relevo.sock`
  (`internal/db/wire/owner/listen.go:15,20-26`), so
  `<sb>/state/relevo/relevo.sock`; `destroy` does not probe the socket
  directly — the stop's bounded lock-clear wait is the bound, the daemon
  unbinds the socket as it exits, and the socket lives inside the directory
  `rm -rf` removes anyway.
- **Q3: `--force`/timeout ordering.** Decision (as planned): attempt the stop
  first, then enforce refusal-on-timeout without `--force`. Concretely in
  `cmd_destroy`: marker check → (binary present) `sb_relevo daemon stop`; on
  failure without `--force` die and leave the directory, with `--force` warn
  and continue → held-lock probe on the corrected path (catches the
  foreign-daemon case; die without `--force`, warn and proceed with it) →
  `chmod -R u+w` + `rm -rf` unchanged. Under `--dry-run` the stop prints
  through `plan()` (`scripts/relevo-sandbox.sh:37-39`) instead of running.

## Steps

1. This plan file.
2. Stop wiring in `cmd_destroy` (Q1 mechanism, Q3 ordering, Q2 corrected path;
   `--dry-run` plans the stop; no-daemon case exits 0). Gate: `sh -n`,
   shellcheck, manual live round trip leaves no listener on
   `<sb>/state/relevo/relevo.sock`.
3. Test `destroy stops a running daemon before removing the sandbox` (stub
   sandbox `bin/relevo` answering `daemon stop`, recording calls and refusing
   to run without the sandbox exports).
4. Tests `destroy without a daemon still succeeds` and `destroy --dry-run
   plans the stop and removes nothing`.
5. Test `destroy refuses a missing marker before stopping` (markerless dir
   keeps its contents; the stub records no call).
6. Mutation check: neutralise the stop step, confirm the step-3 test fails,
   restore.
7. Gate: `sh scripts/relevo-sandbox_test.sh` green (plus shellcheck),
   `git diff --stat` shows only the two scoped files and this plan.

## Deletions

None. The lock-path change is a correction, and the old held-lock test is
updated, not dropped.
