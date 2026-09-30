# The daemon owns relevo.db

Issue: #680. Status: design approved in conversation 2026-09-29; this document
is the written spec for review. Research behind it:
`docs/specs/probes/2026-09-29-db-owner/` (`usage.md`, `protocol.md`,
`lifecycle.md`).

## 1. Goal

Exactly one process per machine opens `relevo.db`: the daemon. Every other
relevo process -- CLI verbs, `relevo mcp`, `relevo wait`, `relevo ui`, the
plugin hooks, the statusline, `relevo serve` and its admin verbs -- reaches the
database through a local unix socket the daemon serves.

This removes #466's blocker (Turso's default mode rejects a second opener, and
its multi-process mode is experimental) with code relevo controls, and turns the
Turso move back into a single-process driver swap.

Success means:

- one opener per machine, checked by `lsof` on a live session;
- every verb behaves as today from the user's side, including when no daemon is
  running (it is started);
- an upgrade (`make install` → re-exec) drops no request outside a bounded
  drain window;
- the existing `internal/db` and `internal/store` test suites pass unchanged
  when every database is reached through the socket.

## 2. Decisions

| # | Decision | Rejected |
|---|---|---|
| D1 | A SQL-level proxy under `internal/db` now; the socket, handshake and lifecycle are general enough for typed endpoints to join later. Nothing above `internal/db` changes. | Typed store/verb RPC now (184+ methods, streaming verbs); a throwaway Turso-only shim. |
| D2 | When no daemon runs, any client auto-starts it. | Fail with a message; silent direct-open fallback. |
| D3 | The daemon is always the owner, on every host. `relevo serve` is a database client like any verb. | "Whoever holds the lock owns" (a host running both serve and a daemon -- zen -- could not run both); ownership that moves at runtime; a separate `relevo dbd` process. |
| D4 | A small custom protocol: length-prefixed frames over a unix socket. | Hrana (no cancellation, 16 MB client message cap, base64 values, no dialer hook in the deprecated client, the only Go server is unlicensed); rqlite (no `BEGIN IMMEDIATE`); PG/MySQL wire (dialect and error-code translation). |
| D5 | JSON control frames; values as SQLite's five storage classes in binary. | All-binary control frames. |
| D6 | A client over the owner's connection cap waits; it is never refused. | Refusal at the cap. |
| D7 | No automatic fallback to opening the file directly; a hidden `RELEVO_DB_DIRECT=1` escape hatch exists until the Turso swap. | Automatic fallback on a failed dial. |
| D8 | Auto-start also brings back a daemon stopped with `systemctl --user stop`: under D3 the daemon is required. | A "deliberately stopped" state. |

## 3. What exists today (the facts the design rests on)

From `usage.md` and `lifecycle.md`, checked at `8e0b0edd`:

- Every open funnels through `internal/db` (`db.OpenWith`, `db.OpenReadOnly`);
  `modernc.org/sqlite` is imported only there. A plain verb holds 3 handles,
  the daemon at least 4.
- A transaction is a pinned connection running `BEGIN IMMEDIATE` with a 30 s
  busy retry (`internal/db/db.go` `Tx`). `SealRound`, `fork` and `ingest` read
  through the same `*db.DB` while their transaction is open, so a client needs
  several concurrent connections. `ingest` holds its write transaction across
  reads of up to tens of MB from disk.
- The largest single value today is about 4.5 MB (a zstd frame); callers load
  whole result sets into memory, so a single response reaches tens of MB.
- Error identity: `mapBusy` recognises modernc's `*sqlite.Error` code 5 and
  the "database is locked" text; `mapMasterMindKey` code 19; `delivery.retryBusy`
  retries on `db.ErrBusy`.
- The state flock (`Store.WithLock`) is a file lock, not SQL, and is unaffected.
- The statusline may run `relevo status --line` every second; the SessionStart
  hook opens (and on a cold machine migrates) the database; hooks give up after
  2 s.
