# Cloud sync redesign: a per-origin log, not a replicated file

Issue: #473 (replaces the design in `2026-10-03-turso-sync-design.md`).
Status: approved by the owner 2026-10-07 (decisions in §7). Supersedes PR #1014,
which is closed; the work lands as one new PR from branch `relevo/sync-log`.

## 0. Why the first design failed

Turso sync is file replication: push sends logical row changes, pull sends raw
4 KB pages (`remote_pull_protocol: "pages"`), and a pull rolls local changes
back, lays the remote's pages down, and replays. That is only correct for a
local file that was **born from the remote** by a bootstrap and is written
only through the sync engine's own connections.

PR #1014 adopted each machine's existing `relevo.db` as the sync member and
spent most of its code bridging that gap:

- The remote was seeded by `turso db import` of a `VACUUM INTO` copy or by
  pushing every row, so its page layout never matched the local file. The
  enable skips the pull because it aborts the process (`seedcase.go`); the
  tick pulls anyway (`runner.go`, `attempt`).
- Several engines open one file: the owner's plain pool, a capture pool
  emulating the engine's change capture with a per-connection pragma, the sync
  handle, and dropped handles the driver cannot close. Writes on any other
  connection (client-socket writes, migrations) are in no change set. The
  capture pool skips `foreign_keys = ON`.
- The plain pool checkpoints the WAL itself, invalidating the engine's revert
  point; `watermark.go` and `sidecar.go` then edit and delete the driver's
  private `-info` / `-changes` files to get past the refusals.
- The backfill deletes and re-inserts every live row to manufacture change
  entries, which re-sent an imported remote's rows and produced the FK
  refusals.
- A Rust panic aborts the daemon (purego, no `recover`), and nothing breaks
  the `Restart=on-failure` loop.

Live outcome on 2026-10-07: 50,303 `integrity_check` errors across ten tables
of `relevo.db` and rows lost against the 18:41 backup. The machine was
restored from that backup with sync off; the corrupt file is kept in
`~/.local/state/relevo/tmp/forensic-20261007/`.

## 1. Decisions

