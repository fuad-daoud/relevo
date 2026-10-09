# Sync redesign R2: the log exchange

The round plans of this slice of the sync redesign, in the order they ran,
as the builders received them. Spec: `docs/specs/2026-10-07-sync-redesign-design.md`.

---

<!-- r2/round-1.md -->

# R2 round 1 of 6

Slice R2 (exchange) of the sync redesign, branch `relevo/sync-log`. Read the
spec first, especially §3a: `docs/specs/2026-10-07-sync-redesign-design.md`
(head 790c07ef or later). Every round ends with `make check` and
`go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered baseline.

## MasterMind amendments for the whole slice (these override the plan below)

- **Export batches are consistent snapshots (spec §3a.4, §3a.8).** The
  exporter drains the outbox and reads every drained row's current state in
  ONE read transaction. Within the batch it coalesces per `(tbl, pk)` to one
  entry with the snapshot state, then ORDERS the batch: upserts in
  `SharedTables` order (parents first), then deletes in reverse order
  (children first). The plan's "keep the first position" is wrong: a row whose
  latest state points at a parent created later would arrive before that
  parent. Each batch is ONE transport append, written atomically.
- **`Entry.batch`** (spec §3a.2): the seq of the first entry of the append
  that wrote it. The fake transport sets it. The importer applies whole
  batches per transaction and never splits one (several whole batches may
  share a transaction).
- **Column values travel verbatim.** Compressed BLOBs and their `*_codec`
  columns (migration 013) are exported as stored and imported as stored: no
  decompression or recompression anywhere in the exchange.
- **Reconcile** emits upserts parents-first and deletes children-first, as
  batches with the same shape as an export batch.
- **Remote input is untrusted.** An entry's `tbl` must be a `SharedTables`
  name, or the entry is refused and reported. Column names used in SQL come
  only from this machine's schema (`pragma_table_info` or `SharedTables`),
  never from a body's keys; a body key that is not a known column is
  dropped. `pk` is parsed as JSON and bound as parameters, never spliced.
  Values are always bound parameters. Add `TestImporterRefusesUnknownTable`
  and `TestImporterNeverSplicesBodyKeys` (a body key like `x"; DROP TABLE
  binding; --` is ignored and every table survives).
- Round headers below say "R1".."R6"; they mean rounds 1-6 of THIS slice, not
  spec slices.

### Round 1 amendment
The outbox drain method returns the drained outbox rows AND each row's
current state (or absence) from one read transaction; test it by writing
between two drains and showing a drain never mixes states from before and
after a concurrent write.

## Plan for this round

**Goal.** The `internal/db` seam R2 builds on: a local-only import-mark table and the read/drain/mark/owner methods `synclog` calls (it never opens its own connections).

**Files.**

- New `internal/db/migrations/023_sync_import_mark.sql` (next free number; 022 is latest): `sync_import_mark(origin TEXT PRIMARY KEY, seq INTEGER NOT NULL)`, no triggers.
- New `internal/db/sync_exchange.go` (non-test, ≤600 lines; new `Tx`/`DB` methods only): outbox drain in `seq` order, delete-drained, generic row-read by `(tbl, pk)` returning typed columns (consulting the 013 `*_codec` columns for exact bytes), per-origin mark get/set, child-owner resolution mirroring the 022 trigger subqueries.
- Touch `internal/db/migrations/SCOPES.md` machine-local list (one bullet for `sync_import_mark`; shared-history section untouched).
- Tests in `internal/db/` beside `outbox_test.go` / `shared_tables_test.go`.

**Ordered steps.**

1. Land migration 023 with the table and no triggers; `TestImportMarkTableIsLocalOnly` (no `sync_outbox_*` trigger names it; a write records nothing) passes.
2. Add mark get/set on the same `Tx` the importer will use; `TestImportMarkRoundTrip` passes, including update-in-place of one origin's mark.
3. Add outbox drain (ordered rows) + delete-through-seq; `TestOutboxDrainIsOrderedAndDeletable` passes against rows seeded like `outbox_drift_test.go:279-289`.
4. Add generic row-read by `(tbl, pk)` for every `SharedTables` table incl. BLOB round-trip through the codec columns; `TestExchangeRowReadCoversSharedTables` passes.
5. Add child-owner resolution for every inherited table (`binding_event`, `round_file`, `chain_event/member/check`, `round`, `event`, two-hop `artifact`, two-kind `transcript`) mirroring `022_sync_outbox.sql:100-232`; `TestOwnerResolutionMatchesTriggers` compares it against live trigger bodies (pattern: `shared_tables_test.go:46-68`) and passes.
6. Run `make check` then `go build -tags modernc ./...`; both green with no baseline or exclusion change.

**Named tests (each fails if its condition breaks).** `TestImportMarkTableIsLocalOnly`, `TestImportMarkRoundTrip`, `TestOutboxDrainIsOrderedAndDeletable`, `TestExchangeRowReadCoversSharedTables`, `TestOwnerResolutionMatchesTriggers`; existing `TestSyncOutboxDriftCoversSharedTables` and `TestSyncOutboxTruncatesWhileOff` still pass (R1 wiring in `internal/relevo/sync_outbox_test.go` untouched).

**Acceptance.** 023 applies on fresh and upgraded files; marks survive backup-restore with the file (same file, same tx); `internal/db` coverage baseline not lowered.

**Out of scope.** The `synclog` package, exporter/importer/reconcile logic, any `internal/sync`, worker, daemon or `cmd/relevo` change.

## Context: the slice preamble

### Conflicts with the code (flagged, not worked around)

1. **CASCADE claim checks out.** Spec §3a.5 says `binding_event`, `round_file`, `chain_*` children carry `ON DELETE CASCADE`: verified in `internal/db/migrations/003_binding_record.sql:38`, `004_round_file.sql:19`, `016_chains.sql:53`. The `001_initial.sql` FKs (`round`, `event`, `artifact`, `transcript`) deliberately lack it, so an importer deleting a `binding`/`round` parent depends on seq order (children's deletes precede the parent's) — the plan pins that, it does not add cascades.
2. **Op vocabulary is a mapping, not a mismatch.** Outbox rows record `insert/update/delete` (`022_sync_outbox.sql`); log entries carry `upsert/delete` (spec §3a.2). The exporter maps insert/update→upsert; absent row→delete.
3. **Body encoding settled by §3a.** §2 says "BLOBs base64", §3a.2 says type-preserving (BLOB back as BLOB, integers as integers, NULL as NULL). §3a wins; JSON has no blob type so the codec tags BLOB columns, and the `*_codec` columns from migration 013 (`body_codec`, `record_json_codec`, `rendered_codec`) are what reproduce exact bytes.
4. **Zero `ON CONFLICT` precedent.** `internal/db` uses plain `INSERT` and one `INSERT OR REPLACE` (`roundfile.go:34`); `OR REPLACE` is banned for the importer (it would CASCADE-delete children). The R4 probe test is load-bearing: if tursogo v0.8.1 lacks `ON CONFLICT(...) DO UPDATE`, that round halts and reports.
5. **Owner-resolution mirror.** Spec §3a.7 wants owner resolution in `internal/db` beside `SharedTables`; migration 022 already encodes it in immutable SQL (migrations are never edited per `migrations/README.md`), so the new Go mirror is pinned against the trigger bodies (`shared_tables_test.go:46-68` `outboxKeyArgs` pattern), not against a second hand-written copy.
6. **`sync_import_mark` is local-only.** No triggers, not in `SharedTables` (`shared_tables.go:22-38`); `SCOPES.md` gains one machine-local bullet. The drift tests parse only the shared-history section, so this is safe.
7. **Handles for tests.** `d.Origin()` (`split.go:121`) is the exporter's "this installation"; `SchemaVersions()` (`db.go:273`) is the importer's known-version source; both engines default `foreign_keys = ON` (`engine_turso.go:127`, `engine_modernc.go:42`), so two `db.Open` files give the required FK-on fixture.

### Open points (builder decides, plan does not)

Batch/chunk sizes for export, import and reconcile; the body-hash function; the `at` clock source; whether the mem fake needs a mutex (only if tests share one); the older-than label comes from `InstallationList` (`installation.go:26`).

### Rounds, one line each

1. **R1 db seam** — migration 023 `sync_import_mark` plus the `internal/db` read/drain/mark/owner methods `synclog` needs.
2. **R2 package + codec + transport** — `internal/synclog` skeleton, `Entry`, type-preserving codec + hash, `LogTransport` interface + in-memory fake with `head`; coverage baseline `--write`.
3. **R3 exporter** — drain in seq order, skip+delete foreign/NULL origins, coalesce, read-row→upsert/delete, delete drained only after accept.
4. **R4 importer core** — `ON CONFLICT` probe first (halt if absent), ordered apply with FK on, marks in-batch-tx, deletes, parent-update safety.
5. **R5 importer robustness** — newer-schema hold, unknown/missing column tolerance.
6. **R6 reconcile + close** — parents-first hash compare vs `head`, chunked/resumable emit, join-with-history, restore-converges, final acceptance.

### Deleted behaviour

1. None — this slice only adds; no existing behaviour, test, or exclusion entry is removed.

## Report requirements

Commits (new, on the round's branch); focused test command and result; `make check` and `go build -tags modernc ./...` results; the mutation check performed and which named test failed; coverage baseline delta (`--write` stated when used; never a lowered baseline); conflicts C1–C7 reconfirmed or newly found; halt rationale with the failing step if halted (R4 probe) or blocked.

---

<!-- r2/round-2.md -->

# R2 round 2 of 6

Slice R2 (exchange) of the sync redesign, branch `relevo/sync-log`. Read the
spec first, especially §3a: `docs/specs/2026-10-07-sync-redesign-design.md`
(head 790c07ef or later). Every round ends with `make check` and
`go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered baseline.

