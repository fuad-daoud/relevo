# Sync redesign R0-R1: strip the file sync, add the outbox

The round plans of this slice of the sync redesign, in the order they ran,
as the builders received them. Spec: `docs/specs/2026-10-07-sync-redesign-design.md`.

---

<!-- r01/round-1.md -->

# Round 1 of 6: verbs, CLI, cockpit and daemon stop calling the old sync

Slices R0 (strip) and R1 (outbox) of the sync redesign, on branch `relevo/sync-log`.
Read the spec first: `docs/specs/2026-10-07-sync-redesign-design.md` (approved; its
§7 decisions are not re-opened). Every round leaves `make check` green on its own.

Global rules every round follows (not repeated per round): CLAUDE.md style (no history in code or tests — no issue numbers, "round N", "used to"; functions ≤ 70 lines; non-test files ≤ 600 lines; comments say why only); no new lint/comment/filesize exclusion to get green; if code moves between packages the round regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so, never lowering a baseline; `cmd/relevo` tests never spawn a harness or reach the network — CLI rules are tested as pure functions in `internal/...`; each round names the test that fails if its key condition is broken. Focused iteration command is `go test` on the touched packages (named per round); the full gate each round runs once at the end is `make check`.

Coverage: if `scripts/check-coverage.sh` reports a drop caused only by deleted code,
do not edit `testdata/coverage-baseline.txt`; list the per-package numbers in the report.

## MasterMind amendments (these override anything below)

- This round runs FIRST because it removes the callers. `internal/db` and
  `internal/sync` code that loses its last caller here stays in place
  (rounds 2 and 3 delete it); only unexported identifiers that `unused` lint
  would flag go now.
- Introduce the one stub error here, in `internal/sync/sync.go` (e.g.
  `ErrSyncUnavailable`, message: sync is not available in this build), and
  return it from enable, push and pull at the verb layer. Round 2 reuses it.
- Remove every call from `internal/relevo`, `cmd/relevo` and `internal/ui` to
  `SeedCopy`, `HasSyncMarker`/`RequireSyncMember`, `DecideSeed`,
  `MarkSeeding`/seeding markers, the backfill, and the sync-handle open.
- There is no idle-window reachability test yet; ignore that reference below.
  `idleSync`/`queueSync` become no-ops that open nothing.

## Plan

Goal: after R0, sync is a stub — `relevo db sync status` works and reports off; enable/push/pull refuse with the round-2 named error; the daemon never opens a sync handle; `:sync` renders the off state; existing `turso_cdc`/driver tables in user files are untouched (no migration).

Files touched: `internal/relevo/syncverb.go` (1-551: `SeedCopy` wiring ~140, `HasSyncMarker` gate ~301), `syncverb_runner.go` (1-56: marker branch ~51), `sync_tick.go` (1-117: `tickRunner`/`queueSync`/`idleSync`/`runSyncOn`), `daemon.go` (sync guard fields ~71-175, tick call ~293, seal trigger ~461) — behavior only, fields may stay for R4; `cmd/relevo/db_sync.go` (1-458: `--seed-uploaded` flag ~94, usage ~22-35, enable path ~208-280), `cmd/relevo/db_sync_call.go` (1-255: push/pull/status one-shot paths), `internal/ui/view_sync.go` (1-538) + `actions_sync.go` (1-397) off-state rendering only.

Ordered steps:

1. Route `VerbRunner.Run` enable/push/pull to the stub error while keeping the verb names, owner-socket plumbing, and disable/status paths, confirmed by `go test ./internal/relevo/`.
2. Remove `--seed-uploaded` flag, seed-copy refs, and seed-path text from `cmd/relevo/db_sync*.go` as pure CLI parsing over `internal/...` helpers, confirmed by `go test ./cmd/relevo/`.
3. Make `idleSync`/`queueSync` never open a handle (off-state no-op, still callable) and confirm the daemon tick test suite passes.
4. Render the `:sync` off state (token `sync:off`, no remote reads) and confirm `go test ./internal/ui/`.
5. Run focused `go test ./internal/relevo/ ./cmd/relevo/ ./internal/ui/` to green, then `make check` once.

Tests: new `TestSyncStatusReportsOff` (CLI-level doc mapping tested as a pure function in `internal/...`, e.g. status doc→line for the off state); new `TestSyncViewRendersOff` (snapshot with no markers renders off, performs no I/O); kept verb-plumbing tests (`TestSyncVerb*` role/probe tests minus marker assertions) and the idle-window reachability test still call `idleSync` and observe a no-op. Mutation: re-adding a handle open on the idle path makes the reachability/no-open test fail. CLI purity is stated in the round report.

Acceptance: `db sync status` prints off; enable/push/pull refuse with the named error; daemon serves with sync off; cockpit `:sync` shows off; no migration added; `make check` green.

