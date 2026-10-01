# Plan: #793 — an auto-started daemon must not outlive its usefulness

Issue #793. Base `origin/main`. One builder round, one commit. Scope:
`cmd/relevo` (daemon.go, startdaemon_unix.go, startdaemon_other.go,
registry_rows.go, `daemon_idle.go`, their tests, one golden) plus the
read-only `owner.Server` connection count, the usage text/README for
`daemon stop`, and this plan doc. No config schema, no protocol change, no
change to an existing interval or timeout, `relevo serve` untouched.

This document is the plan as built, with the round's report and the
follow-ups that outlive it.

## Behaviour

1. **The flag.** `relevo daemon --auto-exit-after D` (internal, defined like
   `--preflight`: in the flag set, out of the usage text). Default `0` =
   never. `D > 0` starts one watcher goroutine that, when it fires, cancels
   the daemon's own ctx: `Run` (`internal/relevo/daemon.go:102-131`) returns
   nil on `context.Canceled`, and `cmdDaemon`'s normal shutdown runs
   (`RemoveDaemonInfo`, DB close, lock release, listener close). The daemon
   logs one line before cancelling.
2. **What idle means.** For the whole period, all of: no owner connection
   (`owner.Server` live-connection count), no locally running builder process,
   no queued round. The clock starts at the first idle sample; any busy sample
   resets it; the exit lands on the first sample at or after the period (one
   poll of slack). Systemd, launchd and by-hand daemons never get the flag and
   keep running exactly as today.
   - running = `b.Builder.Headless() && b.Builder.PID != 0`, plus a running
     gate (`b.GateRun != nil`) — the gate is the daemon's own child and
     abandoning it mid-run would leave the round stuck; the liveness rule
     `internal/relevo/cpus.go:35-40` (`HeldIn`) already states.
   - queued = `!b.QueuedAt.IsZero() || b.Builder.RemoteQueue != nil`.
   - A store read that fails is **not** idle: one Warn, then busy, so an
     unreadable root never reads as "nothing to do".
   - A **nil owner server** counts as 0 connections: there is no socket a
     client could use, and the watcher never dereferences a nil `srv`.
3. **Root gone = exit now.** The sampler stats the state root first; a missing
   root exits immediately regardless of connections or rounds.
4. **`relevo daemon stop`.** New positional form of `daemon`: read this root's
   recorded `DaemonInfo.PID` (`store.ReadDaemonInfo`), SIGTERM it, poll the
   daemon lock (`DaemonRunning`) until it is gone or a bound expires. Friendly
   "no daemon is running" (exit 0, one stdout line, no signal) when there is
   no record or no lock is held; SIGTERM to an already-gone pid is the same
   friendly outcome; a daemon that outlasts the bound is an error naming its
   pid and the bound (exit 1). No SIGKILL, no escalation. When the systemd
   unit or the launchd plist is installed, the daemon is service-managed:
   refuse, naming `systemctl --user stop relevo` (systemd) or
   `launchctl kill SIGTERM gui/<uid>/<label>` (launchd), and never signal.
5. **Docs.** Only `daemon stop`: the `main.go` usage line, `cmdDaemon`'s own
   `fs.Usage`, README line 407 and the "Running the daemon" paragraph.
   `--auto-exit-after` stays out of the human usage and README.

## Seams

- `internal/db/wire/owner/owner.go:221-227` — `connCount` → `ConnCount` with a
  why-comment; `internal/db/wire/owner/conn.go:91` (welcome) is the other
  caller. `internal/db/wire/owner/unsupported.go` gets a `ConnCount() int`
  stub. Read-only; no behaviour change.
- `cmd/relevo/daemon.go` — `daemonFlagValues.autoExitAfter *time.Duration`,
  declared in `daemonFlagSet` next to `--preflight`; positional handling in
  `cmdDaemon`; the watcher wired just before `Run`.
- `cmd/relevo/daemon_idle.go` (new; daemon.go is at the 600-line ceiling) —
  `daemonActivity`, the pure `idleExit`, `daemonActivityNow`, and
  `watchDaemonIdle`.
- `cmd/relevo/startdaemon_unix.go` — `daemonArgv` gains the flag, one const
  `daemonAutoExitAfter = 10 * time.Minute`, and `daemonStop` with the
  `daemonStopSignal` / `daemonStopRunning` / `daemonStopSleep` seams and the
  `daemonStopBound` / `daemonStopPoll` consts.
- `cmd/relevo/startdaemon_other.go` — `daemonStop` stub.
- `cmd/relevo/registry_rows.go` — the daemon entry's flags list gains
  `--auto-exit-after`, so the parity test still matches `fs.VisitAll`.
  `daemon stop` deliberately gets no registry entry: it is a positional form,
  not a dispatcher case label.