## MasterMind amendments for the whole slice (these override the plan below)

- **Export batches are consistent snapshots (spec §3a.4, §3a.8).** The
  exporter drains the outbox and reads every drained row's current state in
  ONE read transaction. Within the batch it coalesces per `(tbl, pk)` to one
  entry with the snapshot state, then ORDERS the batch: upserts in
  `SharedTables` order (parents first), then deletes in reverse order
  (children first). The plan's "keep the first position" is wrong: a row whose
  latest state points at a parent created later would arrive before that
  parent. Each batch is ONE transport append, written atomically.
- **`Entry.batch`** (spec §3a.2): the seq of the first entry of the append
  that wrote it. The fake transport sets it. The importer applies whole
  batches per transaction and never splits one (several whole batches may
  share a transaction).
- **Column values travel verbatim.** Compressed BLOBs and their `*_codec`
  columns (migration 013) are exported as stored and imported as stored: no
  decompression or recompression anywhere in the exchange.
- **Reconcile** emits upserts parents-first and deletes children-first, as
  batches with the same shape as an export batch.
- **Remote input is untrusted.** An entry's `tbl` must be a `SharedTables`
  name, or the entry is refused and reported. Column names used in SQL come
  only from this machine's schema (`pragma_table_info` or `SharedTables`),
  never from a body's keys; a body key that is not a known column is
  dropped. `pk` is parsed as JSON and bound as parameters, never spliced.
  Values are always bound parameters. Add `TestImporterRefusesUnknownTable`
  and `TestImporterNeverSplicesBodyKeys` (a body key like `x"; DROP TABLE
  binding; --` is ignored and every table survives).
- Round headers below say "R1".."R6"; they mean rounds 1-6 of THIS slice, not
  spec slices.

### Round 2 amendment
`Entry` includes `batch`. The fake's append is atomic and sets `batch` to the
first assigned seq of that append. Add a fake-semantics test that two appends
produce two distinct `batch` values and pull returns whole batches.

## Plan for this round

**Goal.** `internal/synclog` exists with `Entry`, a type-preserving codec + body hash, the `LogTransport` interface and its in-memory fake implementing exactly spec §2 semantics including `head`.

**Files.**

- New `internal/synclog/` (package comment 1–3 lines per CLAUDE.md): `entry.go` (`Entry{origin, seq, tbl, pk, op, schema_version, body, at}`; `op` upsert/delete; `pk` the outbox `json_array` text; delete carries no body), `codec.go` (row-map ↔ body using the R1 row-read shape: BLOB tagged so it returns as BLOB, integers as integers, NULL as NULL; unknown columns kept in the map for the importer to ignore), `transport.go` (`LogTransport`: append own-origin entries with `seq = max+1` per origin; read other origins after per-origin marks; read `head` latest-seq+hash per row for one origin; stats), `memfake.go` (in-memory fake with identical semantics), `hash.go` (body hash reconcile compares).
- New `internal/synclog/*_test.go`.
- `testdata/coverage-baseline.txt` gains the `internal/synclog` line via `sh scripts/check-coverage.sh --write` (stated in the report).

**Ordered steps.**

1. Add `Entry` + op/pk/body-shape constructors; a table-driven shape test passes showing delete entries carry no body and `pk` parses to the table's key columns from `SharedTables` (`shared_tables.go:22-38`).
2. Add codec row→body→row; `TestSyncCodecBlobRoundTrip` (a `round_file` body with zstd bytes from `encodeColumn`, `codec.go:37-46`, plus a `transcript` rendered column) passes byte-identical.
3. Prove type fidelity; `TestSyncCodecPreservesTypes` (integer stays integer, NULL stays NULL, TEXT stays TEXT) passes.
4. Add `LogTransport` + mem fake with per-origin `max(seq)+1` assignment and `head` maintenance; fake-semantics tests pass (append assigns contiguous seq after restore-like gaps; pull-after-marks returns only newer; `head` reflects latest body hash per row).
5. Add body hash; `TestSyncBodyHashIsStable` (same row → same hash; one-byte body change → different hash) passes.
6. Write the new-package baseline with `sh scripts/check-coverage.sh --write`, then `make check` + `go build -tags modernc ./...` green; no other baseline lowered, no new lint/comment/filesize exclusion.

**Named tests.** `TestSyncCodecBlobRoundTrip`, `TestSyncCodecPreservesTypes`, `TestSyncBodyHashIsStable`, plus fake-semantics tests (fail if seq assignment, mark filtering, or `head` tracking breaks).

**Acceptance.** Package files ≤600 lines, functions ≤70, comments say why; fake matches §2 semantics the R3–R6 tests rely on.

**Out of scope.** Exporter, importer, reconcile, any `internal/db` change beyond what R1 landed.

## Context: the slice preamble

### Conflicts with the code (flagged, not worked around)