Out of scope: outbox migration/triggers/tests/truncation (rounds 4-6); R2+ reconcile/exporter/importer/worker.

## Deleted by the end of round 3 (closed list)

1. `internal/db/capture.go` + capture pool/`captureConn`/`captureChangesPragma` behavior.
2. `internal/db/capture_rewrite.go` + `Rerecord` change-set rewrite behavior.
3. `internal/db/capture_fk.go` as a file (deleted in round 3; the order comes back as the round 4 shared-table list).
4. `internal/db/writeconn.go` + member-only write routing (`member()`, held-capture writes).
5. `internal/db/capture_modernc.go` stubs and every other tag twin of the capture path.
6. `internal/db/syncmember.go` + `MarkSyncMember`/`IsSyncMember`/marker-table cache behavior.
7. `internal/db/seed_copy.go` + `SeedCopy`/`UploadShape` seed-copy/upload-shape behavior.
8. `internal/sync/backfill.go` change-set backfill behavior (origin backfill in `internal/db` stays).
9. `internal/sync/sidecar.go` driver sidecar edits.
10. `internal/sync/watermark.go` + `internal/sync/turso.go#invalidateAndReopen` watermark invalidation/reopen behavior.
11. `internal/sync/seedcase.go` seed matrix + `internal/sync/seeding.go` seeding-marker/`demandSeedUpload` behavior + `--seed-uploaded` flag and `SeedPath`/`relevo-seed.db` path behavior.
12. `internal/sync/probe.go` membership-marker behavior (`HasSyncMarker`/`RequireSyncMember`) and the member/scratch/seed open-role gate where it exists only for the old engine.
13. Plain-pool member checkpoints/special-casing of a member file (ordinary-file checkpointing stays).
14. Sync enable/push/pull doing anything but refusing with the one named stub error (until R2+ re-implements on the log design).

Kept (not deleted): origin backfill/twin/repoint/gate; `sync` settings section + `turso.token`; statusline tokens; `:sync` view shell (off state); verb plumbing over the owner socket; remote-refusal classification; `relevo-local.db` split; `docs/sync-driver-panic.md`.

## Report requirements

Include: files changed vs the round's declared scope (`git diff --stat` compared); the named mutation check (what was broken, which test failed); focused `go test` command output and the final `make check` output; whether `testdata/coverage-baseline.txt` was regenerated (with the `--write` command) or untouched; for any CLI-touching round, the sentence that its tests are pure functions in `internal/...` with no harness or network; anything deliberately left not done.

---

<!-- r01/round-2.md -->

# Round 2 of 6: strip the old engine from internal/sync

Slices R0 (strip) and R1 (outbox) of the sync redesign, on branch `relevo/sync-log`.
Read the spec first: `docs/specs/2026-10-07-sync-redesign-design.md` (approved; its
§7 decisions are not re-opened). Every round leaves `make check` green on its own.

Global rules every round follows (not repeated per round): CLAUDE.md style (no history in code or tests — no issue numbers, "round N", "used to"; functions ≤ 70 lines; non-test files ≤ 600 lines; comments say why only); no new lint/comment/filesize exclusion to get green; if code moves between packages the round regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so, never lowering a baseline; `cmd/relevo` tests never spawn a harness or reach the network — CLI rules are tested as pure functions in `internal/...`; each round names the test that fails if its key condition is broken. Focused iteration command is `go test` on the touched packages (named per round); the full gate each round runs once at the end is `make check`.

Coverage: if `scripts/check-coverage.sh` reports a drop caused only by deleted code,
do not edit `testdata/coverage-baseline.txt`; list the per-package numbers in the report.

## MasterMind amendments (these override anything below)

- The stub error already exists (round 1 added it in `internal/sync/sync.go`);
  reuse it, do not add a second one. Skip step 2's "introduce" part.
- After round 1 nothing outside `internal/sync` calls the code this round
  deletes. If something still does, remove that call rather than keeping the
  dropped code alive.
- `internal/db` capture/seed-copy code is deleted in round 3, not here; this
  round only stops `internal/sync` from using it (`backfill.go` is its last
  user).
- Leave `internal/db/preflight.go` alone, whatever the plan below says about
  trimming it; round 3 trims it together with the seed copy it references.

## Plan

Goal: `internal/sync` holds no change-set backfill, no sidecars, no watermarks, no seed matrix/upload path; enable/push/pull refuse with one named error saying sync is not available in this build; origin gate, settings/token plumbing, statusline tokens, remote-refusal classification stay.