## The report

- **Step 1, the connection count.** `connCount` is exported as `ConnCount`;
  the comment names the welcome and the daemon's idle watcher as its readers.
  `conn.go`'s welcome uses the exported name; `unsupported.go` returns 0 off
  unix. `TestConnCountCountsLiveConnections` pins 0 → 1 after a handshake →
  0 once the client closes.
- **Step 2, the flag and the spawn value.** `--auto-exit-after` is defined on
  `daemonFlagSet` as an internal flag (default 0). `daemonArgv` now emits
  `daemon --auto-exit-after 10m0s`. The registry row lists the flag; the
  `TestStartDaemonHelpers` argv expectation was amended. The interaction
  golden changed only `help-json.golden`.
- **Step 3, the idle decision.** `daemon_idle.go` holds `daemonActivity`, the
  pure `idleExit` (root gone → true; any of Conns/Running/Queued → false;
  `after <= 0` → false; else `idleFor >= after`), and `daemonActivityNow`.
  `TestIdleExit` tables every condition; `TestDaemonActivityFromStore` covers a
  headless binding with a pid, a `QueuedAt` binding, a `RemoteQueue` binding, a
  `GateRun` binding, an unreadable database (busy) and a missing root.
- **Step 4, the watcher.** `watchDaemonIdle` ticks at `idlePoll` (30s), starts
  its clock on the first idle sample, resets on any busy sample, logs one line
  and cancels at or past the period, and returns on ctx done. Wired in
  `cmdDaemon` only when `*autoExitAfter > 0`.
- **Step 5, `daemon stop`.** `cmdDaemon` checks the positionals after
  `parseFlags` and before the `--check`/`--preflight` block: exactly `["stop"]`
  and neither peek flag runs `daemonStop()`; any other positional is a usage
  refusal. `daemonStop` refuses a service-managed host, reports "no daemon is
  running" for no record or no lock, otherwise signals the recorded pid and
  polls the lock to a bound. User-visible shape: friendly line, exit 0;
  bound exceeded, error naming pid and bound, exit 1.
- **Step 6, usage and README.** The `main.go` daemon line, `cmdDaemon`'s
  `fs.Usage` and the README (a `daemon stop` bullet and the "Running the
  daemon" sentence) all name `daemon stop`. C12/contract tests stay green.

## Commands run

- `go test -race -count=1 ./internal/db/wire/owner/ -run ConnCount` — green.
- `go test -race -count=1 ./cmd/relevo/ -run 'IdleExit|DaemonActivity|DaemonStop|WatchDaemonIdle|StartDaemonHelpers|Registry'`
  — green.
- `go test ./cmd/relevo -run Contract -update` — green, but it does not match
  `TestHelpJSONDocumentsTheSurface` (the name has no "Contract"), so the golden
  was regenerated with `go test ./cmd/relevo -run TestHelpJSONDocumentsTheSurface -update`;
  `git diff --stat -- cmd/relevo/testdata` then showed only `help-json.golden`.
- Mutations (each reverted after it failed):
  - delete `a.Conns > 0` from `idleExit` → `TestIdleExit/live_connection` fails.
  - delete `a.Running` → `TestIdleExit/running_builder` fails.
  - delete `!a.RootExists` → `TestIdleExit/root_gone_while_busy` (and the
    missing-root sampler case) fails.
- `make check` green.

## Deletions

Nothing is deleted. The two existing expectations that changed are amended,
not deleted: the `daemonArgv` assertion in `machinedb_routes_test.go` and
`testdata/contract/help-json.golden`.

## Risks, readings and follow-ups

- **Readings of the fixed decisions:** a running gate counts as "a locally
  running builder"; a remote binding's `RemoteQueue` counts as "a queued
  round". Both only keep the daemon up longer.
- **Not covered, on purpose:** `relevo doctor` listing other roots' daemons
  (the ticket's follow-up; it is the only tool that will see a daemon whose
  root was renamed rather than deleted), and a daemon old enough to hold the
  lock without a `daemon.json` — `daemon stop` reports "not running" for it, as
  `store.DaemonInfo`'s own comment says.
- **Never idle-exits:** a systemd/launchd service daemon, a hand-started one,
  and `relevo serve`'s daemon — none of them is spawned by `spawnDetachedDaemon`.
- **`daemon stop` refuses on a service-managed host** so it never fights
  systemd's restart policy. The refusal has its own test.
- **Bounded by the next reconcile:** a stale `Builder.PID` keeps the daemon up
  only until the tick clears it; that is the intended conservative direction.
- **`daemon stop` opens the database directly** (`ReadDaemonInfo`, the same
  call `bugreport` makes): the route for any `daemon` invocation is `routeNone`
  and dialing the owner would auto-start the very daemon being stopped.
