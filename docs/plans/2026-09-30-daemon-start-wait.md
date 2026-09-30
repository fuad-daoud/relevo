# Plan: #717 — a relevo client waits, within a set limit, for a daemon that is still starting

## 0. Where the seed and the code disagree (read this first)

**Decision 3 is not fully true.** The seed says the daemon keeps its flock "through its whole life, including across a re-exec". The code does not do that:
- The re-exec path releases the lock before `syscall.Exec`: `cmd/relevo/daemon.go:427` `_ = lock.Close()`.
- The new image takes the lock again only after `loadConfigReadOnly` (`daemon.go:111` → `:119`).

So for part of the re-exec gap, `DaemonRunning()` returns **false**. What does survive the gap is the listener:
- The descriptor is handed over with `RELEVO_LISTEN_FD` (`daemon.go:421-431`) and adopted by `openOwnerListener` (`owner_serve_unix.go:41-50`).
- It is never unlinked and never re-bound, and `Drain` → `quiet()` (`owner.go:118-122`) leaves it open with a backlog.

The plan therefore treats "starting" as either of two signals:
1. A plain `connect` to the socket succeeds, which means some process holds the listener.
2. `DaemonRunning()` is true.

It checks the connect first, because the lock probe has a cost. `DaemonRunning` (`daemonlock.go:45-58`) *takes* the flock for a moment. If a daemon runs `AcquireDaemonLock` (`daemon.go:119`) at that instant, it loses and exits "already running". On a serve host that means `Restart=on-failure` and a lost listener descriptor. So the plan probes the lock only when the connect has failed, and at most once per process. That keeps the re-exec case (connect succeeds) away from the lock entirely.

## 1. How it fails today

The `ExecStartPre` client is `relevo config agents --force`. `routeForArgs` (`machinedb.go:87-98`) gives it `routeOwner` with `verbDialBudget` = 2 s. The path it takes:

1. `store` machine opener → `openDBRoute` (`machinedb.go:145-162`) → `db.Dial(sock)` (`internal/db/dial.go:20-22`).
2. `dial` (`dial.go:27-31`) creates `ctx` with a **`dialTimeout` = 2 s** deadline (`dial.go:15`) and calls `client.Info(ctx, sock)` (`client.go:46-57`).
3. `client.Info` → `dialSock` (`client.go:59-65`) → the installed hook `dialWithStart` (`machinedb.go:67-69`, `machinedb_unix.go:30-60`).
   - During drain, the exec gap, or the new image's open and migrate, the listener exists. `dialUDS` (`machinedb_unix.go:35`) succeeds at once because the kernel queues the connection in the backlog. No auto-start and no loop are involved.
4. `conn.handshake` (`session.go:40-60`) sets the socket deadline from `ctx` and sends hello, which sits in the buffer. `c.w.Read()` then blocks until the 2 s `dialTimeout` expires. The owner accepts nothing until `serveOwner` runs at `daemon.go:320`, which is after `openDBDirect` (`:159`) and `newRuntimeOn` (`:179`, the migration).
5. The error is `db: dial <sock>: <ErrOpen>: i/o timeout`. It is **not** `errOwnerUnavailable`.

**Which budget trips:** in the `ExecStartPre` case it is `internal/db/dial.go:15` `dialTimeout`, on the `client.Info` handshake. The `dialWithStart` loop budget (`dbRouteBudget`) never comes into play, because the dial succeeds.

`client.handshakeTimeout` (`client.go:23`, used in `openConn` `:67-80`) only bounds pooled connections. Those open after `Info` has succeeded, so they are not part of this failure. This is why the fix goes into the `Info` probe, and why pooled handshakes stay exactly as they are.

A second case is a socket that is missing during a start: a daemon built before the socket existed, or a fresh start between lock (`:119`) and bind (`:136`). Here `dialWithStart` loops for `dbRouteBudget` = 2 s and returns `errOwnerUnavailable`, still inside `dial.go`'s 2 s ctx.

## 2. The wait rule

- **Trigger:** all of the following:
  - the first `db.Dial` attempt has failed;
  - the process's route has a non-zero start wait;
  - a starting owner is evident: a plain connect to `sock` succeeds, or, if it does not, `store.New(root).DaemonRunning()` returns true (probed once).