1. **CASCADE claim checks out.** Spec §3a.5 says `binding_event`, `round_file`, `chain_*` children carry `ON DELETE CASCADE`: verified in `internal/db/migrations/003_binding_record.sql:38`, `004_round_file.sql:19`, `016_chains.sql:53`. The `001_initial.sql` FKs (`round`, `event`, `artifact`, `transcript`) deliberately lack it, so an importer deleting a `binding`/`round` parent depends on seq order (children's deletes precede the parent's) — the plan pins that, it does not add cascades.
2. **Op vocabulary is a mapping, not a mismatch.** Outbox rows record `insert/update/delete` (`022_sync_outbox.sql`); log entries carry `upsert/delete` (spec §3a.2). The exporter maps insert/update→upsert; absent row→delete.
3. **Body encoding settled by §3a.** §2 says "BLOBs base64", §3a.2 says type-preserving (BLOB back as BLOB, integers as integers, NULL as NULL). §3a wins; JSON has no blob type so the codec tags BLOB columns, and the `*_codec` columns from migration 013 (`body_codec`, `record_json_codec`, `rendered_codec`) are what reproduce exact bytes.
4. **Zero `ON CONFLICT` precedent.** `internal/db` uses plain `INSERT` and one `INSERT OR REPLACE` (`roundfile.go:34`); `OR REPLACE` is banned for the importer (it would CASCADE-delete children). The R4 probe test is load-bearing: if tursogo v0.8.1 lacks `ON CONFLICT(...) DO UPDATE`, that round halts and reports.
5. **Owner-resolution mirror.** Spec §3a.7 wants owner resolution in `internal/db` beside `SharedTables`; migration 022 already encodes it in immutable SQL (migrations are never edited per `migrations/README.md`), so the new Go mirror is pinned against the trigger bodies (`shared_tables_test.go:46-68` `outboxKeyArgs` pattern), not against a second hand-written copy.
6. **`sync_import_mark` is local-only.** No triggers, not in `SharedTables` (`shared_tables.go:22-38`); `SCOPES.md` gains one machine-local bullet. The drift tests parse only the shared-history section, so this is safe.
7. **Handles for tests.** `d.Origin()` (`split.go:121`) is the exporter's "this installation"; `SchemaVersions()` (`db.go:273`) is the importer's known-version source; both engines default `foreign_keys = ON` (`engine_turso.go:127`, `engine_modernc.go:42`), so two `db.Open` files give the required FK-on fixture.

### Open points (builder decides, plan does not)

Batch/chunk sizes for export, import and reconcile; the body-hash function; the `at` clock source; whether the mem fake needs a mutex (only if tests share one); the older-than label comes from `InstallationList` (`installation.go:26`).

### Rounds, one line each

1. **R1 db seam** — migration 023 `sync_import_mark` plus the `internal/db` read/drain/mark/owner methods `synclog` needs.
2. **R2 package + codec + transport** — `internal/synclog` skeleton, `Entry`, type-preserving codec + hash, `LogTransport` interface + in-memory fake with `head`; coverage baseline `--write`.
3. **R3 exporter** — drain in seq order, skip+delete foreign/NULL origins, coalesce, read-row→upsert/delete, delete drained only after accept.
4. **R4 importer core** — `ON CONFLICT` probe first (halt if absent), ordered apply with FK on, marks in-batch-tx, deletes, parent-update safety.
5. **R5 importer robustness** — newer-schema hold, unknown/missing column tolerance.
6. **R6 reconcile + close** — parents-first hash compare vs `head`, chunked/resumable emit, join-with-history, restore-converges, final acceptance.

### Deleted behaviour

1. None — this slice only adds; no existing behaviour, test, or exclusion entry is removed.

## Report requirements