Files touched: delete `internal/sync/backfill.go` (1-335) + `backfill_test.go`, `sidecar.go` (1-238) + `sidecar_test.go`, `watermark.go` (1-281) + `watermark_test.go`, `seedcase.go` (1-103), `seeding.go` (1-112), `open_turso_sidecar_test.go`, `pullwatermark_test.go`, `scratch_pushpull_test.go`, `live_probe_verify_test.go`; stub `enable.go` (1-534), `runner.go` (1-264), `turso.go` (`invalidateAndReopen`:103-160 deleted, `Turso` type kept only if the stub needs it, else deleted); trim `probe.go` (`HasSyncMarker`/`RequireSyncMember` at 105-160 deleted; `Throwaway` kept only if an R4 probe still needs it, else the file goes), `open.go`/`open_turso.go`/`open_modernc.go` (member/seed roles deleted); trim `preflight.go` remainder per preamble point 9.

Ordered steps:

1. Delete the engine files above and confirm no `*.go` references `KeyBackfill|KeySeeding|KeySeeded|SeedCase|DecideSeed|InvalidateStaleWatermark|WALMaxFrame|invalidateAndReopen` outside deleted tests.
2. Introduce the single stub error (e.g. `ErrSyncUnavailable` in `internal/sync/sync.go`) returned by enable/push/pull paths and confirm `grep -rn "SeedUpload\|SeedCopy\|seed-uploaded" --include="*.go" internal/sync/ cmd/` is empty.
3. Keep `settings.go`, `token.go`, `client.go`/`fake.go`, `sync.go` tokens/state, `remoterefusal.go` compiling with their tests and confirm `go test ./internal/sync/` passes.
4. Run focused `go test ./internal/sync/ ./internal/db/` to green, then `make check` once.

Tests: new `TestSyncVerbsRefuseWhenStubbed` (enable, push, pull each return the one named error); kept `TestToken*`, `TestSettings*`, `TestStatusToken*`, `TestClassifyRemoteRefusal*` pin the keep column. Mutation: routing enable past the stub to the old seed decision makes the test fail (and the test names the stub error).

Acceptance: package builds on both tags; no test reaches the network; `make check` green.

Out of scope: CLI text/flags (round 1), daemon wiring (round 1), outbox (rounds 4-6).

## Deleted by the end of round 3 (closed list)

1. `internal/db/capture.go` + capture pool/`captureConn`/`captureChangesPragma` behavior.
2. `internal/db/capture_rewrite.go` + `Rerecord` change-set rewrite behavior.
3. `internal/db/capture_fk.go` as a file (deleted in round 3; the order comes back as the round 4 shared-table list).
4. `internal/db/writeconn.go` + member-only write routing (`member()`, held-capture writes).
5. `internal/db/capture_modernc.go` stubs and every other tag twin of the capture path.
6. `internal/db/syncmember.go` + `MarkSyncMember`/`IsSyncMember`/marker-table cache behavior.
7. `internal/db/seed_copy.go` + `SeedCopy`/`UploadShape` seed-copy/upload-shape behavior.
8. `internal/sync/backfill.go` change-set backfill behavior (origin backfill in `internal/db` stays).
9. `internal/sync/sidecar.go` driver sidecar edits.
10. `internal/sync/watermark.go` + `internal/sync/turso.go#invalidateAndReopen` watermark invalidation/reopen behavior.
11. `internal/sync/seedcase.go` seed matrix + `internal/sync/seeding.go` seeding-marker/`demandSeedUpload` behavior + `--seed-uploaded` flag and `SeedPath`/`relevo-seed.db` path behavior.
12. `internal/sync/probe.go` membership-marker behavior (`HasSyncMarker`/`RequireSyncMember`) and the member/scratch/seed open-role gate where it exists only for the old engine.
13. Plain-pool member checkpoints/special-casing of a member file (ordinary-file checkpointing stays).
14. Sync enable/push/pull doing anything but refusing with the one named stub error (until R2+ re-implements on the log design).

Kept (not deleted): origin backfill/twin/repoint/gate; `sync` settings section + `turso.token`; statusline tokens; `:sync` view shell (off state); verb plumbing over the owner socket; remote-refusal classification; `relevo-local.db` split; `docs/sync-driver-panic.md`.

## Report requirements

Include: files changed vs the round's declared scope (`git diff --stat` compared); the named mutation check (what was broken, which test failed); focused `go test` command output and the final `make check` output; whether `testdata/coverage-baseline.txt` was regenerated (with the `--write` command) or untouched; for any CLI-touching round, the sentence that its tests are pure functions in `internal/...` with no harness or network; anything deliberately left not done.

---

<!-- r01/round-3.md -->

# Round 3 of 6: strip capture and member routing from internal/db

Slices R0 (strip) and R1 (outbox) of the sync redesign, on branch `relevo/sync-log`.
Read the spec first: `docs/specs/2026-10-07-sync-redesign-design.md` (approved; its
§7 decisions are not re-opened). Every round leaves `make check` green on its own.