- The daemon builds its runtime -- opening and migrating the database and
  minting `installation.json` -- *before* `AcquireDaemonLock`. Two daemons
  cold-started together crash 2 times in 6 on a half-written
  `installation.json` (`O_EXCL` create, then write).
- Re-exec closes the database and the lock on purpose before `syscall.Exec`;
  no fd survives.
- `relevo serve` opens the same machine database as the daemon, never takes
  the daemon lock and never re-execs. zen runs both; contabo runs serve only.
- Measured: daemon start-to-lock 54-73 ms; `status` warm 12 ms, cold 35 ms; a
  wedged writer costs a CLI write 30.4 s before `db: tx begin: busy`.

## 4. The seam

`internal/db` gains a second constructor beside `Open`:

- `db.Open(path)` -- opens the file directly. Used by the daemon, and by tests.
- `db.Dial(sock)` -- connects to the owner. Returns the same `*db.DB`.

`Dial` registers a `database/sql` driver that speaks the protocol. relevo's own
`Tx` code (the `BEGIN IMMEDIATE` loop, `COMMIT`, `ROLLBACK`) runs unchanged in
the client and sends its SQL over the wire.

`cmd/relevo`'s `openDB` makes the choice: the daemon calls `Open`; any other
process opening the machine database (`store.DefaultRoot()/relevo.db`) calls
`Dial`, auto-starting the daemon if needed; a database at any other path
(tests, e2e roots) is opened directly.

Tests need a rule of their own: `cmd/relevo`'s `TestMain` points
`XDG_STATE_HOME` at a temp root, so there the temp database *is* the machine
database, and a plain `openDB` would dial and auto-start a real daemon in CI.
`openDB` therefore takes its owner from the runtime rather than from the
environment: a test either opens directly (the default in `cmd/relevo`'s
`TestMain`) or starts an in-process owner on a short socket path and dials it.
No test ever auto-starts a `relevo daemon` process.

`mapBusy` and `mapMasterMindKey` change from matching `*sqlite.Error` to
matching any error with a `Code() int` method, so modernc's errors and the
wire's reconstructed errors map the same way.

## 5. The protocol

**Transport.** `<state root>/relevo.sock`, mode 0600, in the 0700 state root.
The owner checks the peer uid (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED`/`Xucred`
on macOS, via `golang.org/x/sys/unix`). The code is `//go:build unix`; other
platforms get a stub that refuses, as `internal/store/lock_unsupported.go` does.

**Frames.** A 4-byte little-endian length, then the payload. Control payloads
are JSON objects with a `type` and a request `id`:
`hello`, `welcome`, `refuse`, `exec`, `query`, `rows`, `next`, `done`,
`close`, `cancel`, `error`.

**Values.** SQLite's storage classes, one type byte each: NULL, int64, float64,
text, blob; text and blob are length-prefixed raw bytes. No base64, no message
cap. relevo already stores times as text and booleans as integers.

**Rows.** Streamed in batches of about 1 MB; the client asks for the next batch
with `next`. A caller that materialises a whole result set still works, and no
side holds a large result twice.

**Connections.** Each client driver connection pins one owner connection,
opened with today's DSN pragmas (`busy_timeout`, `journal_mode(WAL)`,
`foreign_keys(ON)`, `journal_size_limit`), so per-connection behaviour is
unchanged. The cap counts pinned owner connections; the handshake never waits on
it, and a client over the cap waits. When a client disconnects, the owner cancels
its in-flight request, issues `ROLLBACK`, and closes the pinned connection
rather than returning it to a pool.

**Cancellation.** `cancel{id}` cancels the context of a running request on the
owner, which interrupts the statement where the engine can. Under Turso it
cannot: tursogo v0.8.1 exposes no statement interrupt -- `turso_connection_interrupt`
in the C ABI would lift that -- so a cancel releases the client at once, after a
short grace for the owner's reply, while the statement runs to completion on the
owner. That connection is then discarded rather than served again, because the
client can no longer know the stream's state.

**Errors.** `error{id, code, extended_code, message}`. The client rebuilds an
error carrying `Code()`, so `ErrBusy`, `ErrInvalid` and `retryBusy` work
unchanged. Owner refusals have their own codes: `wrong_proto`,
`shutting_down`, `restarting`.