- **D1. `relevo.db` never meets a sync engine.** It stays a plain local
  database the daemon owns (#680). No member markers, no capture connection,
  no driver sidecars beside it. A sync bug can at worst stall sync; it cannot
  touch the record.
- **D2. What syncs is a per-origin append-only log.** Every shared row
  already has one owning installation (`SCOPES.md`: `origin` on root tables,
  inherited through ULID parents on children). A machine appends entries only
  for rows it owns and only reads other origins' entries. No two machines ever
  write the same remote row, so there is no conflict rule, no last-push-wins
  loss, and no remote foreign key to refuse anything.
- **D3. Change tracking is SQL triggers into a local outbox.** Migration-made
  `AFTER INSERT/UPDATE/DELETE` triggers on every shared table write
  `(seq, tbl, pk, op)` into `sync_outbox` in `relevo.db`. Triggers fire for
  every connection and every writer, including migrations and client-socket
  writes, so there is no "every write must go through X" rule to keep.
  Verified 2026-10-07 on tursogo v0.8.1: insert, update, `INSERT OR REPLACE`
  and delete each record one outbox row.
- **D4. The driver runs out of process.** A `relevo sync-worker` child owns a
  separate replica file `relevo-sync.db`, created only by a bootstrap from the
  remote and opened only through `TursoSyncDb` / `Connect()`. Nothing else
  opens it; it is never checkpointed, vacuumed, copied or edited by relevo. A
  driver abort kills the worker, never the daemon.
- **D5. Every failure has a breaker.** The worker writes an in-call marker
  before each driver call. The daemon counts consecutive worker deaths, backs
  off exponentially, and after three latches `sync:err` with the cause until
  `relevo db sync retry`. A remote refusal that will repeat latches the same
  way instead of showing as `sync:behind` forever.
- **D6. Joining is bootstrap plus reconcile, for every machine.** A new
  machine, an existing machine, and a second machine with its own history all
  join the same way: bootstrap an empty replica, import other origins, export
  own history. There is no seed matrix, upload path, `--seed-uploaded`, seed
  copy, or "history on both sides" refusal.
- **D7. The remote's schema is the log's, not relevo's.** relevo migrations
  never have to reach the remote (the engine only teaches the remote DDL made
  on a sync connection). Entries carry the writer's `schema_version`.

Rejected: keeping `relevo.db` as the member and routing every connection
through `TursoSyncDb.Connect()`. It fixes the multi-engine problem but still
needs a bootstrapped file, so every existing machine would have to rebuild
its record from the remote, and a driver abort would still take the daemon
and the record's only writer with it.

## 2. The log

Remote tables, created by the worker over a sync connection on first use:

| table | columns | notes |
|---|---|---|
| `log` | `origin, seq, tbl, pk, op, schema_version, body, at` | PK `(origin, seq)`. `op` is `upsert` or `delete`. `body` is the row as JSON (column name to value; BLOBs base64, already zstd per migration 013). |
| `head` | `origin, tbl, pk, seq, hash` | Latest entry per row: the materialized state reconcile compares against. Single writer per origin. |
| `meta` | `key, value` | Log format version. |

- `seq` is assigned by the worker as `max(seq) + 1` for its own origin
  **in the replica**, never taken from the local outbox. A `relevo.db`
  restored from a backup (as on 2026-10-07) therefore never reuses a sequence
  number.
- Commit order is preserved: the outbox is drained in `seq` order, so a
  parent's entry precedes its children's, and an importer applying one
  origin's entries in order with foreign keys on never sees a missing parent.
- Coalescing is allowed: an outbox insert followed by updates exports once,
  with the row's state at export time, at the first position. A row inserted
  and deleted before export exports nothing.
- Compaction (dropping superseded `log` entries below every reader's import
  mark) is out of scope for v1; `head` keeps reconcile cheap meanwhile.

## 3. Components

- **Outbox (R1).** One migration adds `sync_outbox` and the triggers.
  Triggers name the table's primary key columns. For a child row, the owning
  origin is read through its parent in the trigger itself
  (`(SELECT origin FROM binding_record WHERE id = NEW.record_id)`), so a
  delete still knows its origin. A test enumerates `SCOPES.md`'s shared tables
  and asserts each has its three triggers keyed on its current PK, so a later
  migration that changes a shared table cannot drift silently. The outbox
  fills whether sync is on or off. While sync is off, the daemon truncates it
  on a schedule (reconcile covers the gap at enable).
- **Deletes.** Every delete of an owned row propagates, child rows included.
  A child deleted by a cascade may no longer resolve its origin, because the
  parent is already gone; such an outbox row is skipped, since the parent's
  own delete cascades on every importer. A delete the importer finds already
  applied is a no-op.
- **Exporter (R2, daemon).** Drains the outbox in `seq` order. It skips rows
  whose origin is not this installation, which also stops an imported row
  from echoing back. It serializes each row and hands batches to the worker.
- **Importer (R2, daemon).** Applies other origins' entries in `seq` order
  inside one transaction per batch: an upsert on the table's natural key, a
  delete by key (local `ON DELETE CASCADE` does the children). The import mark
  per origin is stored in `relevo.db` itself (`sync_import_mark`), so a
  restore of `relevo.db` rewinds it consistently and re-import is an
  idempotent upsert. Unknown columns are ignored and missing ones take their
  defaults. An entry whose `schema_version` is newer than this binary's holds
  that origin's mark and reports "relevo on this machine is older than
  <label>".
- **Reconcile (R2).** Compares this origin's rows in `relevo.db` against
  `head` by hash and emits an upsert for every difference and a delete for
  every `head` row that is gone locally. It is the initial export at join,
  the repair after a local restore, and an on-demand audit
  (`relevo db sync reconcile --dry-run`). It walks tables in the parents-first
  order `capture_fk.go` already computes; that code moves here.
- **Worker (R3).** `relevo sync-worker` is spawned and supervised by the
  daemon and talks JSON lines over a pipe: `export(batch)`, `pull()` → entries
  newer than the given marks, `stats()`. It owns `relevo-sync.db` and the
  token. Push uses `PushOperationsThreshold`, bootstrap uses
  `PullBytesThreshold`. It holds no relevo database handle at all.
- **Transport seam.** The worker sits behind a `LogTransport` interface (fake
  in every CI test). A second implementation over `relevo serve` is possible
  without touching R1/R2; see §7.

## 3a. Exchange details (R2)

Settled before R2 was planned (2026-10-08):

1. **Package.** The exchange logic lives in a new package
   `internal/synclog` (codec, exporter, importer, reconcile, the
   `LogTransport` interface and its in-memory fake). SQL stays in
   `internal/db`: `synclog` calls `internal/db` methods, it does not open its
   own connections or embed SQL strings that belong to the db package.
2. **Entry.** One log entry is `origin, seq, batch, tbl, pk, op,
   schema_version, body, at`. `batch` is the seq of the first entry of the
   append that wrote it, so a reader can tell where one atomic batch ends.
   `op` is `upsert` or `delete`. `pk` is the same `json_array(...)` text the
   outbox records. `body` is the row as JSON, column name to value, with a
   type-preserving encoding: BLOB values (zstd-compressed transcript and
   round-file bodies, migration 013) must come back as BLOB, integers as
   integers, NULL as NULL. A delete carries no body.
3. **Transport interface.** `LogTransport` has: append this origin's entries
   (the transport assigns `seq` as max(seq for that origin) + 1, so a
   `relevo.db` restored from a backup never reuses a number); read entries of
   other origins after per-origin marks; read `head` (latest seq and body hash
   per row) for one origin; stats. The in-memory fake implements exactly the
   spec §2 semantics, including `head`. R3 will implement it over Turso.
4. **Exporter.** Drains `sync_outbox` in `seq` order and reads the current
   state of every drained row **in the same read transaction**, so one export
   batch is a consistent snapshot: any parent a row's state references is
   either already exported or in this batch. Skips (and deletes) rows whose
   `origin` is NULL or is not this installation: that also stops an imported
   row from echoing back. Coalesces repeated entries for the same `(tbl, pk)`
   within the batch to one entry with the snapshot state: present -> `upsert`
   with body, absent -> `delete`. Orders the batch upserts first in
   `SharedTables` order (parents before children), then deletes in reverse
   order (children before parents), and appends it as one transport call,
   which the transport writes atomically. Deletes drained outbox rows only
   after the transport accepted the batch (at-least-once; re-export is safe
   because import is an idempotent upsert). Column values are exported as
   stored: compressed BLOBs and their `*_codec` columns travel verbatim.
5. **Importer.** Applies other origins' entries in `seq` order, one
   transaction per export batch (never splitting a batch, several whole
   batches may share a transaction), foreign keys ON. Upsert is
   `INSERT ... ON CONFLICT(<pk>) DO UPDATE SET ...` on the table's primary
   key. NEVER `INSERT OR REPLACE`: REPLACE deletes the old row first, and
   `binding_event`, `round_file` and `chain_*` children have `ON DELETE
   CASCADE`, so replacing a parent would silently delete its children. Delete is by
   primary key; a delete already applied is a no-op. Unknown columns in a body
   are ignored, missing ones take the column default. An entry whose
   `schema_version` is newer than this binary's holds that origin's mark (no
   later entry of that origin applies) and is reported, not skipped.
6. **Import marks** live in `relevo.db` in a new local-only table (next free
   migration number, e.g. `sync_import_mark(origin TEXT PRIMARY KEY, seq
   INTEGER NOT NULL)`), updated in the same transaction as the batch it
   covers, so restoring `relevo.db` from a backup rewinds them consistently.
   It is not a shared table: no outbox triggers, not in `SharedTables`.
7. **Reconcile.** For this origin's rows, walking `SharedTables` in order
   (parents first), compare each row's body hash with `head` and emit an
   upsert for every difference or missing row and a delete for every `head`
   row that is gone locally. Chunked and resumable (resuming from `head` is
   enough: a re-run emits only what still differs). Enumerating "this
   origin's rows" for a child table needs the same owner resolution the
   triggers use; put it in `internal/db` beside `SharedTables` so the triggers
   and reconcile cannot drift (a test compares them).
8. **Ordering guarantee.** Each export batch is a consistent snapshot sorted
   parents first for upserts and children first for deletes, and the importer
   applies whole batches in seq order, so a row never arrives before a parent
   it references. Reconcile emits its batches the same way: upserts
   parents-first, deletes children-first.

## 3b. Worker details (R3)

1. **Process.** `relevo sync-worker` is a hidden subcommand. The daemon
   starts it as a child process of its own executable and talks JSON lines
   over the child's stdin and stdout, one request in flight at a time, each
   with an id. The child's stderr goes to the daemon log. The token reaches
   the worker in the first request over the pipe, never in argv or the
   environment.
2. **Ownership.** The worker opens only `relevo-sync.db`, beside `relevo.db`
   in the state directory, through `turso.NewTursoSyncDb` with
   `BootstrapIfEmpty` true, and runs every statement through
   `TursoSyncDb.Connect()`. Its package does not import `internal/db`, and a
   test pins that with `go list -deps`. It never checkpoints, vacuums, copies
   or edits the replica or the driver's files.
3. **Remote schema.** On first use against an empty remote the worker creates
   `log`, `head` and `meta` (spec §2) over a sync connection and pushes, so
   the remote learns them.
4. **Requests.** `hello(version, origin, token, url)`, `export(entries)`:
   one replica transaction assigns `seq = max + 1` for the origin, appends to
   `log`, updates `head`, then `Push`; the reply comes only after the push
   succeeded, so the daemon deletes outbox rows only for entries the remote
   holds. `pull(marks)`: `Pull`, then the entries of other origins past their
   marks. `head(origin, after, limit)`: a page of `head` rows for reconcile.
   `stats`. `shutdown`.
5. **Breaker.** Before each request the daemon writes an in-call marker in
   `relevo-local.db`; it clears it on the reply. A worker that dies or misses
   its deadline is killed and counted, and so is a marker found uncleared at
   daemon start. Backoff doubles from one minute. Three consecutive deaths
   latch `sync:err` with the cause until `relevo db sync retry`. A remote
   refusal classified as permanent latches the same way. A cancelled worker
   is restarted, never reused.
6. **Transport.** The daemon's `synclog.LogTransport` implementation is a
   client of this pipe. CI tests drive it against a fake worker (the test
   binary re-executed in a worker mode that can be told to die, hang or
   refuse); a test against a real Turso database runs only when
   `RELEVO_SCRATCH_SYNC_URL` and `RELEVO_SCRATCH_SYNC_TOKEN` are set.

## 3c. Wiring details (R4)

1. **Enable.** Preflight (origin gate, token, remote URL from the `sync`
   section or `--url`), then start the worker, bootstrap the replica, import
   every other origin, and reconcile-export this origin's history in chunks.
   Progress is a `sync.join` marker in `relevo-local.db`; an interrupted
   enable resumes. The mark goes on only when the join finished.
2. **Steady state.** While on: after a round seals and on the 5-minute idle
   window, drain the outbox, export, pull, import. The runtime wires the
   runner so the idle path runs in production, and a test drives the real
   `Tick` to prove it. Outbox truncation stays off while on.
3. **Disable.** Stop the worker, mark off, delete the token, delete
   `relevo-sync.db` and the driver's files beside it. `relevo.db` keeps every
   imported row.
4. **Status.** `relevo db sync status`, the statusline token and `:sync` show
   on/off, a latched breaker and its cause, the outbox backlog, the last
   export and import times, and any origin held by a newer schema version.
   `relevo db sync retry` clears a latch. `ErrSyncUnavailable` goes away.
5. **CLI tests** stay pure: no harness, no network, no worker process.

## 4. Flows

- **Enable / join.** Preflight (origin gate, token) → worker bootstraps
  `relevo-sync.db` from the remote (empty remote: create the log tables) →
  import every other origin → reconcile-export this origin's history in
  chunks, resumable from `head` → mark on. A join interrupted at any point
  resumes from `head` and the import marks; there is no half-enabled state to
  wedge.
- **Steady state.** After a round seals and on the idle window (wired this
  time: the daemon's runtime assigns the runner, and a test fails if the idle
  path is unreachable): drain outbox → `export` → `pull` → import. Bounded by
  per-step deadlines, and the worker is restarted rather than reused after a
  cancelled call.
- **Leave.** Stop the worker, mark off, delete the token, delete
  `relevo-sync.db` (derived; the remote has it). `relevo.db` is untouched and
  keeps other origins' imported rows as read-only history.
- **Restore from backup.** Restoring `relevo.db` rewinds the import marks
  with it, so re-import is idempotent. The next reconcile re-exports anything
  the remote has that differs.

## 5. What happens to PR #1014

Do not merge it. Close it with a pointer to this spec and salvage from its
branch:

| keep | drop |
|---|---|
| origin backfill, twin/repoint, origin gate | `capture.go`, `writeconn.go`, capture pool and member routing |
| `sync` settings section, `turso.token` in local `secret`, token-never-logged tests | backfill, `Rerecord`, membership markers, `HasSyncMarker` |
| statusline tokens, `:sync` view, verb plumbing over the owner socket | `sidecar.go`, `watermark.go`, `invalidateAndReopen` |
| parents-first table ordering (`capture_fk.go`, moves to reconcile) | seed matrix, seed copy, `--seed-uploaded`, upload path |
| the `relevo-local.db` split (already deployed; harmless, no longer load-bearing) | plain-pool checkpoints of a member file (no member file exists) |
| remote-refusal classification (feeds D5's latch) | driver-version pin rationale tied to the seed open |

`docs/sync-driver-panic.md` stays as the record of what the driver does. The
`relevo-seed` remote is abandoned: its pages came from the old design. The
owner deletes it with Turso tooling; relevo never deletes cloud data.

## 6. Slices and tests

No CI test reaches the network or spawns a harness; `cmd/relevo` tests stay
pure per CLAUDE.md.

- **R0 strip.** Before anything is added, delete §5's drop column from the
  base branch and turn sync into a stub that reports `sync:off`, so later
  slices build on a tree with no capture connection, no member file and no
  driver-file edits. The daemon, cockpit and verbs keep working with sync
  off. Tests: the owner opens `relevo.db` with no capture pool, and no code
  path names `-info`, `-changes` or `turso_cdc`.
- **R1 outbox.** Migration, triggers, drift test over `SCOPES.md`'s tables,
  off-state truncation. Tests: every shared-table write shape (insert, update,
  upsert, delete, cascade) leaves the expected outbox rows; a local-only table
  leaves none.
- **R2 exchange.** Codec, exporter, importer, reconcile against an in-memory
  `LogTransport`. Tests: `TestTwoMachinesShareOneRecord` with two real
  `relevo.db` files and foreign keys on; `TestImportedRowsNeverEcho`;
  `TestRestoreFromBackupConverges` (restore an old copy, sync, compare);
  `TestNewerSchemaHoldsTheMark`; `TestJoinWithHistoryOnBothSides`.
- **R3 worker.** Process, pipe protocol, replica ownership, breaker. Tests: a
  fake worker that exits mid-call leaves the daemon serving and the breaker
  counting; three deaths latch `sync:err`; a cancelled call restarts the
  worker. A scratch-remote test runs only when `RELEVO_SCRATCH_SYNC_URL` is
  set.
- **R4 wiring.** Enable/leave/status/cockpit on the new core. Test:
  idle-window reachability.
- **R5 soak.** Two scratch installations (laptop sandbox and zen) on a scratch
  remote for a week, with `integrity_check` on both `relevo.db` files every
  day, before any live machine enables.

## 7. Owner decisions (2026-10-07)

1. **Transport: Turso with the sync-worker**, as specified here. The
   `LogTransport` seam stays, so `relevo serve` remains possible later.
2. **Bulk content syncs in v1.** Transcript and round-file bodies travel in
   the log like any other row; the first export is chunked and resumable from
   `head`.
3. **All deletes propagate**, child rows included (§3, Deletes).
4. **PR shape:** PR #1014 is closed. Branch `relevo/sync-log` is cut from
   `relevo/turso-live-repair-local` (what the laptop runs, including the
   `relevo-local.db` split), R0 removes the dropped code, and the redesign
   lands as one new PR carrying this spec.

Build: each slice is planned by a `lite-planner` round and run as a
`relevo chain` on zen with `--gate "make check"`; slices land on
`relevo/sync-log` in order R0 to R5.