- **With neither signal:** the failure returns exactly as today, fast. That covers a stale socket (the connect is refused) with no lock holder, and no socket with no lock holder.
- **What the wait covers:** one budgeted `client.Info` (dial **and** a completed handshake) under a single ctx whose deadline is `start + ownerStartWait`.
  - A connection sitting in the backlog is answered the moment `Serve` starts.
  - If the dial fails (no socket yet), `dialWithStart`'s own loop runs again. The outer loop re-attempts with short jittered steps until the deadline, so a socket that appears mid-wait (lock, then bind) is picked up.
- **The limit:** `ownerStartWait = 15 * time.Second`, one named constant in `cmd/relevo/machinedb.go`, next to `verbDialBudget`.
  - Sizing: the dead window is drain (≤5 s, `drainDeadline`) plus exec plus the new image's config load, lock and migration (12→15 ≈ 5 s). That is about 10 s, and 15 s adds margin.
  - The watcher debounce and preflight (≈4-6 s) run while the old image is still serving, so they do not count.
  - 15 s is far below systemd's default `TimeoutStartSec`.
  - The limit is measured from the first attempt, so a verb spends at most `ownerStartWait` in total, not `2 s + 15 s`.
- **Exempt (start wait = 0):**
  - `status --line` (`isStatuslineArgs`, `machinedb.go:117-119`);
  - any hook: argv carrying `--hook`, which covers `mastermind init --hook claude` and `mastermind notice --hook claude` from `claude-plugin/hooks/hooks.json`;
  - the peek verbs and the daemon (`routeNone`, which never dials);
  - `RELEVO_DB_DIRECT` (`routeDirect`).
- **Visible:** when the wait starts, one line goes to stderr, printed once: `relevo: the relevo daemon is starting; waiting up to 15s (<sock>)`. stdout stays clean.
- **When the limit runs out:** the error wraps `errOwnerUnavailable` (so existing `errors.Is` callers still match) and reads `relevo daemon at <sock> is starting or unresponsive after 15s`.

## 3. Changes per file, and why these seams

- **`internal/db/dial.go`**
  - Add `DialContext(ctx, sock)`. `dial` takes its deadline from the caller's `ctx` instead of creating its own at `:28`.
  - `Dial(sock)` becomes `DialContext` with a `dialTimeout` ctx. Its behaviour and the test hop are unchanged.
  - Why here: this is the only place the `Info` handshake budget lives, and a longer dial loop alone cannot reach the handshake.
  - `internal/db` must not learn about the daemon lock or auto-start, because those belong to `cmd/relevo`.
- **`internal/db/dial_unsupported.go`**: add the matching `DialContext` stub, so the build on other platforms still compiles.
- **`cmd/relevo/machinedb.go`**
  - Add `ownerStartWait`, the start-wait variable `dbRouteStartWait`, and a small exemption helper, `isHookArgs` (argv has `--hook`).
  - `routeForArgs` returns a start wait: 0 for statusline and hooks, `ownerStartWait` for other verbs.
  - `installDBRoute` takes and stores it.
  - `openDBRoute` does the following:
    - makes the first attempt with a `dbRouteBudget` ctx. That is 2 s for verbs, the same as today. The statusline gets 0.5 s, which finally puts the statusline's handshake inside its own spec budget as well;
    - if the start wait is 0 or no starting owner is evident, returns as today. A ctx-deadline failure is additionally wrapped in `errOwnerUnavailable`, so `status --line` (`status.go:196`) stays silent when an owner is bound but not serving;
    - otherwise prints the stderr line, loops `db.DialContext` against the single deadline, and returns the named error when the limit runs out.
  - The evidence probe is `ownerStarting(root, sock)`: connect first, then lock, lock at most once. It lives in `machinedb_unix.go`; `machinedb_other.go` gets a stub that returns false.
  - Why this seam and not `dialWithStart` or `SetDialer`: those two run for every pooled reconnect. Widening them would change pooled handshakes after the owner is serving, which decision 5 forbids, and they cannot see the handshake. `openDBRoute` runs once per process open and sees the whole of dial plus handshake.
