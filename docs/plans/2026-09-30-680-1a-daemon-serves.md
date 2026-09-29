# Plan: #680 stage 1a — the daemon serves `relevo.sock` and hands it across re-exec

Spec: `docs/specs/2026-09-29-db-owner-design.md` §5-§8. Read at `382b4bc`.
Issue #680 is not readable here (no remote); the spec's §10 is the authoritative
correction and is treated as the issue.

## Amendments applied with this plan

Four amendments override the plan and the spec wherever they disagree.

**E — bind early, serve late.** Bind the socket (or adopt `RELEVO_LISTEN_FD`)
right after the lock and the path check, not after the startup passes; start
`Serve` once the handle is ready. Connections made meanwhile wait in the backlog
instead of finding no socket. The stale-socket unlink stays in the bind path,
under the lock.

**F — a newer schema still gets a socket.** Serve whenever the database opened,
including when its schema is newer than this binary: the owner is a SQL pipe,
and a dialled client answers `Newer()` from its own embedded maximum (stage 0b).
If the daemon nils `rt.DB` only to pause its own ingest on a newer schema, keep
a handle for the owner. Only an open failure means no socket (one warning). Test
it.

**G — `restarting` is retryable.** The owner guarantees a request refused
`restarting` was never executed, so the client maps that refusal to
`driver.ErrBadConn` (wrapping the refusal, so it stays inspectable);
`database/sql` then retries pool-level statements on a fresh connection. Other
refusals stay plain errors. Pin: a pool-level `Exec` sent during a drain is
retried and, once a new owner serves, applied exactly once. (Retrying a `db.Tx`
whose `BEGIN` was refused is stage 1b; `db.Tx` does not change here.)

**H — transaction tracking on success only.** `inTx` becomes true when
`BEGIN`/`BEGIN IMMEDIATE` succeeds, false when `COMMIT`/`END` succeeds or when
`ROLLBACK` runs; a `COMMIT` that fails (e.g. busy) leaves it true, since SQLite
keeps that transaction open. Pin: a failed `COMMIT` keeps the connection counted
as in a transaction for the drain.

The mutation checks these amendments carry are in §3 step 9.

---

## 1. Behaviour and the cases (the contract)

**Serve.** `cmd/relevo`'s daemon path (`cmdDaemon`) binds `<state root>/relevo.sock`
after `AcquireDaemonLock`, the `rt.DB` open/validation and the one-shot startup
passes, and immediately before `WriteDaemonInfo` — mode 0600 inside the 0700
root, peer-uid checked per accepted connection (0b already does this), then
`go db.NewOwner(rt.DB).Serve(ln)`. A nil `rt.DB` (open failed, or a newer
schema) means no socket: one warning, and the daemon runs otherwise as today.
`--check` and `--preflight` return before the lock and never bind, dial or
auto-start.

**Which `*db.DB`.** The owner serves `rt.DB`. Why: it is the handle `cmdDaemon`
opens under the lock, the one the daemon's own work (ingest, dedupe,
compression, origin backfill, installation row) writes through, and it already
carries the installation origin the handshake must report. The other handles the
daemon holds on the same file today — the config store's, the gates/claims
store's, the agy deliverer's and the release cache's — are left exactly as they
are; spreading the socket over them is 1b's job, not 1a's.

**Path limit.** `owner.SocketPath(root)` builds `<root>/relevo.sock` and refuses
when its byte length reaches the platform `sun_path` limit (104 darwin, 108
linux). Checked right after the lock and before `newRuntime`, so a winning start
refuses with one clear line naming the path and the limit before it mints or
migrates anything; a losing start still says "already running". No fallback
location.

**Stale socket.** The bind path removes any file at the path before binding —
safe because the lock is held, so no live owner can exist. The adopt path never
unlinks.