Global rules every round follows (not repeated per round): CLAUDE.md style (no history in code or tests — no issue numbers, "round N", "used to"; functions ≤ 70 lines; non-test files ≤ 600 lines; comments say why only); no new lint/comment/filesize exclusion to get green; if code moves between packages the round regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so, never lowering a baseline; `cmd/relevo` tests never spawn a harness or reach the network — CLI rules are tested as pure functions in `internal/...`; each round names the test that fails if its key condition is broken. Focused iteration command is `go test` on the touched packages (named per round); the full gate each round runs once at the end is `make check`.

Coverage: if `scripts/check-coverage.sh` reports a drop caused only by deleted code,
do not edit `testdata/coverage-baseline.txt`; list the per-package numbers in the report.

## MasterMind amendments (these override anything below)

- After rounds 1 and 2 nothing outside `internal/db` uses the capture pool,
  member routing, markers or seed copy, so deleting them here compiles.
- `capture_fk.go`: DELETE it with its test. Do not relocate it. The
  parents-first order is re-created in round 4 as a fixed, ordered list of
  shared tables checked against the schema's foreign keys.
- Finish the `preflight.go` trim here: keep the origin gate (and the secret and
  compress checks only if they reference nothing deleted); delete the
  upload-shape check.

## Plan

Goal: `internal/db` opens `relevo.db` with no capture pool, no member routing, no marker reads, no seed copy; origin backfill/twin/repoint/gate and the `relevo-local.db` split keep working.

Files touched: delete `internal/db/capture.go` (1-386), `capture_rewrite.go` (1-422), `capture_modernc.go` (1-25), `writeconn.go` (1-62), `syncmember.go` (1-102), `seed_copy.go` (1-167); decide `capture_fk.go` (1-216) per preamble point 7 (relocate pure `groupTables`/`parentsFirst` to a reconcile-owned home or delete with the recorded reason); delete capture tests (`capture_test.go`, `capture_rewrite_test.go`, `capture_fk_test.go`, `capture_churn_test.go`, `capture_pragma_test.go`, `capturemember_test.go` and their helpers); edit `db.go` (~300-390: `Close` capture release, `writeConn` call sites, member branches), `engine_turso.go` (capture/member comments and pool paths), `dial.go`/`dial_unsupported.go` (member routing), `preflight.go` (upload-shape check only — full preflight trim finishes in round 2 if it touches sync), `compress.go`/`vacuum.go` keep `walCheckpoint` (ordinary-file checkpointing stays; only member-specific checkpointing goes).

Ordered steps (one line each, deliverable + check):

1. Delete the six files plus capture tests and confirm `grep -rn "turso_cdc\|captureConnFor\|releaseHeldCapture\|closeCapturePool\|MarkSyncMember\|IsSyncMember\|SeedCopy\|UploadShape" --include="*.go" internal/ cmd/` shows no `internal/db` hits.
2. Resolve `capture_fk.go` (relocate ordering with its test, or delete with the R2-rewrite reason in the report) and confirm the chosen home compiles and `CaptureGroups`-or-successor has one owner.
3. Rewire `db.go`/`engine_turso.go`/`dial.go` writes and `Close` to the plain pool path and confirm `go test ./internal/db/` passes.
4. Neutralize tag twins so `go build ./...` and `go build -tags modernc ./...` both succeed with no `writeConn`/`member()` references left.
5. Run the focused suite `go test ./internal/db/` to green, then `make check` once.

Tests (with names): new/kept `TestOpenHasNoCapturePool` (open a scratch `relevo.db`, assert no `turso_cdc` tables are created and writes succeed through the pool); kept origin tests (`TestOriginGate*`, `TestOriginBackfill*`, `TestOriginRepoint*`, twin tests) pin the keep column; tree grep for `-info| -changes|turso_cdc` over `*.go` is asserted by `TestNoDriverPrivateNames` (docs excluded). Mutation: restoring the capture pragma on one connection makes `TestOpenHasNoCapturePool` fail.

Acceptance: `relevo.db` opens and writes with sync untouched; origin tooling and split tests pass; `make check` green.

Out of scope: anything in `internal/sync` (round 2), CLI/verbs/cockpit/daemon behavior (round 1), outbox migration (round 4).

## Deleted by the end of round 3 (closed list)