**Handshake.** The client sends `hello{proto, version, exe_id, schema_know}`.
The owner answers `welcome{proto, min_client, version, schema_have, schema_know,
origin, features}` -- `origin` is the installation id a scoped handle needs and
must not read from the file -- or `refuse{code, message}`. The client answers
`Newer()` by comparing `welcome`'s `schema_have` with its own embedded maximum,
exactly as `db.Open` does, so a client older than the database opens and its
callers decide. `features` is where typed endpoints announce themselves later.

**Owner-only work.** Migrations, minting `installation.json`, the 0600 chmod,
and the one-shot startup passes (dedupe, compression) happen in the daemon.
`VACUUM INTO` and `wal_checkpoint` are SQL and pass through.

## 6. Lifecycle

**Daemon start order.** Load config read-only (today's `--preflight` path) →
`AcquireDaemonLock` → open and migrate → `installation.Load` → listen on
`relevo.sock` → write `daemon.json` → tick. `installation.json` is minted by
writing a temp file and renaming it into place. A daemon that loses the lock
exits "already running" before touching anything. `--check` and `--preflight`
never dial and never auto-start.

**Auto-start.** On a dial failure with no socket or a refused connection, the
client asks the service manager -- `systemctl --user start --no-block relevo`
when the unit is installed, `launchctl kickstart` on macOS -- and otherwise
spawns a detached `relevo daemon` in its own session, logging under the state
root, so the daemon never lives inside a harness session's process scope. It
then re-dials with backoff: 2 s for verbs and hooks, about 0.5 s for the
statusline, which prints nothing when it runs out. Concurrent starters are
harmless: the extra daemons lose the lock and exit.

**Serve hosts.** `relevo serve` and its admin verbs dial like any client.
`dist/relevo-serve.service` gains `Wants=` and `After=relevo.service`; contabo
gains a user `relevo.service` (a deploy change in the servers repo). serve's
own auto-start is a fallback only: a daemon spawned by serve would live in
serve's cgroup and die with it.

**Re-exec.** The daemon finishes its tick, stops admitting new transactions,
waits up to about 5 s for open ones to finish, closes the database and the
lock, clears `FD_CLOEXEC` on the listener, passes it as `RELEVO_LISTEN_FD`, and
execs. The new image adopts the listener with `net.FileListener` -- no unlink,
no rebind -- takes the lock, opens and migrates, and serves. Connections
arriving in the gap wait in the kernel backlog. Existing client connections
close at exec; a request that never reached the owner is retried on a fresh
connection, while one already sent fails with a connection-lost error and is
never retried automatically. A transaction still open after the drain fails with
the retryable `restarting`. Without the fd handoff, queued connections are reset
(measured), which is why the handoff is required, not optional.

**Long-lived clients** (`mcp`, `wait`, `ui`, serve) recover the same way from
an owner crash: bad connection, re-dial, auto-start.

**Version skew.** The owner is authoritative for protocol and schema. A client
below `min_client` gets one line naming the daemon's newer build. A daemon
older than the client -- the window between `make install` and the re-exec --
is retried briefly, then "restart the daemon (relevo doctor)". A newer schema
gives today's `ErrNewerSchema` line.

**Failures.** Dial plus handshake time out at 2 s. Requests have no deadline:
ingest's long transactions are legitimate. A wedged writer still costs the
30 s busy retry, but the error names the holder, which the owner knows: "busy:
write lock held by pid 1234 (relevo daemon ingest) for 28s". stdout stays
clean on every failure. `relevo doctor` gains an owner row: socket, pid,
protocol, open connections.

**Escape hatch.** `RELEVO_DB_DIRECT=1` makes a client open the file directly,
as today. It is safe only while the driver is modernc, is not documented for
users, and is removed with the Turso swap.

## 7. Stages

Each stage is one PR, planned by a lite-planner round, built on a remote
server, and carries its plan in `docs/plans/`.

| Stage | Content | User-visible | Size |
|---|---|---|---|
| 0a | Lock-first daemon start; atomic `installation.json` mint. | Fixes the cold-start crash | S |
| 0b | `internal/db`: protocol, owner server, client driver, `db.Dial`, `Code()`-based error mapping. Nothing calls `Dial` yet. | No | L |
| 1a | The daemon serves `relevo.sock` (uid check), hands the listener across re-exec, drains transactions; `doctor` owner row. Clients still open the file. | No | M |
| 1b | `openDB` dials; auto-start; serve and admin verbs as clients; hook and statusline budgets; `RELEVO_DB_DIRECT`; serve unit `Wants=relevo.service`; contabo daemon deploy. | Yes -- the switch | M-L |
| 2 | #466 removes `RELEVO_DB_DIRECT` with the driver swap. | Out of scope | -- |

0a and 0b are independent. 1a needs 0b; 1b needs 1a. Until 1b merges,
everything is reversible by not merging.

## 8. Verification

- **The suites through the proxy (0b).** A test switch makes `dbtest` reach
  every database through an in-process owner over a socketpair, and the
  `internal/db` and `internal/store` suites run that way in CI. Passing
  unchanged is the transparency proof.
- **Direct-only tests (the Turso swap).** Five tests hold a direct handle even
  when the switch is on, because the operation they exercise is direct-only by
  design: `Vacuum` refuses a handle reached over the wire (`vacuum.go`), the
  per-path handle count tracks direct handles only (`handles.go`), and
  `engineCode` maps an engine's own sentinel before the error is put on the wire
  (`engine_turso.go`). They open through `directOpen`/`directOpenTestDB`
  (`internal/db/helpers_test.go`), the direct opener the hop wraps:
  `TestVacuumKeepsRows`, `TestVacuumShrinksTheFileAndKeepsTheHandleUsable`,
  `TestCompressHistoryOnce`, `TestTwoDirectHandlesOnOnePathAreCounted` and
  `TestEngineCodeMapsARealBusyAndConstraint`. `TestVacuumRefusesADialledHandle`
  pins the refusal itself. This is a listed exception to passing unchanged, not
  a bent test.
- **Protocol tests (0b):** client killed mid-transaction rolls back and the
  `seq` invariants hold; three or more concurrent connections while one is
  pinned in a transaction; `cancel`; a ~5 MB blob and a ~50 MB result set;
  busy and constraint errors reach `ErrBusy`/`ErrInvalid`; each handshake
  refusal; the connection cap waits.
- **Re-exec under load (1a):** clients issue requests continuously while the
  daemon re-execs; zero failures outside the drain window.
- **End to end (1b):** `make e2e` runs a headless round through the socket; a
  `cmd/relevo` test pins auto-start against a fake daemon (no harness, no
  network).
- **Mutation tests** on the load-bearing conditions: connection pinning,
  rollback on disconnect, the fd handoff, lock-before-open.
- **Platform:** test sockets live under a short `/tmp` path (macOS `sun_path`
  is 104 bytes); the Windows stub keeps the cross-compile green; the coverage
  baseline is regenerated for the new package, and the round says so.

## 9. Out of scope

- The Turso driver swap (#466) and cloud sync (#473).
- Typed endpoints on the socket (the handshake leaves room for them).
- A `relevo db query` debugging verb; `sqlite3` works until the swap.
- The remote server/client protocol, beyond `relevo serve` dialling locally.

## 10. Corrections to #680

The issue body predates the research. Where it and this spec disagree, this
spec holds:

- Cold-start races are **not** serialised by `AcquireDaemonLock`: the database
  and `installation.json` are touched before the lock (stage 0a).
- Socket survival across re-exec is a design item, not a checkbox: without fd
  inheritance, queued requests are reset.
- `--check`/`--preflight` never *migrate*; `--check` does create the state root
  and lock file, and both may read an existing database read-only.
- No plugin hook starts or probes the daemon; the SessionStart hook is itself
  a database opener and moves onto the socket in stage 1b.
- The serve-host owner is the daemon (D3), not "serve or the daemon".