**Re-exec.** The upgrade hook returns true → `Run` returns `ErrReexec`. New
order: (a) drain the owner (≤ ~5 s), (b) `closeDB()`, (c) `stop()`, (d)
`lock.Close()`, (e) dup the listener and clear `FD_CLOEXEC` on the dup, (f)
`reexec` with `RELEVO_LISTEN_FD=<fd>` plus the existing `RELEVO_REEXEC_FROM`. The
drain must precede `closeDB()` or an open transaction cannot commit. The child,
before either bind path, sees `RELEVO_LISTEN_FD` and adopts with
`net.FileListener` — no unlink, no rebind — takes the lock, opens and migrates,
serves. `Serve` stops accepting when the drain starts, so connections that
arrive across the gap sit in the inherited backlog and the new image accepts
them.

**Drain.** `Server.Drain(ctx)` in three named pieces: mark draining and stop the
accept loop while keeping the listener open; refuse a request that arrives on a
connection with no open transaction with `refuse{restarting}` (`conn.start`,
before `claim`); let requests inside an open transaction run so it can commit;
wait until every connection is out of a transaction or the ~5 s deadline; then
drop the client connections (rollback) but never the listener. Transaction
membership is tracked per connection from the SQL already on the wire
(`BEGIN`/`BEGIN IMMEDIATE` opens, `COMMIT`/`ROLLBACK`/`END` closes) because
relevo's own `Tx` sends those statements through `ExecContext` rather than
through `database/sql`'s `BeginTx` — a raw protocol could not otherwise tell an
idle committed connection from an open transaction.

**Doctor row.** A new global row `owner`, assembled in `cmd/relevo` from a dial
(like `databaseCheck`/`serverChecks`) rather than through `doctor.Env`: ok reads
`socket <path> · pid <n> · protocol relevo-owner v1 · <n> connections`; a refused
dial or absent socket is a warn with a fix; a daemon that predates 1a reads as
warn. `wire.Welcome` gains `PID` and `Conns`.

## 2. Seams — files, types, functions

**`internal/db/wire` (protocol, no sockets).**

- `msg.go` `Welcome` gains `PID int` and `Conns int`; `wire.Version` stays 1
  (additive fields).

**`internal/db/wire/owner` (unix).**

- `conn.go` `serve()` fills `Welcome.PID` (`os.Getpid()`) and `Welcome.Conns`
  (`len(s.conns)` under `s.mu`).
- `conn.go` `start()` — the exact point `restarting` is sent: when `s.draining`
  and `!c.inTx`, answer `c.refuse(wire.RefuseRestarting, …)` and return without
  claiming a slot; `run`/`exec`/`query` set `c.inTx` from the statement's first
  keyword.
- `owner.go` `Server` gains `draining bool` (under `mu`) and a
  `beginDrain/quiet/dropClients/Drain` triple; `Serve` returns without closing
  the listener when the stop is a drain, and still closes it on `Close`.
- New `listen.go`: `SocketPath(root string) (string, error)`, `Listen(root
  string) (net.Listener, error)` (path check, stale unlink, bind, `chmod 0600`),
  `Adopt(fd int) (net.Listener, error)` (`os.NewFile`+`net.FileListener`),
  `Inherit(ln net.Listener) (*os.File, error)` (a non-CLOEXEC dup whose fd goes
  into `RELEVO_LISTEN_FD`).
- New `sunlimit_linux.go` (108) / `sunlimit_darwin.go` (104), mirroring
  `peer_linux.go`/`peer_darwin.go`.
- New `listen_unsupported.go` (`//go:build !unix`): `SocketPath`/`Listen`/
  `Adopt`/`Inherit` refuse; `Drain` on the existing stub `Server` is a no-op.

**`internal/db`.**

- `dial.go` and `dial_unsupported.go`: no change to `Dial`/`NewOwner`.
- New `ownerstatus.go` (unix) + `ownerstatus_unsupported.go`: `ProbeOwner(sock
  string) (OwnerStatus, error)` (2 s dial+handshake, reusing `client.Info` with
  the widened `info`); `OwnerStatus` defined in a platform-neutral file so
  `cmd/relevo` can name it on Windows.

**`internal/db/wire/client`.**

- `client.go` `info` gains `PID`/`Conns`; `unsupported.go` matches.
- `session.go` response switches (`ExecContext`, `QueryContext`, `requestNext`,
  `awaitDone`) decode a mid-request `KindRefuse` into the `*wire.Refusal` and
  mark the connection dead, so a drain's `restarting` reaches a real dialled
  client instead of looking like a lost connection.