Commits (new, on the round's branch); focused test command and result; `make check` and `go build -tags modernc ./...` results; the mutation check performed and which named test failed; coverage baseline delta (`--write` stated when used; never a lowered baseline); conflicts C1–C7 reconfirmed or newly found; halt rationale with the failing step if halted (R4 probe) or blocked.

---

<!-- r2/round-3.md -->

# R2 round 3 of 6

Slice R2 (exchange) of the sync redesign, branch `relevo/sync-log`. Read the
spec first, especially §3a: `docs/specs/2026-10-07-sync-redesign-design.md`
(head 790c07ef or later). Every round ends with `make check` and
`go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered baseline.

## MasterMind amendments for the whole slice (these override the plan below)

- **Export batches are consistent snapshots (spec §3a.4, §3a.8).** The
  exporter drains the outbox and reads every drained row's current state in
  ONE read transaction. Within the batch it coalesces per `(tbl, pk)` to one
  entry with the snapshot state, then ORDERS the batch: upserts in
  `SharedTables` order (parents first), then deletes in reverse order
  (children first). The plan's "keep the first position" is wrong: a row whose
  latest state points at a parent created later would arrive before that
  parent. Each batch is ONE transport append, written atomically.
- **`Entry.batch`** (spec §3a.2): the seq of the first entry of the append
  that wrote it. The fake transport sets it. The importer applies whole
  batches per transaction and never splits one (several whole batches may
  share a transaction).
- **Column values travel verbatim.** Compressed BLOBs and their `*_codec`
  columns (migration 013) are exported as stored and imported as stored: no
  decompression or recompression anywhere in the exchange.
- **Reconcile** emits upserts parents-first and deletes children-first, as
  batches with the same shape as an export batch.
- **Remote input is untrusted.** An entry's `tbl` must be a `SharedTables`
  name, or the entry is refused and reported. Column names used in SQL come
  only from this machine's schema (`pragma_table_info` or `SharedTables`),
  never from a body's keys; a body key that is not a known column is
  dropped. `pk` is parsed as JSON and bound as parameters, never spliced.
  Values are always bound parameters. Add `TestImporterRefusesUnknownTable`
  and `TestImporterNeverSplicesBodyKeys` (a body key like `x"; DROP TABLE
  binding; --` is ignored and every table survives).
- Round headers below say "R1".."R6"; they mean rounds 1-6 of THIS slice, not
  spec slices.

### Round 3 amendment
Replace step 3's coalescing rule with the snapshot rule above, and add
`TestExporterOrdersBatchParentsFirst`: in one batch, insert a child row, then
insert a new parent and re-point the child to it (and separately delete a
parent's children then the parent); the exported batch lists the parent
upsert before the child upsert and the child delete before the parent delete.
Mutation the MasterMind will run: drop the sort.

## Plan for this round

**Goal.** The exporter: drain the outbox in `seq` order, skip (and delete) foreign/NULL-origin rows so imports never echo, coalesce repeats, read current row state, hand batches to the transport, delete drained rows only after accept.

**Files.**

- New `internal/synclog/export.go` (+ tests): drain via R1 order, skip rule (`origin` NULL or ≠ this installation's `d.Origin()`), coalesce same `(tbl, pk)` keeping first position with state-at-export, present→`upsert` with body / absent→`delete`, transport append, delete-drained-after-accept (at-least-once; re-export safe).
- Fixtures: two real `relevo.db` files via `db.Open` with distinct `Options{Origin}`, FK on by engine default.

**Ordered steps.**

1. Export one owned insert through the fake; assert one `upsert` entry with the row's body lands and the outbox row is gone.
2. Seed foreign-origin and NULL-origin (cascade-child pattern from `outbox_drift_test.go:346-373`) rows; `TestImportedRowsNeverEcho` passes: nothing appended, those outbox rows deleted.
3. Seed insert+updates and insert+delete for one key; coalesce test passes: one entry at the first position with latest state, respectively nothing exported.
4. Make the fake reject a batch; `TestExporterKeepsOutboxUntilAccept` passes: no outbox row deleted, retry exports the same entries.
5. Delete an owned row before export; exporter emits `delete` with no body.
6. `make check` + `go build -tags modernc ./...` green; baselines not lowered.

**Named tests.** `TestImportedRowsNeverEcho`, `TestExporterCoalescesRepeatedEntries`, `TestExporterKeepsOutboxUntilAccept`, `TestExporterEmitsDeleteForAbsentRow`.

**Acceptance.** Outbox op mapping insert/update→upsert pinned; export preserves commit order (parent entries precede children); at-least-once safe.

**Out of scope.** Importer, import marks, reconcile, transport implementations beyond the fake.

## Context: the slice preamble

### Conflicts with the code (flagged, not worked around)

1. **CASCADE claim checks out.** Spec §3a.5 says `binding_event`, `round_file`, `chain_*` children carry `ON DELETE CASCADE`: verified in `internal/db/migrations/003_binding_record.sql:38`, `004_round_file.sql:19`, `016_chains.sql:53`. The `001_initial.sql` FKs (`round`, `event`, `artifact`, `transcript`) deliberately lack it, so an importer deleting a `binding`/`round` parent depends on seq order (children's deletes precede the parent's) — the plan pins that, it does not add cascades.
2. **Op vocabulary is a mapping, not a mismatch.** Outbox rows record `insert/update/delete` (`022_sync_outbox.sql`); log entries carry `upsert/delete` (spec §3a.2). The exporter maps insert/update→upsert; absent row→delete.
3. **Body encoding settled by §3a.** §2 says "BLOBs base64", §3a.2 says type-preserving (BLOB back as BLOB, integers as integers, NULL as NULL). §3a wins; JSON has no blob type so the codec tags BLOB columns, and the `*_codec` columns from migration 013 (`body_codec`, `record_json_codec`, `rendered_codec`) are what reproduce exact bytes.
4. **Zero `ON CONFLICT` precedent.** `internal/db` uses plain `INSERT` and one `INSERT OR REPLACE` (`roundfile.go:34`); `OR REPLACE` is banned for the importer (it would CASCADE-delete children). The R4 probe test is load-bearing: if tursogo v0.8.1 lacks `ON CONFLICT(...) DO UPDATE`, that round halts and reports.
5. **Owner-resolution mirror.** Spec §3a.7 wants owner resolution in `internal/db` beside `SharedTables`; migration 022 already encodes it in immutable SQL (migrations are never edited per `migrations/README.md`), so the new Go mirror is pinned against the trigger bodies (`shared_tables_test.go:46-68` `outboxKeyArgs` pattern), not against a second hand-written copy.
6. **`sync_import_mark` is local-only.** No triggers, not in `SharedTables` (`shared_tables.go:22-38`); `SCOPES.md` gains one machine-local bullet. The drift tests parse only the shared-history section, so this is safe.
7. **Handles for tests.** `d.Origin()` (`split.go:121`) is the exporter's "this installation"; `SchemaVersions()` (`db.go:273`) is the importer's known-version source; both engines default `foreign_keys = ON` (`engine_turso.go:127`, `engine_modernc.go:42`), so two `db.Open` files give the required FK-on fixture.

### Open points (builder decides, plan does not)

Batch/chunk sizes for export, import and reconcile; the body-hash function; the `at` clock source; whether the mem fake needs a mutex (only if tests share one); the older-than label comes from `InstallationList` (`installation.go:26`).

### Rounds, one line each

1. **R1 db seam** — migration 023 `sync_import_mark` plus the `internal/db` read/drain/mark/owner methods `synclog` needs.
2. **R2 package + codec + transport** — `internal/synclog` skeleton, `Entry`, type-preserving codec + hash, `LogTransport` interface + in-memory fake with `head`; coverage baseline `--write`.
3. **R3 exporter** — drain in seq order, skip+delete foreign/NULL origins, coalesce, read-row→upsert/delete, delete drained only after accept.
4. **R4 importer core** — `ON CONFLICT` probe first (halt if absent), ordered apply with FK on, marks in-batch-tx, deletes, parent-update safety.
5. **R5 importer robustness** — newer-schema hold, unknown/missing column tolerance.
6. **R6 reconcile + close** — parents-first hash compare vs `head`, chunked/resumable emit, join-with-history, restore-converges, final acceptance.

### Deleted behaviour

1. None — this slice only adds; no existing behaviour, test, or exclusion entry is removed.

## Report requirements

Commits (new, on the round's branch); focused test command and result; `make check` and `go build -tags modernc ./...` results; the mutation check performed and which named test failed; coverage baseline delta (`--write` stated when used; never a lowered baseline); conflicts C1–C7 reconfirmed or newly found; halt rationale with the failing step if halted (R4 probe) or blocked.

---

<!-- r2/round-4.md -->

# R2 round 4 of 6

Slice R2 (exchange) of the sync redesign, branch `relevo/sync-log`. Read the
spec first, especially §3a: `docs/specs/2026-10-07-sync-redesign-design.md`
(head 790c07ef or later). Every round ends with `make check` and
`go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered baseline.

## MasterMind amendments for the whole slice (these override the plan below)

- **Export batches are consistent snapshots (spec §3a.4, §3a.8).** The
  exporter drains the outbox and reads every drained row's current state in
  ONE read transaction. Within the batch it coalesces per `(tbl, pk)` to one
  entry with the snapshot state, then ORDERS the batch: upserts in
  `SharedTables` order (parents first), then deletes in reverse order
  (children first). The plan's "keep the first position" is wrong: a row whose
  latest state points at a parent created later would arrive before that
  parent. Each batch is ONE transport append, written atomically.
- **`Entry.batch`** (spec §3a.2): the seq of the first entry of the append
  that wrote it. The fake transport sets it. The importer applies whole
  batches per transaction and never splits one (several whole batches may
  share a transaction).
- **Column values travel verbatim.** Compressed BLOBs and their `*_codec`
  columns (migration 013) are exported as stored and imported as stored: no
  decompression or recompression anywhere in the exchange.
- **Reconcile** emits upserts parents-first and deletes children-first, as
  batches with the same shape as an export batch.
- **Remote input is untrusted.** An entry's `tbl` must be a `SharedTables`
  name, or the entry is refused and reported. Column names used in SQL come
  only from this machine's schema (`pragma_table_info` or `SharedTables`),
  never from a body's keys; a body key that is not a known column is
  dropped. `pk` is parsed as JSON and bound as parameters, never spliced.
  Values are always bound parameters. Add `TestImporterRefusesUnknownTable`
  and `TestImporterNeverSplicesBodyKeys` (a body key like `x"; DROP TABLE
  binding; --` is ignored and every table survives).
- Round headers below say "R1".."R6"; they mean rounds 1-6 of THIS slice, not
  spec slices.

### Round 4 amendment
Apply whole batches (by `Entry.batch`) per transaction. Add
`TestImporterAppliesARepointedChildAfterItsNewParent`: export the batch from
the round 3 test and import it into an empty peer with foreign keys on; it
applies without a foreign-key error.

## Plan for this round

**Goal.** The importer core: ordered, FK-on, transactional apply of other origins' entries with per-batch marks — gated first by a probe proving tursogo v0.8.1 supports the upsert the design depends on.

**Files.**

- New `internal/synclog/import.go` (+ tests): per-origin `seq`-ordered apply, one transaction per batch, FK on; upsert as `INSERT ... ON CONFLICT(<pk cols>) DO UPDATE SET ...` on the table's `PrimaryKey` (never `INSERT OR REPLACE`); delete by key, already-applied delete is a no-op; `sync_import_mark` updated in the same transaction as its batch.
- Fixtures: two real `relevo.db` files (`db.Open`, distinct origins), foreign keys asserted on (pattern `outbox_drift_test.go:250-261`).

**Ordered steps.**

1. Add the probe `TestLogRequiresOnConflictUpsert` (real `relevo.db`, `INSERT ... ON CONFLICT(id) DO UPDATE` on a scratch table); if it fails, **halt the round and report** — no importer code lands on an engine that cannot do the one safe upsert.
2. With the probe green, apply one origin's mixed entries (parents before children per `SharedTables` order) into an empty peer; `TestTwoMachinesShareOneRecord` passes: every row present on both, FK check clean.
3. Delete a `binding_record` root on the exporter side, export, import; `TestImporterRootDeleteCascades` passes: the row and its `binding_event`/`round_file` children are gone on the importer, marks advanced.
4. Delete one child row only (`round_file` / `chain_member`), export, import; `TestImporterChildOnlyDelete` passes: only that row gone, parent and siblings intact.
5. Update a parent row (`binding` label/`cwd`) with children present, export, import; `TestImporterParentUpdateKeepsChildren` passes: parent updated, `round`/`event` children untouched (this is the test that catches an `OR REPLACE` regression — mutating it to REPLACE must fail it).
6. `make check` + `go build -tags modernc ./...` green; baselines not lowered.

**Named tests.** `TestLogRequiresOnConflictUpsert` (gate), `TestTwoMachinesShareOneRecord`, `TestImporterRootDeleteCascades`, `TestImporterChildOnlyDelete`, `TestImporterParentUpdateKeepsChildren`.

**Acceptance.** Import is idempotent (re-applying a batch changes nothing); marks move only with their batch; no `OR REPLACE` string appears in non-test code.

**Out of scope.** Newer-schema hold, unknown/missing column tolerance, reconcile, exporter changes.

## Context: the slice preamble

### Conflicts with the code (flagged, not worked around)

1. **CASCADE claim checks out.** Spec §3a.5 says `binding_event`, `round_file`, `chain_*` children carry `ON DELETE CASCADE`: verified in `internal/db/migrations/003_binding_record.sql:38`, `004_round_file.sql:19`, `016_chains.sql:53`. The `001_initial.sql` FKs (`round`, `event`, `artifact`, `transcript`) deliberately lack it, so an importer deleting a `binding`/`round` parent depends on seq order (children's deletes precede the parent's) — the plan pins that, it does not add cascades.
2. **Op vocabulary is a mapping, not a mismatch.** Outbox rows record `insert/update/delete` (`022_sync_outbox.sql`); log entries carry `upsert/delete` (spec §3a.2). The exporter maps insert/update→upsert; absent row→delete.
3. **Body encoding settled by §3a.** §2 says "BLOBs base64", §3a.2 says type-preserving (BLOB back as BLOB, integers as integers, NULL as NULL). §3a wins; JSON has no blob type so the codec tags BLOB columns, and the `*_codec` columns from migration 013 (`body_codec`, `record_json_codec`, `rendered_codec`) are what reproduce exact bytes.
4. **Zero `ON CONFLICT` precedent.** `internal/db` uses plain `INSERT` and one `INSERT OR REPLACE` (`roundfile.go:34`); `OR REPLACE` is banned for the importer (it would CASCADE-delete children). The R4 probe test is load-bearing: if tursogo v0.8.1 lacks `ON CONFLICT(...) DO UPDATE`, that round halts and reports.
5. **Owner-resolution mirror.** Spec §3a.7 wants owner resolution in `internal/db` beside `SharedTables`; migration 022 already encodes it in immutable SQL (migrations are never edited per `migrations/README.md`), so the new Go mirror is pinned against the trigger bodies (`shared_tables_test.go:46-68` `outboxKeyArgs` pattern), not against a second hand-written copy.
6. **`sync_import_mark` is local-only.** No triggers, not in `SharedTables` (`shared_tables.go:22-38`); `SCOPES.md` gains one machine-local bullet. The drift tests parse only the shared-history section, so this is safe.
7. **Handles for tests.** `d.Origin()` (`split.go:121`) is the exporter's "this installation"; `SchemaVersions()` (`db.go:273`) is the importer's known-version source; both engines default `foreign_keys = ON` (`engine_turso.go:127`, `engine_modernc.go:42`), so two `db.Open` files give the required FK-on fixture.

### Open points (builder decides, plan does not)

Batch/chunk sizes for export, import and reconcile; the body-hash function; the `at` clock source; whether the mem fake needs a mutex (only if tests share one); the older-than label comes from `InstallationList` (`installation.go:26`).

### Rounds, one line each

1. **R1 db seam** — migration 023 `sync_import_mark` plus the `internal/db` read/drain/mark/owner methods `synclog` needs.
2. **R2 package + codec + transport** — `internal/synclog` skeleton, `Entry`, type-preserving codec + hash, `LogTransport` interface + in-memory fake with `head`; coverage baseline `--write`.
3. **R3 exporter** — drain in seq order, skip+delete foreign/NULL origins, coalesce, read-row→upsert/delete, delete drained only after accept.
4. **R4 importer core** — `ON CONFLICT` probe first (halt if absent), ordered apply with FK on, marks in-batch-tx, deletes, parent-update safety.
5. **R5 importer robustness** — newer-schema hold, unknown/missing column tolerance.
6. **R6 reconcile + close** — parents-first hash compare vs `head`, chunked/resumable emit, join-with-history, restore-converges, final acceptance.

### Deleted behaviour

1. None — this slice only adds; no existing behaviour, test, or exclusion entry is removed.

## Report requirements

Commits (new, on the round's branch); focused test command and result; `make check` and `go build -tags modernc ./...` results; the mutation check performed and which named test failed; coverage baseline delta (`--write` stated when used; never a lowered baseline); conflicts C1–C7 reconfirmed or newly found; halt rationale with the failing step if halted (R4 probe) or blocked.

---

<!-- r2/round-5.md -->

# R2 round 5 of 6

Slice R2 (exchange) of the sync redesign, branch `relevo/sync-log`. Read the
spec first, especially §3a: `docs/specs/2026-10-07-sync-redesign-design.md`
(head 790c07ef or later). Every round ends with `make check` and
`go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered baseline.

## MasterMind amendments for the whole slice (these override the plan below)

- **Export batches are consistent snapshots (spec §3a.4, §3a.8).** The
  exporter drains the outbox and reads every drained row's current state in
  ONE read transaction. Within the batch it coalesces per `(tbl, pk)` to one
  entry with the snapshot state, then ORDERS the batch: upserts in
  `SharedTables` order (parents first), then deletes in reverse order
  (children first). The plan's "keep the first position" is wrong: a row whose
  latest state points at a parent created later would arrive before that
  parent. Each batch is ONE transport append, written atomically.
- **`Entry.batch`** (spec §3a.2): the seq of the first entry of the append
  that wrote it. The fake transport sets it. The importer applies whole
  batches per transaction and never splits one (several whole batches may
  share a transaction).
- **Column values travel verbatim.** Compressed BLOBs and their `*_codec`
  columns (migration 013) are exported as stored and imported as stored: no
  decompression or recompression anywhere in the exchange.
- **Reconcile** emits upserts parents-first and deletes children-first, as
  batches with the same shape as an export batch.
- **Remote input is untrusted.** An entry's `tbl` must be a `SharedTables`
  name, or the entry is refused and reported. Column names used in SQL come
  only from this machine's schema (`pragma_table_info` or `SharedTables`),
  never from a body's keys; a body key that is not a known column is
  dropped. `pk` is parsed as JSON and bound as parameters, never spliced.
  Values are always bound parameters. Add `TestImporterRefusesUnknownTable`
  and `TestImporterNeverSplicesBodyKeys` (a body key like `x"; DROP TABLE
  binding; --` is ignored and every table survives).
- Round headers below say "R1".."R6"; they mean rounds 1-6 of THIS slice, not
  spec slices.


## Plan for this round

**Goal.** Importer robustness: a newer-writer entry holds its origin's mark and reports instead of applying, and column drift in either direction is absorbed.

**Files.**

- `internal/synclog/import.go` (same file, small deltas): per-entry `schema_version` comparison against the binary's known version (`SchemaVersions`, `db.go:273`); hold rule (entry newer → stop that origin at the previous seq, report `relevo on this machine is older than <label>` with the label from `InstallationList`); upsert projecting only known columns, missing columns taking defaults.
- Tests in `internal/synclog/`.

**Ordered steps.**

1. Import a batch whose middle entry carries a higher `schema_version`; `TestNewerSchemaHoldsTheMark` passes: rows before it applied, mark rests at the previous seq, no later entry of that origin applied, report names the writer's label.
2. Follow with a same-version entry after a binary "upgrade" (known version raised); held entries then apply and the mark advances.
3. Import a body with an extra unknown column; it applies with the column ignored.
4. Import a body missing a defaulted column; it applies with the column default.
5. `make check` + `go build -tags modernc ./...` green; baselines not lowered.

**Named tests.** `TestNewerSchemaHoldsTheMark`, `TestImporterIgnoresUnknownColumns`, `TestImporterDefaultsMissingColumns`.

**Acceptance.** A newer writer can never wedge an older machine: the hold is per-origin, later entries wait, and the message names the installation.

**Out of scope.** Reconcile, join flow, exporter changes, worker/daemon wiring.

## Context: the slice preamble

### Conflicts with the code (flagged, not worked around)

1. **CASCADE claim checks out.** Spec §3a.5 says `binding_event`, `round_file`, `chain_*` children carry `ON DELETE CASCADE`: verified in `internal/db/migrations/003_binding_record.sql:38`, `004_round_file.sql:19`, `016_chains.sql:53`. The `001_initial.sql` FKs (`round`, `event`, `artifact`, `transcript`) deliberately lack it, so an importer deleting a `binding`/`round` parent depends on seq order (children's deletes precede the parent's) — the plan pins that, it does not add cascades.
2. **Op vocabulary is a mapping, not a mismatch.** Outbox rows record `insert/update/delete` (`022_sync_outbox.sql`); log entries carry `upsert/delete` (spec §3a.2). The exporter maps insert/update→upsert; absent row→delete.
3. **Body encoding settled by §3a.** §2 says "BLOBs base64", §3a.2 says type-preserving (BLOB back as BLOB, integers as integers, NULL as NULL). §3a wins; JSON has no blob type so the codec tags BLOB columns, and the `*_codec` columns from migration 013 (`body_codec`, `record_json_codec`, `rendered_codec`) are what reproduce exact bytes.
4. **Zero `ON CONFLICT` precedent.** `internal/db` uses plain `INSERT` and one `INSERT OR REPLACE` (`roundfile.go:34`); `OR REPLACE` is banned for the importer (it would CASCADE-delete children). The R4 probe test is load-bearing: if tursogo v0.8.1 lacks `ON CONFLICT(...) DO UPDATE`, that round halts and reports.
5. **Owner-resolution mirror.** Spec §3a.7 wants owner resolution in `internal/db` beside `SharedTables`; migration 022 already encodes it in immutable SQL (migrations are never edited per `migrations/README.md`), so the new Go mirror is pinned against the trigger bodies (`shared_tables_test.go:46-68` `outboxKeyArgs` pattern), not against a second hand-written copy.
6. **`sync_import_mark` is local-only.** No triggers, not in `SharedTables` (`shared_tables.go:22-38`); `SCOPES.md` gains one machine-local bullet. The drift tests parse only the shared-history section, so this is safe.
7. **Handles for tests.** `d.Origin()` (`split.go:121`) is the exporter's "this installation"; `SchemaVersions()` (`db.go:273`) is the importer's known-version source; both engines default `foreign_keys = ON` (`engine_turso.go:127`, `engine_modernc.go:42`), so two `db.Open` files give the required FK-on fixture.

### Open points (builder decides, plan does not)

Batch/chunk sizes for export, import and reconcile; the body-hash function; the `at` clock source; whether the mem fake needs a mutex (only if tests share one); the older-than label comes from `InstallationList` (`installation.go:26`).

### Rounds, one line each

1. **R1 db seam** — migration 023 `sync_import_mark` plus the `internal/db` read/drain/mark/owner methods `synclog` needs.
2. **R2 package + codec + transport** — `internal/synclog` skeleton, `Entry`, type-preserving codec + hash, `LogTransport` interface + in-memory fake with `head`; coverage baseline `--write`.
3. **R3 exporter** — drain in seq order, skip+delete foreign/NULL origins, coalesce, read-row→upsert/delete, delete drained only after accept.
4. **R4 importer core** — `ON CONFLICT` probe first (halt if absent), ordered apply with FK on, marks in-batch-tx, deletes, parent-update safety.
5. **R5 importer robustness** — newer-schema hold, unknown/missing column tolerance.
6. **R6 reconcile + close** — parents-first hash compare vs `head`, chunked/resumable emit, join-with-history, restore-converges, final acceptance.

### Deleted behaviour

1. None — this slice only adds; no existing behaviour, test, or exclusion entry is removed.

## Report requirements

Commits (new, on the round's branch); focused test command and result; `make check` and `go build -tags modernc ./...` results; the mutation check performed and which named test failed; coverage baseline delta (`--write` stated when used; never a lowered baseline); conflicts C1–C7 reconfirmed or newly found; halt rationale with the failing step if halted (R4 probe) or blocked.

---

<!-- r2/round-6.md -->

# R2 round 6 of 6

Slice R2 (exchange) of the sync redesign, branch `relevo/sync-log`. Read the
spec first, especially §3a: `docs/specs/2026-10-07-sync-redesign-design.md`
(head 790c07ef or later). Every round ends with `make check` and
`go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered baseline.

## MasterMind amendments for the whole slice (these override the plan below)

- **Export batches are consistent snapshots (spec §3a.4, §3a.8).** The
  exporter drains the outbox and reads every drained row's current state in
  ONE read transaction. Within the batch it coalesces per `(tbl, pk)` to one
  entry with the snapshot state, then ORDERS the batch: upserts in
  `SharedTables` order (parents first), then deletes in reverse order
  (children first). The plan's "keep the first position" is wrong: a row whose
  latest state points at a parent created later would arrive before that
  parent. Each batch is ONE transport append, written atomically.
- **`Entry.batch`** (spec §3a.2): the seq of the first entry of the append
  that wrote it. The fake transport sets it. The importer applies whole
  batches per transaction and never splits one (several whole batches may
  share a transaction).
- **Column values travel verbatim.** Compressed BLOBs and their `*_codec`
  columns (migration 013) are exported as stored and imported as stored: no
  decompression or recompression anywhere in the exchange.
- **Reconcile** emits upserts parents-first and deletes children-first, as
  batches with the same shape as an export batch.
- **Remote input is untrusted.** An entry's `tbl` must be a `SharedTables`
  name, or the entry is refused and reported. Column names used in SQL come
  only from this machine's schema (`pragma_table_info` or `SharedTables`),
  never from a body's keys; a body key that is not a known column is
  dropped. `pk` is parsed as JSON and bound as parameters, never spliced.
  Values are always bound parameters. Add `TestImporterRefusesUnknownTable`
  and `TestImporterNeverSplicesBodyKeys` (a body key like `x"; DROP TABLE
  binding; --` is ignored and every table survives).
- Round headers below say "R1".."R6"; they mean rounds 1-6 of THIS slice, not
  spec slices.

### Round 6 amendment
Reconcile batches follow the export batch shape (upserts parents-first,
deletes children-first) and are appended one batch per transport call.

## Plan for this round

**Goal.** Reconcile plus the two convergence proofs: join-with-history-on-both-sides and restore-from-backup, then final acceptance of the slice.

**Files.**

- New `internal/synclog/reconcile.go` (+ tests): walk `SharedTables` parents-first (`shared_tables.go:22-38`), compare each owned row's body hash with the transport's `head` for this origin, emit `upsert` for differences/missing rows and `delete` for `head` rows gone locally; chunked and resumable (re-run emits only what still differs; resuming from `head` suffices).
- Join-convergence test driving exporter + importer + reconcile across two files and the fake; restore test via file copy of `relevo.db` (marks rewind with the file since they live in it).

**Ordered steps.**

1. Reconcile an empty `head` against a populated file; every owned row emits once in parents-first order and a second run emits nothing (`TestReconcileEmitsOnlyDifferences` passes).
2. Delete a local row and add one after a full reconcile; next run emits exactly one `delete` and one `upsert`.
3. Both machines hold disjoint pre-join history, fake starts empty: import-others then reconcile-export-own on each; `TestJoinWithHistoryOnBothSides` passes with both files converged and FK checks clean.
4. Snapshot machine B's file, advance B, restore the snapshot over B's file, sync; `TestRestoreFromBackupConverges` passes: re-import is idempotent, reconcile re-exports what differs, both files converge (this also proves no outbox `seq` reuse matters — transport assigns `seq`).
5. Mutation pass: break each round's key condition (echo a foreign row, REPLACE a parent, skip the mark-hold, drop parents-first order) and confirm the named test fails.
6. Final `make check` + `go build -tags modernc ./...` green; coverage: `internal/synclog` baseline met, `internal/db` baseline not lowered (regenerate with `--write` only if code moved packages, and say so).

**Named tests.** `TestReconcileEmitsOnlyDifferences`, `TestJoinWithHistoryOnBothSides`, `TestRestoreFromBackupConverges`.

**Acceptance.** All nine required tests exist and pass: `TestTwoMachinesShareOneRecord`, `TestImportedRowsNeverEcho`, `TestRestoreFromBackupConverges`, `TestNewerSchemaHoldsTheMark`, `TestJoinWithHistoryOnBothSides`, root-cascade + child-only delete propagation, parent-update-keeps-children, BLOB round trip. Slice touches only `internal/db` (R1 seam) and `internal/synclog`; no network, worker, daemon wiring, enable/disable, or `cmd/relevo` changes; compaction explicitly not built (spec v1 out of scope).

**Out of scope.** R3 worker, R4 wiring, R5 soak, compaction.

## Context: the slice preamble

### Conflicts with the code (flagged, not worked around)

1. **CASCADE claim checks out.** Spec §3a.5 says `binding_event`, `round_file`, `chain_*` children carry `ON DELETE CASCADE`: verified in `internal/db/migrations/003_binding_record.sql:38`, `004_round_file.sql:19`, `016_chains.sql:53`. The `001_initial.sql` FKs (`round`, `event`, `artifact`, `transcript`) deliberately lack it, so an importer deleting a `binding`/`round` parent depends on seq order (children's deletes precede the parent's) — the plan pins that, it does not add cascades.
2. **Op vocabulary is a mapping, not a mismatch.** Outbox rows record `insert/update/delete` (`022_sync_outbox.sql`); log entries carry `upsert/delete` (spec §3a.2). The exporter maps insert/update→upsert; absent row→delete.
3. **Body encoding settled by §3a.** §2 says "BLOBs base64", §3a.2 says type-preserving (BLOB back as BLOB, integers as integers, NULL as NULL). §3a wins; JSON has no blob type so the codec tags BLOB columns, and the `*_codec` columns from migration 013 (`body_codec`, `record_json_codec`, `rendered_codec`) are what reproduce exact bytes.
4. **Zero `ON CONFLICT` precedent.** `internal/db` uses plain `INSERT` and one `INSERT OR REPLACE` (`roundfile.go:34`); `OR REPLACE` is banned for the importer (it would CASCADE-delete children). The R4 probe test is load-bearing: if tursogo v0.8.1 lacks `ON CONFLICT(...) DO UPDATE`, that round halts and reports.
5. **Owner-resolution mirror.** Spec §3a.7 wants owner resolution in `internal/db` beside `SharedTables`; migration 022 already encodes it in immutable SQL (migrations are never edited per `migrations/README.md`), so the new Go mirror is pinned against the trigger bodies (`shared_tables_test.go:46-68` `outboxKeyArgs` pattern), not against a second hand-written copy.
6. **`sync_import_mark` is local-only.** No triggers, not in `SharedTables` (`shared_tables.go:22-38`); `SCOPES.md` gains one machine-local bullet. The drift tests parse only the shared-history section, so this is safe.
7. **Handles for tests.** `d.Origin()` (`split.go:121`) is the exporter's "this installation"; `SchemaVersions()` (`db.go:273`) is the importer's known-version source; both engines default `foreign_keys = ON` (`engine_turso.go:127`, `engine_modernc.go:42`), so two `db.Open` files give the required FK-on fixture.

### Open points (builder decides, plan does not)

Batch/chunk sizes for export, import and reconcile; the body-hash function; the `at` clock source; whether the mem fake needs a mutex (only if tests share one); the older-than label comes from `InstallationList` (`installation.go:26`).

### Rounds, one line each

1. **R1 db seam** — migration 023 `sync_import_mark` plus the `internal/db` read/drain/mark/owner methods `synclog` needs.
2. **R2 package + codec + transport** — `internal/synclog` skeleton, `Entry`, type-preserving codec + hash, `LogTransport` interface + in-memory fake with `head`; coverage baseline `--write`.
3. **R3 exporter** — drain in seq order, skip+delete foreign/NULL origins, coalesce, read-row→upsert/delete, delete drained only after accept.
4. **R4 importer core** — `ON CONFLICT` probe first (halt if absent), ordered apply with FK on, marks in-batch-tx, deletes, parent-update safety.
5. **R5 importer robustness** — newer-schema hold, unknown/missing column tolerance.
6. **R6 reconcile + close** — parents-first hash compare vs `head`, chunked/resumable emit, join-with-history, restore-converges, final acceptance.

### Deleted behaviour

1. None — this slice only adds; no existing behaviour, test, or exclusion entry is removed.

## Report requirements

Commits (new, on the round's branch); focused test command and result; `make check` and `go build -tags modernc ./...` results; the mutation check performed and which named test failed; coverage baseline delta (`--write` stated when used; never a lowered baseline); conflicts C1–C7 reconfirmed or newly found; halt rationale with the failing step if halted (R4 probe) or blocked.

---

<!-- r2fix/common.md -->

Slice R2 follow-up of the sync redesign, branch `relevo/sync-log` with R2
(`internal/synclog`, `internal/db/sync_*.go`) already on it. Read spec §3a:
`docs/specs/2026-10-07-sync-redesign-design.md`. The MasterMind verified R2 by
mutation testing and found the gaps below. Every round ends with `make check`
and `go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline. Name the test that fails for each condition and report the
mutation you ran against it (break the condition, see the test fail, revert).

---

<!-- r2fix/round-1.md -->

# R2 follow-up round 1 of 3: the drain is one snapshot and closes over pending parents

Slice R2 follow-up of the sync redesign, branch `relevo/sync-log` with R2
(`internal/synclog`, `internal/db/sync_*.go`) already on it. Read spec §3a:
`docs/specs/2026-10-07-sync-redesign-design.md`. The MasterMind verified R2 by
mutation testing and found the gaps below. Every round ends with `make check`
and `go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline. Name the test that fails for each condition and report the
mutation you ran against it (break the condition, see the test fail, revert).

## 1. Pin the drain wrapper to one transaction

`TestOutboxDrainIsOneSnapshot` holds its own `d.Tx` and calls
`tx.DrainOutbox` inside it, so it pins the transaction, not the wrapper.
Proven: rewriting `DB.DrainOutbox` to read the entries in one `d.Tx` and the
rows in a second one leaves every test green, and the exporter calls
`e.db.DrainOutbox(...)`. Make the guarantee unbreakable in the exporter's own
path: either the exporter drains inside its own `d.Tx` (and `DB.DrainOutbox`
goes away), or add a test that drives `DB.DrainOutbox` itself against a
competing writer. Mutation to report: the two-transaction rewrite above must
fail a named test.

## 2. Close the drain over pending parents

`DrainOutbox(limit)` takes the first `limit` outbox rows and each row's
current state. If a drained row's current state references a parent whose own
outbox entries all lie beyond the window (a child re-pointed to a parent
created later), the batch carries the child without the parent, the importer's
foreign-key check fails, and that origin's import blocks on that batch for
good. Fix: within the drain's snapshot, for every drained row present, follow
its foreign keys (`pragma_foreign_key_list`, shared tables only); a parent that
has outbox entries with `seq` beyond the window is pulled into the batch with
its snapshot state, recursively. Its own later outbox entries stay where they
are and are exported again in their own batch, which is harmless because
import is an idempotent upsert; the exporter's seq cut does not change. Test `TestDrainCarriesAPendingParentPastTheWindow`: seed parent P1 and child
C pointing at P1, export everything so the outbox is empty. Then, in order:
update an unrelated column of C (outbox entry a), insert parent P2 (entry b),
re-point C to P2 (entry c). Drain with a window of 1: it takes entry a, whose
snapshot state of C already points at P2, while P2's entry b lies beyond the
window. Export that batch and import it into an empty peer that already holds
P1 and C, with foreign keys on: it must apply without a foreign-key error, and
P2 must be present on the peer. Mutation to report: disable the
closure and the test fails.

## 3. Ownership of child rows after the write

The importer's ownership gate checks the owner of a row that already exists
here and forces the owner column of root rows to the entry's origin. A child
row (no owner column of its own; it resolves through its parent) is not
checked when it is new, and a re-pointing update is checked only against its
old parent. So an entry from origin A can insert a `binding_event` into origin
B's `binding_record`, or move its own child under B's parent, and the row then
belongs to B's history. Fix: after the upsert, inside the same transaction,
resolve the row's owner with `ResolveOwner` and refuse (as an invalid entry, so
the batch is dropped and reported per the drop-and-continue rule) unless it
equals the entry's origin. Tests: `TestImportRefusesAChildUnderAnotherOrigin`
(new child under B's parent from A is refused and absent afterwards) and
`TestImportRefusesRepointingAChildToAnotherOrigin`. Mutation to report: drop
the post-write check and both fail.

## 4. Marks advance only by contiguous batches

After the security fix, `mark()` is forward-only but `past()` (the mark move
for a dropped batch) writes the batch tail unconditionally, and nothing checks
contiguity. A fabricated batch starting at seq 1_000_000 -- dropped as invalid
or applied as valid -- moves the origin's mark to 1_000_000, and every genuine
entry below it is skipped forever. Sequence numbers per origin are contiguous
by construction (the transport assigns max + 1 per append), so: a batch is
applied or dropped only when its first seq is exactly the origin's mark + 1
(1 when the origin has no mark); a batch that starts later is a gap -- the
origin holds for the run and the gap is reported, nothing is skipped; a batch
at or below the mark is a replay and is ignored; `past()` is forward-only like
`mark()`. Tests: `TestImportHoldsOnAGapInsteadOfSkipping` (a forged batch at
seq 1_000_000 moves no mark, later genuine batches still apply) and
`TestImportNeverRewindsAMarkWhenDropping`. Mutations to report: removing the
contiguity check, and making `past()` unconditional, each fail a named test.

---

<!-- r2fix/round-2.md -->

# R2 follow-up round 2 of 3: reconcile pages, keeps a cursor, reads one snapshot per chunk

Slice R2 follow-up of the sync redesign, branch `relevo/sync-log` with R2
(`internal/synclog`, `internal/db/sync_*.go`) already on it. Read spec §3a:
`docs/specs/2026-10-07-sync-redesign-design.md`. The MasterMind verified R2 by
mutation testing and found the gaps below. Every round ends with `make check`
and `go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline. Name the test that fails for each condition and report the
mutation you ran against it (break the condition, see the test fail, revert).

Reconcile's rules are right; its shape is not, and its first live run is the
join of a machine with ~26k rows whose compressed bodies total ~380 MB:

- `walk` reads every owned row of every table (`SharedOwnedRows`) and encodes
  every differing body before `differences(limit)` keeps the first `limit`, so
  the whole history is in memory at once.
- Every `ReconcileBatch` re-walks everything to take the next chunk.
- Each table is read in its own transaction while the daemon writes.

Change it so that:
1. Rows are read in key-ordered pages (keyset pagination on the primary key,
   bounded page size) via a new `internal/db` method; no call reads a whole
   table.
2. A chunk stops as soon as it holds its limit of differences: bodies are
   encoded only for rows inspected in that chunk.
3. `Reconcile()` keeps a cursor (table index, last key, upserts-or-deletes
   phase) across its chunks, so each row is inspected once per run; head is
   read once per run (or per chunk if that is simpler, say which).
4. Each chunk's reads happen in ONE read transaction, so a chunk is a
   consistent snapshot and a row never arrives before a parent it references.
5. The delete phase pages head rows the same way instead of one query per head
   row if that is cheap; otherwise keep it and say why.
6. Output order is unchanged: upserts parents-first, deletes children-first;
   `TestReconcileOrdersDeletesChildrenFirst` and the converge tests stay green.

Tests: `TestReconcileReadsBoundedPages` (a seam or counter shows no read returns
more than the page size), `TestReconcileInspectsEachRowOnce` (a multi-chunk run
reads each row once), plus the existing converge tests. Mutations to report:
reading a whole table, and restarting the walk per chunk, each fail a named
test.

---

<!-- r2fix/round-3.md -->

# R2 follow-up round 3 of 3: stronger column-drift tests and a cleanup pass

Slice R2 follow-up of the sync redesign, branch `relevo/sync-log` with R2
(`internal/synclog`, `internal/db/sync_*.go`) already on it. Read spec §3a:
`docs/specs/2026-10-07-sync-redesign-design.md`. The MasterMind verified R2 by
mutation testing and found the gaps below. Every round ends with `make check`
and `go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline. Name the test that fails for each condition and report the
mutation you ran against it (break the condition, see the test fail, revert).

1. `TestImporterIgnoresUnknownColumns` and `TestImporterDefaultsMissingColumns`
   mostly assert that the import succeeds. Make each assert the resulting row
   column by column: an unknown body key changes nothing and is not named in
   SQL; an omitted column takes its declared default on insert and keeps the
   existing value on update. Mutations to report: naming NULL for an omitted
   column on update, and binding an unknown key, each fail a named test.
2. Run `go test -race -count=10 ./internal/synclog/ ./internal/db/` and report
   any failure (the TempDir cleanup race was fixed in `prepareTempDir`; confirm
   it stays fixed).
3. Grep every `.go` file under `internal/synclog` and the `internal/db/sync_*`
   files for history wording (`used to|no longer|round [0-9]|old (engine|path|design)`)
   and fix any that describe history rather than current behaviour.