- **`cmd/relevo/machinedb_unix.go`**: add `ownerStarting`. `dialWithStart` is unchanged.
- **`cmd/relevo/machinedb_other.go`**: add an `ownerStarting` stub that returns false.
- **`internal/db/wire/client/*`**: no change. `handshakeTimeout` and `openConn` stay untouched (decision 5).
- **`cmd/relevo/daemon.go`, `internal/store`, `dist/`**: no change (decision 1).

Keep `openDBRoute` under 70 lines. Split out the wait loop (`awaitStartingOwner`) if it grows.

## 4. Ordered steps

1. Add `DialContext` to `dial.go` and `dial_unsupported.go`, and point `Dial` at it. Done when `go test ./internal/db/...` is green with `TestDialAndNewOwnerAgreeOnSchemaOriginAndRows` and `TestDialRefusesAnUnreachableSocket` unchanged.
2. Change the route plumbing in `machinedb.go`: `ownerStartWait`, the start wait from `routeForArgs` and `installDBRoute`, and `isHookArgs`. Done when the package builds. `withOwnerRoute` (`machinedb_routes_test.go:121-126`) keeps its signature and installs start wait 0, so every existing route test is unchanged. Add a `withWaitingOwnerRoute(t)` helper next to it.
3. Add `ownerStarting` in `machinedb_unix.go`, with a stub in `_other.go`. Done when the probe test below passes: connect-evidence; lock-evidence with the lock held in the test through `store.New(root).AcquireDaemonLock()`, which works in-process because flock is per open file; neither.
4. Add the wait loop, the stderr line, the final error and the `errOwnerUnavailable` wrap to `openDBRoute`. Done when the tests in §5 pass.
5. Save this plan as `docs/plans/2026-09-30-daemon-start-wait.md` (user rule: the plan ships in the same PR as the code).
6. Run `sh scripts/check-coverage.sh`. It is not expected to move, since no code changes package; if it does move, say so in the report and do not lower the baseline. Then run `make check` once.

Commands:
- Focused, while iterating: `go test ./cmd/relevo -run 'Owner|Statusline|Route|Starting|Hook' -count=1 && go test ./internal/db/... -count=1`
- Full check, once at the end: `make check`

## 5. Tests

All tests are in `cmd/relevo/machinedb_routes_test.go`, or in a new `machinedb_wait_test.go` if the first file would pass 670 lines by much.

Ground rules:
- Use `shortStateRoot(t)` with `t.Setenv("XDG_STATE_HOME", …)`.
- Set `noDaemon(t)` wherever no owner is started, and use only in-process owners.
- No harness, no network, no user config.

The in-process "bound but not serving" owner: split `startTestOwner` (around `:85-117`) so a test can:
1. bind with `openOwnerListener(root)`;
2. open `d` with `openDBDirect`;
3. call `serveOwner(d, ln)` from a goroutine after a delay longer than 2 s (e.g. 2.5 s).

This is the same bind-early, serve-late order as `daemon.go:136` → `:320`. The precedent is `owner/reexec_test.go`'s helper.

- **`TestVerbWaitsForAnOwnerBoundButNotServing`**
  - Setup: waiting route, owner serves after 2.5 s.
  - Expect: `openDB(machineDBPath())` succeeds, took more than `dialTimeout`, and a `KVGet` works. stderr carries the single waiting line and stdout is empty.
- **`TestVerbWaitsWhileTheDaemonLockIsHeld`**
  - Setup: lock held, no socket; a goroutine binds and serves after 2.5 s.
  - Expect: success.
- **`TestVerbWithNoDaemonStillFailsFast`**
  - Setup: waiting route, no socket, no lock, `noDaemon`.
  - Expect: an error in well under `ownerStartWait` (at most `verbDialBudget` + slack), `errors.Is(err, errOwnerUnavailable)`, and no waiting line on stderr.
- **`TestStaleSocketDoesNotWait`**
  - Setup: a socket file left by a closed listener, no lock.
  - Expect: a fast failure and no waiting line.
- **`TestWedgedOwnerFailsAtTheStartWait`**
  - Setup: an owner that is bound and never serves; `ownerStartWait` temporarily lowered through a test override. If the builder cannot use a variable, set the limit through `installDBRoute`'s start-wait argument.
  - Expect: the error names the daemon as starting or unresponsive and names the socket, and elapsed time is at most the limit plus slack.