**`cmd/relevo`.**

- New unix file `owner_serve_unix.go` + `other` stub in package main:
  `checkSocketPath(root)`, `openOwnerListener(root)` (adopt if
  `RELEVO_LISTEN_FD`, else `owner.Listen`), `serveOwner(rt, ln) (*owner.Server,
  error)`, `drainAndHandoff(srv, ln) (fd int, err error)`.
- `daemon.go`: the path check after the lock; `serveOwner` before
  `WriteDaemonInfo`; the `ErrReexec` branch becomes drain → closeDB → stop →
  lock.Close → handoff → `reexec`; a `defer srv.Close()` for every non-reexec
  exit.
- `doctor_checks.go` (+`doctor.go` insert): `ownerCheck(status db.OwnerStatus,
  probeErr error) doctor.Check` and the call in `cmdDoctor` beside
  `databaseCheck`.

**No test should bind unless it means to.** Today the only `cmdDaemon` callers
that reach the listen point are none: `TestDaemonSecondStartLeavesNoDB` loses the
lock, `TestDaemonCheckLeavesNoDB` and `TestDaemonPreflight*` return early.
`internal/e2e` drives `relevo.NewDaemon(...).Tick`, never `Run`; `internal/relevo`'s
`Run` tests stay socket-free because listening is not in `Daemon.Run`;
`internal/serve/daemon.go` only calls `Tick`. Every new socket uses a short
`/tmp/rvo-*` root, never `t.TempDir()`, so the 104-byte `sun_path` holds on macOS
and CI's harness-free, network-free runners are unaffected (unix sockets are
fine).

## 3. Ordered steps

1. Widen the handshake: `Welcome.PID`/`Conns`, `serve()` fills them,
   `client.info` + its non-unix stub match — `go test ./internal/db/wire/...`
   green.
2. Add the owner socket primitives (`SocketPath`, `Listen`, `Adopt`, `Inherit`,
   per-OS limit, 0600, stale unlink) and the `!unix` stubs — `go test
   ./internal/db/wire/owner/` pins stale-socket replacement and the over-limit
   refusal.
3. Add the drain (`beginDrain`/`quiet`/`dropClients`/`Drain`, per-conn `inTx`,
   the `restarting` refusal) — `go test ./internal/db/wire/owner/` pins
   refuse-while-draining, commit-through, and wait-for-the-open-transaction.
4. Surface a mid-request refusal in the wire client — `go test
   ./internal/db/wire/client/` pins `TestDriverSeesARestartingRefusal`.
5. Add `internal/db.OwnerStatus`/`ProbeOwner` (+stub) — `go test ./internal/db/`
   pins the fields against a real owner on `/tmp`.
6. Factor the cmd/relevo lifecycle helpers and wire them into `cmdDaemon` (path
   check after the lock; serve before `daemon.json`; drain+handoff in the
   re-exec branch) — `go test ./cmd/relevo/ -run 'TestDaemon'` green, including
   `TestDaemonRefusesATooLongSocketPath` (names the path and the limit, leaves no
   `relevo.db`).
7. Add the `owner` doctor row (`ownerCheck` + `ProbeOwner`) — `go test
   ./cmd/relevo/ -run TestOwnerCheck` green.
8. Add the re-exec-under-load test in package `owner` (TestMain helper mode; a
   real `syscall.Exec` of the test binary as its own daemon, load clients via
   `wire/client`) — `go test -race -count=1 ./internal/db/wire/owner/ -run
   TestListenerSurvivesARealReexecUnderLoad` green, failures only inside the
   drain window and a connection queued across the exec served by the new image.
9. Mutation checks, one at a time, each reverted and recorded:
   - child unlinks+rebinds instead of adopting → the under-load test fails;
   - `Drain` returns immediately (no flag, no wait) →
     `TestServeRefusesANewTransactionWhileDraining` and
     `TestDrainWaitsForAnOpenTransactionToCommit` fail;
   - **G**: map `restarting` to a plain error → `TestDriverSeesARestartingRefusal`
     and `TestARestartingRefusalIsRetriedOnce` fail;
   - **H**: clear `inTx` on any `COMMIT` (record the effect even when the
     statement fails) → `TestAFailedCommitKeepsTheConnectionInATransaction`
     fails;
   - **E/F**: start `Serve` before the handle is ready, or drop the kept handle
     on a newer schema → the connection has no socket to land on.