1. `internal/db/capture.go` + capture pool/`captureConn`/`captureChangesPragma` behavior.
2. `internal/db/capture_rewrite.go` + `Rerecord` change-set rewrite behavior.
3. `internal/db/capture_fk.go` as a file (deleted in round 3; the order comes back as the round 4 shared-table list).
4. `internal/db/writeconn.go` + member-only write routing (`member()`, held-capture writes).
5. `internal/db/capture_modernc.go` stubs and every other tag twin of the capture path.
6. `internal/db/syncmember.go` + `MarkSyncMember`/`IsSyncMember`/marker-table cache behavior.
7. `internal/db/seed_copy.go` + `SeedCopy`/`UploadShape` seed-copy/upload-shape behavior.
8. `internal/sync/backfill.go` change-set backfill behavior (origin backfill in `internal/db` stays).
9. `internal/sync/sidecar.go` driver sidecar edits.
10. `internal/sync/watermark.go` + `internal/sync/turso.go#invalidateAndReopen` watermark invalidation/reopen behavior.
11. `internal/sync/seedcase.go` seed matrix + `internal/sync/seeding.go` seeding-marker/`demandSeedUpload` behavior + `--seed-uploaded` flag and `SeedPath`/`relevo-seed.db` path behavior.
12. `internal/sync/probe.go` membership-marker behavior (`HasSyncMarker`/`RequireSyncMember`) and the member/scratch/seed open-role gate where it exists only for the old engine.
13. Plain-pool member checkpoints/special-casing of a member file (ordinary-file checkpointing stays).
14. Sync enable/push/pull doing anything but refusing with the one named stub error (until R2+ re-implements on the log design).

Kept (not deleted): origin backfill/twin/repoint/gate; `sync` settings section + `turso.token`; statusline tokens; `:sync` view shell (off state); verb plumbing over the owner socket; remote-refusal classification; `relevo-local.db` split; `docs/sync-driver-panic.md`.

## Report requirements

Include: files changed vs the round's declared scope (`git diff --stat` compared); the named mutation check (what was broken, which test failed); focused `go test` command output and the final `make check` output; whether `testdata/coverage-baseline.txt` was regenerated (with the `--write` command) or untouched; for any CLI-touching round, the sentence that its tests are pure functions in `internal/...` with no harness or network; anything deliberately left not done.

---

<!-- r01/round-4.md -->

# Round 4 of 6: outbox migration and triggers

Slices R0 (strip) and R1 (outbox) of the sync redesign, on branch `relevo/sync-log`.
Read the spec first: `docs/specs/2026-10-07-sync-redesign-design.md` (approved; its
§7 decisions are not re-opened). Every round leaves `make check` green on its own.

Global rules every round follows (not repeated per round): CLAUDE.md style (no history in code or tests — no issue numbers, "round N", "used to"; functions ≤ 70 lines; non-test files ≤ 600 lines; comments say why only); no new lint/comment/filesize exclusion to get green; if code moves between packages the round regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so, never lowering a baseline; `cmd/relevo` tests never spawn a harness or reach the network — CLI rules are tested as pure functions in `internal/...`; each round names the test that fails if its key condition is broken. Focused iteration command is `go test` on the touched packages (named per round); the full gate each round runs once at the end is `make check`.

Coverage: if `scripts/check-coverage.sh` reports a drop caused only by deleted code,
do not edit `testdata/coverage-baseline.txt`; list the per-package numbers in the report.

## MasterMind amendments (these override anything below)

- Outbox schema: `sync_outbox(seq INTEGER PRIMARY KEY AUTOINCREMENT, tbl TEXT
  NOT NULL, pk TEXT NOT NULL, op TEXT NOT NULL, origin TEXT)`. `op` is
  `insert`, `update` or `delete`. `origin` is the owning installation id, or
  NULL when the trigger cannot resolve it (a child deleted by a cascade after
  its parent is gone); the R2 exporter skips NULL-origin rows.
- `pk` is the row's primary key as `json_array(...)` of its key columns, even
  for one-column keys, so the exporter parses one shape. First confirm in a
  test that the linked engine supports `json_array` inside a trigger body; if
  it does not, stop and report rather than inventing another encoding.
- `installation` (`id`, `label`, `first_seen`, `last_seen`): each machine
  writes only its own row and its `id` IS the installation id, so its origin
  expression is `NEW.id` / `OLD.id`. It gets the three triggers like any root
  table. Preamble point 3 is resolved by this.
- Add `internal/db/shared_tables.go`: the 15 shared tables as one ordered Go
  list, parents first, each with its primary-key columns. Round 5's drift test
  checks the migration against this list, the list against SCOPES.md, and the
  order against `pragma_foreign_key_list`. R2's exporter and reconcile will use
  the same list.
- Verified on tursogo v0.8.1: AFTER triggers fire for insert, update and
  delete, and `INSERT OR REPLACE` of an existing row fires only the insert
  trigger. Encode that; do not work around it.

## Plan

Goal: one migration (next free number — `022` at plan time, re-verified with `ls`) adds `sync_outbox(seq, tbl, pk, op)` plus `AFTER INSERT/UPDATE/DELETE` triggers on every shared table in SCOPES.md's table list, keyed on each table's PK with the owning origin resolved inside the trigger.

Files touched: new `internal/db/migrations/022_sync_outbox.sql`; `internal/db/migrations/README.md` (trigger rule update); `SCOPES.md` (header count fix); `internal/db/migrate.go` (only if the generic runner needs trigger support — expected: no change).