- **`TestStatuslineKeepsItsShortBudgetWhileAnOwnerStarts`**
  - Setup: an owner that is bound and never serves; `run([]string{"status","--line"})` with the route from `routeForArgs`.
  - Expect: stdout and stderr empty, exit nil, under 1 s.
  - The existing `TestStatuslineGivesUpSilently` (`:322`) stays as it is.
- **`TestRouteStartWaitExemptions`**: a pure table over `routeForArgs`.
  - `status --line` → 0;
  - `mastermind init --hook claude` → 0;
  - `mastermind notice --hook claude` → 0;
  - `config agents --force` → `ownerStartWait`;
  - `daemon` → routeNone;
  - `RELEVO_DB_DIRECT` → routeDirect.

## 6. Mutation pins

Each mutation below must make the named test fail. Name each result in the report.

- **The wait condition.** In `openDBRoute`, make the "starting owner evident" branch never wait (for example, force `ownerStarting` to false). `TestVerbWaitsForAnOwnerBoundButNotServing` must fail.
- **Evidence gates the wait.** Force `ownerStarting` to true. `TestVerbWithNoDaemonStillFailsFast` and `TestStaleSocketDoesNotWait` must fail.
- **The exemption.** Give `status --line` the `ownerStartWait`. `TestRouteStartWaitExemptions` and `TestStatuslineKeepsItsShortBudgetWhileAnOwnerStarts` must fail.

## 7. Files this round touches

This list is closed. Nothing is deleted.

1. `internal/db/dial.go`
2. `internal/db/dial_unsupported.go`
3. `cmd/relevo/machinedb.go`
4. `cmd/relevo/machinedb_unix.go`
5. `cmd/relevo/machinedb_other.go`
6. `cmd/relevo/machinedb_routes_test.go`, and/or a new `cmd/relevo/machinedb_wait_test.go`
7. `docs/plans/2026-09-30-daemon-start-wait.md`

Anything outside this list is a halt.

## 8. Risks and what must not regress

- **A daemon that is wedged but alive now costs up to 15 s** instead of 2 s, for verbs only.
  - It stays bounded: one constant, one deadline measured from the first attempt.
  - It stays visible: the stderr line appears when the wait starts, and the final error names the daemon as "starting or unresponsive" with the socket.
  - A wedged daemon with a live listener is also a case `relevo doctor`'s owner row already reports.
- **Lock-probe race.** This is described in §0. The probe runs only after a failed connect and only once per process, so it never touches the re-exec path. The same race already exists in `status.go:165`, `doctor/env.go:66` and `remote_sync.go:529`; it is not new here, just narrowed. `DaemonRunning` also creates the lock file under the state root, as the daemon itself does.
- **Must not change:**
  - `status --line`: silent, under 1 s, exit 0. Its handshake budget is actually tightened.
  - Hooks: 2 s, unchanged, and no stderr line.
  - `--json` verbs: stdout is untouched, and the waiting line goes to stderr only.
  - Pooled connections: `handshakeTimeout` and `dialWithStart` are unchanged.
  - `errOwnerUnavailable` matching: the new errors wrap it.
  - `TestVerbWithoutAnOwnerNamesTheSocket` and `TestNoOwnerStartsTheDaemonOnce` pass unmodified.
- **Long-lived clients** (`mcp`, `wait`, `ui`, serve) still reconnect a pooled connection on 2 s during a later re-exec. That is deliberately out of scope (decision 5).
- **Out of scope:** the deploy side (`srv` health check and rollback ordering). It belongs to a separate change in the servers repo.

## 9. What the report must include

- The final values of `ownerStartWait` and the stderr line, and the actual wording of the error when the limit runs out.
- For each mutation in §6: the mutation made, and the test that failed.
- Elapsed times seen in the wait test and the fail-fast test.
- `git diff --stat` compared against §7.
- The `make check` result, and whether the coverage baseline moved (expected: no).
- Confirmation that the §0 divergence was handled as written: the connect is checked first, and the lock is probed at most once.