10. Full gate and cross checks: `go test -race -count=1 ./internal/db/...
    ./cmd/relevo/`, `make check`, `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go
    build ./...`, `CGO_ENABLED=0 GOOS=darwin go vet ./...` — all green, the
    coverage baseline regenerated with `sh scripts/check-coverage.sh --write`
    and every line kept or raised (never lowered) if a package moved more than a
    point.
11. Save this plan to `docs/plans/2026-09-30-680-1a-daemon-serves.md`, committed
    with the code, `Refs #680` — final `make check` green before the report.

## 4. What is deleted

1. Nothing. This stage adds code and reorders the daemon's own start and re-exec
   steps; no function, flag, test call site, file or behaviour is removed.
   `db.Open`/`OpenWith`/`OpenReadOnly`/`Dial`/`NewOwner` keep their contracts,
   `owner.Server.Serve`/`Close` keep theirs (drain is a third, additive stop),
   `wire.Version` stays 1, and no `.golangci.yml` exclusion,
   `scripts/check-*.allow` entry, `//nolint` or coverage baseline line is added
   or lowered.

## 5. The report must include

- Commands run in order with results: the focused `go test -race -count=1
  ./internal/db/wire/owner/`, `./internal/db/...`, `./cmd/relevo/`, then `make
  check`, the Windows build and the darwin vet.
- The mutation checks: the condition, the exact edit, the named test that
  failed, and that it was reverted.
- The under-load result: the drain window observed, the connection queued across
  the exec and served by the new image, and any edit a test or seam needed to
  pass (or a halt with the failing test named).
- Whether the coverage baseline was regenerated, with the exact `--write`
  command and the lines the new code moved; a statement that no line was
  lowered.
- What is knowingly not done: no client dials (every verb, `mcp`, `wait`, hooks
  and `relevo serve` still open the file directly), no auto-start, no
  `RELEVO_DB_DIRECT`, no serve-unit change, no consolidation of the daemon's
  other handles, no typed endpoints — all 1b.
- The plan-doc path, the commit/PR with `Refs #680`, and any deviation from this
  plan with its reason.

## 6. Flags on the seed (points resolved, not guessed)

1. `wire.Welcome` carries neither pid nor an open-connection count, so the
   doctor row's "pid, open connections" needs additive `Welcome.PID`/`Conns`
   fields plus the client's non-unix stub.
2. "A transaction still open after the drain gets the `restarting` refusal"
   cannot be a pushed frame: the wire is strict request/response and the owner
   cannot send an unsolicited refusal. Resolved as: a request arriving while
   draining on a connection with no open transaction is refused `restarting`; a
   transaction that outlasts the ~5 s window is cut off at exec and its client
   sees a lost connection, never a silent commit.
3. The current re-exec branch closes the DB before anything else; the drain must
   run first or "let open ones commit" is impossible.
4. The seed's "which `*db.DB`" is answered `rt.DB`; the daemon's other four
   handles on the same file stay, so 1a does not yet give the machine a single
   handle per process.
5. Listening cannot live in `internal/relevo.Daemon.Run`: `internal/serve/daemon.go`
   and `internal/relevo`'s own tests run it under `t.TempDir()` roots that would
   exceed `sun_path`. It lives in `cmd/relevo` over primitives in
   `internal/db/wire/owner`.
6. `internal/store` has no socket path; the path and its per-OS limit live in
   `owner` (the package that binds), and `cmd/relevo` calls `owner.SocketPath`
   for the daemon's refusal line and the doctor row.
7. cmd/relevo's `TestMain` unsets every `RELEVO_*` variable, so the
   `RELEVO_LISTEN_FD` handoff is exercised by the `owner` helper (no such
   isolation), not by a cmd/relevo test.