Scope table (15, PK → origin expression source): `binding_record`(id→`NEW.origin`), `binding`(id→`NEW.origin`), `repo`(id→`NEW.origin`), `mastermind`(id→`NEW.origin`), `chains`(id→`NEW.origin`), `binding_event`((record_id,seq)→parent `binding_record`), `round_file`((record_id,name)→parent `binding_record`), `chain_event`((chain_id,seq)→parent `chains`), `chain_member`((chain_id,binding)→parent `chains`), `chain_check`((chain_id,run)→parent `chains`), `round`(id→parent `binding` via `binding_id`), `event`(id→parent `binding` via `binding_id`), `artifact`(id→`round→binding` two-hop), `transcript`(id→`CASE owner_kind`: round→`round→binding`, mastermind→`mastermind`; see preamble point 4), `installation`(id→ resolved per preamble point 3 and recorded). Local-only tables (`kv`, `secret`, `config_doc`, `config_revision`, `config_meta`, `config_import`, `session_consent`, `ingest_cursor`) get no triggers. `INSERT OR REPLACE` behavior from preamble point 11 is encoded (insert trigger only), not "fixed". `DELETE` of a child whose parent is already gone (cascade) writes the row with whatever origin the trigger could resolve; the skip rule lives in the R2 exporter, not here — the trigger never fails the deleting statement.

Ordered steps:

1. Write migration 022 (table + triggers, `CREATE ... IF NOT EXISTS`, version-guard-safe) and confirm a fresh open migrates to the new version.
2. Update `README.md` trigger rules and the SCOPES.md header, confirmed by `git diff --stat` showing the doc changes beside the migration.
3. Verify every shared-table PK is keyed in its three triggers by selecting trigger bodies from `sqlite_schema` on a fresh file.
4. Run focused `go test ./internal/db/` to green, then `make check` once.

Tests: round 5 owns the test files; this round's pin is the migration itself plus a smoke check that a fresh database carries `sync_outbox` and one trigger per (table × insert/update/delete). Mutation: dropping any one trigger makes round 5's drift test fail (named there).

Acceptance: fresh databases have the outbox + full trigger set; existing databases migrate forward in one transaction; local-only writes produce no rows (proved in round 5); `make check` green.

Out of scope: drift/write-shape tests (round 5), truncation (round 6), exporter/importer/reconcile (R2).

## Report requirements

Include: files changed vs the round's declared scope (`git diff --stat` compared); the named mutation check (what was broken, which test failed); focused `go test` command output and the final `make check` output; whether `testdata/coverage-baseline.txt` was regenerated (with the `--write` command) or untouched; for any CLI-touching round, the sentence that its tests are pure functions in `internal/...` with no harness or network; anything deliberately left not done.

---

<!-- r01/round-5.md -->

# Round 5 of 6: outbox drift and write-shape tests

Slices R0 (strip) and R1 (outbox) of the sync redesign, on branch `relevo/sync-log`.
Read the spec first: `docs/specs/2026-10-07-sync-redesign-design.md` (approved; its
§7 decisions are not re-opened). Every round leaves `make check` green on its own.

Global rules every round follows (not repeated per round): CLAUDE.md style (no history in code or tests — no issue numbers, "round N", "used to"; functions ≤ 70 lines; non-test files ≤ 600 lines; comments say why only); no new lint/comment/filesize exclusion to get green; if code moves between packages the round regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so, never lowering a baseline; `cmd/relevo` tests never spawn a harness or reach the network — CLI rules are tested as pure functions in `internal/...`; each round names the test that fails if its key condition is broken. Focused iteration command is `go test` on the touched packages (named per round); the full gate each round runs once at the end is `make check`.

Coverage: if `scripts/check-coverage.sh` reports a drop caused only by deleted code,
do not edit `testdata/coverage-baseline.txt`; list the per-package numbers in the report.

## MasterMind amendments (these override anything below)

- The drift test compares three things: the shared-table list in
  `internal/db/shared_tables.go`, the shared tables named in SCOPES.md, and
  the triggers present on a freshly migrated file (three per table, keyed on
  the list's PK columns). It also checks the list is parents-first against
  `pragma_foreign_key_list`.
- Write-shape tests assert the `origin` column too: a root row's own origin,
  a child's inherited origin (including `artifact` two hops and `transcript`
  by `owner_kind`), and NULL only for a cascade-deleted child whose parent is
  gone.

## Plan

Goal: prove every shared-table write shape leaves the expected outbox rows and no local-only table does.

