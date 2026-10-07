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