Files touched: new `internal/db/outbox_drift_test.go` (or `sync_outbox_test.go` — one home for both tests below); no non-test files except fixes migration 022 needs (amend as new commit, never rewriting round 4's commit).

Ordered steps:

1. Add `TestSyncOutboxDriftCoversSharedTables` enumerating SCOPES.md's shared-table list and asserting each has its three triggers keyed on its current PK, confirmed by changing one PK in a scratch copy and watching it fail.
2. Add `TestSyncOutboxWriteShapes` covering insert, update, upsert (`INSERT OR REPLACE` → single insert row per preamble point 11), delete, and cascade delete on a parent with children, confirmed against a fresh migrated file with foreign keys on.
3. Add the local-only negative (`config_doc`/`secret`/`kv`/`session_consent` writes leave no rows) in the same file, confirmed by the same run.
4. Run focused `go test ./internal/db/ -run 'Outbox|Drift'` to green, then full `go test ./internal/db/`, then `make check` once.

Tests: the two names above (mutation-tested by the MasterMind: break one trigger's PK or origin expression and the named test fails). No history in test names/comments.

Acceptance: all write shapes pinned; a later migration that changes a shared table's PK breaks the drift test loudly; `make check` green.

Out of scope: truncation (round 6), any exporter/importer semantics.

## Report requirements

Include: files changed vs the round's declared scope (`git diff --stat` compared); the named mutation check (what was broken, which test failed); focused `go test` command output and the final `make check` output; whether `testdata/coverage-baseline.txt` was regenerated (with the `--write` command) or untouched; for any CLI-touching round, the sentence that its tests are pure functions in `internal/...` with no harness or network; anything deliberately left not done.

---

<!-- r01/round-6.md -->

# Round 6 of 6: truncate the outbox while sync is off

Slices R0 (strip) and R1 (outbox) of the sync redesign, on branch `relevo/sync-log`.
Read the spec first: `docs/specs/2026-10-07-sync-redesign-design.md` (approved; its
§7 decisions are not re-opened). Every round leaves `make check` green on its own.

Global rules every round follows (not repeated per round): CLAUDE.md style (no history in code or tests — no issue numbers, "round N", "used to"; functions ≤ 70 lines; non-test files ≤ 600 lines; comments say why only); no new lint/comment/filesize exclusion to get green; if code moves between packages the round regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so, never lowering a baseline; `cmd/relevo` tests never spawn a harness or reach the network — CLI rules are tested as pure functions in `internal/...`; each round names the test that fails if its key condition is broken. Focused iteration command is `go test` on the touched packages (named per round); the full gate each round runs once at the end is `make check`.

Coverage: if `scripts/check-coverage.sh` reports a drop caused only by deleted code,
do not edit `testdata/coverage-baseline.txt`; list the per-package numbers in the report.

## MasterMind amendments (these override anything below)

- Do NOT hook truncation onto `idleSync`. That path is gated on `rt.Sync`,
  which production never sets, so it never runs. Add truncation as its own
  phase in the daemon's `Tick` (beside the other periodic phases, through
  `d.safely`), gated only on the machine-local enabled mark (`sync.enabled`
  in `relevo-local.db`, read via `internal/sync` state): truncate when off,
  never when on. Bound it to once per window (reuse the 5-minute window
  constant or its own), not every 2 s tick.
- The tests must drive the real `Tick`, not the phase function directly, so a
  phase that is never called fails them. Mutation: removing the phase from
  `Tick` must fail `TestSyncOutboxTruncatesWhileOff`.

## Plan

Goal: while sync is off, the daemon truncates the outbox on a schedule (reconcile covers the gap at enable in R2); while sync is on, it never truncates.

Files touched: outbox truncate helper beside the migration owner (`internal/db` — e.g. `outbox.go`, ≤600 lines, ≤70-line functions) plus the schedule hook in `internal/relevo/sync_tick.go` (`idleSync` path: truncate when `rt.Sync.On()`/enabled mark is off, never when on); test in `internal/db` and/or `internal/relevo` (`sync_tick_test.go` neighbor).

Ordered steps:

1. Add the truncate helper (delete-all from `sync_outbox`, safe on an empty table, no vacuum/checkpoint) and confirm `go test ./internal/db/` passes.
2. Wire the off-state schedule (truncate on the idle window only while off; on-state path untouched) and confirm the tick suite passes.
3. Prove never-truncate-while-on with the enabled-mark set in the test, confirmed by `go test ./internal/relevo/`.
4. Run focused `go test ./internal/db/ ./internal/relevo/` to green, then `make check` once.

Tests: new `TestSyncOutboxTruncatesWhileOff` (markers absent/off → rows truncated on the window) and `TestSyncOutboxKeptWhileOn` (enabled mark present → rows kept). Mutation: inverting the enabled check fails the second test.

Acceptance: off-state machines don't grow the outbox unboundedly; on-state rows are never discarded by this path; `make check` green; R1 complete.

Out of scope: reconcile/enable backfill of the gap (R2), any export/import.

## Report requirements

Include: files changed vs the round's declared scope (`git diff --stat` compared); the named mutation check (what was broken, which test failed); focused `go test` command output and the final `make check` output; whether `testdata/coverage-baseline.txt` was regenerated (with the `--write` command) or untouched; for any CLI-touching round, the sentence that its tests are pure functions in `internal/...` with no harness or network; anything deliberately left not done.

---

<!-- r01/round-7-cleanup.md -->

# Cleanup round: test strength, history wording, dead leftovers

Branch `relevo/sync-r01`, head `fd713d1a`. Spec:
`docs/specs/2026-10-07-sync-redesign-design.md`. This round follows the R0/R1
chain; the MasterMind verified that chain and found the items below. Nothing
here changes runtime behaviour except removing dead code and stale text.
`make check` must be green at the end.

Rules: CLAUDE.md code style (no history in code or tests: no "used to", "no
longer", "old ...", "round N", "stopped using"; comments state the current
constraint and why; functions <= 70 lines; files <= 600 lines). No new lint,
comment or filesize exclusion. Do not touch `testdata/coverage-baseline.txt`;
report per-package coverage if the guard complains.

## 1. Make the inherited-origin test able to fail (most important)

`internal/db/outbox_drift_test.go`, `testInheritedOriginShape` (and its
`seedRoots` helper if needed). Today every root row is seeded with the same
origin, `instA`, so a trigger that reads the owner from the WRONG table still
passes. Proven: changing `sync_outbox_artifact_ins`'s origin expression to
`(SELECT origin FROM mastermind LIMIT 1)` leaves `TestSyncOutboxWriteShapes`
green.

Fix: seed each root with a DIFFERENT origin, for example `binding` = `instB`,
`mastermind` = `instM`, `binding_record` = `instR`, `chains` = `instC`,
`repo` = `instP`, `installation` id = `instI`. Then assert every child's
outbox `origin` equals ITS OWN parent's:
- `round`, `event` -> their `binding` (`instB`);
- `artifact` -> `round` -> `binding` (`instB`);
- `transcript` with `owner_kind = 'round'` -> `instB`; with
  `owner_kind = 'mastermind'` -> `instM`;
- `binding_event`, `round_file` -> `binding_record` (`instR`);
- `chain_event`, `chain_member`, `chain_check` -> `chains` (`instC`);
- each root -> its own origin, and `installation` -> its own id.

Add the chain_* children and binding_event if the test does not insert them
yet. Before finishing, run these two mutations yourself and put the failing
test output in the report, then revert them:
- (a) `sync_outbox_artifact_ins` origin -> `(SELECT origin FROM mastermind LIMIT 1)`;
- (b) swap the two `transcript` CASE branches in `sync_outbox_transcript_ins`.
Both must fail `TestSyncOutboxWriteShapes`.

## 2. History wording (comments and test comments only)

Rewrite each to state the current constraint, with no history:
- `internal/relevo/sync_tick.go` (queueSync/idleSync doc comments): "used to
  hand a change set"; also shorten them, they restate the code.
- `internal/relevo/syncverb.go` around line 18: "reimplements the CLI's old
  direct-open path".
- `internal/sync/sync.go` around line 18 (`ErrSyncUnavailable` doc): "what they
  no longer do is".
- `cmd/relevo/db_sync.go` around line 195: "enable no longer needs the daemon
  stopped".
- `internal/relevo/syncverb_test.go` around line 89: "whatever decision used
  to"; and the test doc that says "this round's own pin".
- `internal/db/engine_turso.go` line 21: "round 2's one-time".
Then grep every `.go` file this branch changed (`git diff --name-only
7320331d -- '*.go'`) for `used to|no longer|round [0-9]|old (engine|path|driver|design)|stopped using`
and fix any other hit that describes history (a phrase like "a credential it
no longer uses" that describes current behaviour is fine).

## 3. Dead leftovers

- `internal/db/history.go`: `HasSharedHistory` and `historyTables` have no
  production caller; delete them and their tests.
- `internal/db/wire/msg.go`: delete `SyncCodeSeedUploadRequired`, the
  `SeedUploaded` verb field, the `Backfilled` result field, and any other
  seed/backfill-only field or code nothing sets; update the wire tests that
  only exercised them (`internal/db/wire/client/verb_test.go` and others).
- `cmd/relevo/db_sync.go` `dbSyncUsage`: say plainly that enable, push and
  pull refuse in this build; drop the sentences describing what `--url`
  stores and the contradiction refusal, since enable does none of that now.
  Keep the flag list if the flags still parse. Regenerate
  `cmd/relevo/testdata/contract/help-json.golden` only if it changes. Any CLI
  test stays a pure function: no harness, no network.

## Report

Files changed against this list; both mutation outputs from section 1; the
grep from section 2 with what you changed; focused `go test` output for
`./internal/db/... ./internal/relevo/... ./internal/sync/... ./cmd/relevo/...`;
`go build -tags modernc ./...`; final `make check` output.
