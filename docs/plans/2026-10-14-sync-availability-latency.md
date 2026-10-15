# Sync step 4: availability and latency as history tables — builder plan

## MasterMind corrections (binding; they override the plan text below where they differ)

C1. **The tables are append-only, and that is the trigger skip.** A row is never updated, and it is deleted only by age. So `availability_event` and `latency_sample` get an insert outbox trigger only: no update trigger and no delete trigger. A delete never reaches the outbox, so the age prune exports nothing with no clock in SQL and no connection marker. Do not use a `WHEN` age clause.
- The drift test and the owner/trigger tests take an explicit append-only set naming these two tables, so a third table cannot silently lose its delete trigger.
- The importer refuses a delete op on either table (the existing drop path). An upsert of an id this file already holds changes nothing, because rows are immutable.

C2. **Time is an integer.** Store `at` as `at_ms INTEGER` (Unix milliseconds, UTC) and prune with `at_ms < ?` from Go's clock. Today's events carry mixed offsets (`+03:00` and `Z`) and variable-width nanoseconds, so a text comparison would misorder them. Convert at the write and the migration, and convert back to `time.Time` on read.

C3. **An arriving row older than the window is skipped, not inserted.** The mark still moves in the same transaction. A joiner therefore never imports and then prunes 30+-day-old history. This replaces "applied then pruned". Test it.

C4. **Reconcile, for these two tables, only considers rows inside the window.** It never proposes a delete or a re-append for a row that aged out on either side, and never counts one as drift. Name the test `TestReconcileIgnoresAgedHistoryRows`.

C5. **The prune runs in the daemon's existing periodic maintenance** (`internal/relevo/daemon.go`, beside the mastermind prune), not on every tick. The read-time `Prune` filter stays, so a machine without a daemon still shows 30 days.

C6. **The migration runs where the runtime opens both files**, before any reader or writer of the histories, so the daemon, every verb and `db sync enable`/join all see migrated rows. It is idempotent behind a local kv marker. Name the function and every call site in the report.

C7. **Mixed versions.** Migration 029 bumps the schema version. A reader that is still at 028 holds an origin whose entries carry 029 (`Importer.held`); it does not drop the batch, which would lose the round rows sharing that batch. Pin this with a test: a reader at 028 meets a batch carrying a history row plus a round row, holds it, and applies both once it knows 029. If the schema version is not what entries carry, stop and report it.

C8. **The owner check matches the existing root tables.**
- The body's `origin` column never decides: a row lands under the entry's origin, as a forged origin does for other root tables. Find that code and say how it applies here.
- An upsert naming an existing row another origin owns is dropped.
- `TestImporterRefusesACrossOriginHistoryRow` covers both cases, for both tables.

C9. **Three rounds, not four.** Round 4's spec and SCOPES amendment folds into Round 3. A docs-only round would cost a full gate run and a review for a few sentences.

C10. **`TestPeerHistoryNeverGatesAPick`** stays as a pin, but keep it small: one peer outage event plus one slow peer sample, and this machine's pick unchanged. Picks read the ledger, not history.

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §2 (one-writer-per-machine row), §4.2 (Availability and latency), §11, §12 item 4. Steps 1–3c merged (`main` `29beadcb`). Target doc for this plan: `docs/plans/2026-10-14-sync-availability-latency.md`.

## Where the seed contradicts the code (read first, do not guess around)

1. **No trigger-skip mechanism exists.** Seed §3 says "find how the triggers can skip them (e.g. a local-only marker on the connection or a trigger condition on age)". `internal/db/migrations/022_sync_outbox.sql:1-7` and `internal/db/migrations/README.md:62-83` state triggers fire for every connection and every writer, write one row, and never raise; there is no connection marker, no `WHEN` age clause, no session variable anywhere in `internal/db`. The builder designs and builds the skip (Round 2 names the chosen mechanism and why); it is not found.
2. **No existing DB prune path exists.** Seed §3 says "on the existing prune path". Today prune is pure in-memory filtering on write/read: `internal/availability/history.go:78-87` (`History.Prune`), `internal/availability/latency.go:76-85`, applied at `internal/availability/gates.go:35,70,273`, `internal/availability/probe.go:282`, `cmd/relevo/candidates.go:112`, `internal/relevo/statsinputs.go:49`. No daemon tick, no SQL delete, no hourly prune touches these histories (`internal/relevo/daemon.go:249-274` prunes masterminds and truncates the outbox only). The builder creates the SQL prune and wires it into the daemon/verb path (Round 2).
3. **Writer-check behaviour is settled: drop, not hold.** Seed §2 says "hold or refuse, whichever the existing writer check does". The existing check drops: `internal/synclog/import.go:187-192,329-367,469-541` (`errNotOwner`, `owns`/`ownsAfterWrite`, `refused()` → `Drop` + mark moved past via `past()`), pinned by `internal/synclog/import_owner_test.go:32-139`. The new tables match drop semantics exactly.

## Behaviour and cases

- Two new tables in `relevo.db`, `availability_event` and `latency_sample`, each PK ULID `id` (`internal/db/ulid.go:13` `NewID`), plus `origin` (own installation id) plus the fields `internal/availability/history.go:22-37` `Event` and `internal/availability/latency.go:19-26` `Sample` hold. Both join `internal/db/shared_tables.go:22-38` `SharedTables` as roots (`owner: origin`, no hops), travelling through existing outbox triggers and row log. No remote format change.
- One writer per machine: importer refuses (drops, mark moved past, `Dropped` report) any row in either table whose `origin` ≠ entry origin, including a new child-equivalent case (row placed under a foreign origin — here every row is a root, so the check is `owns` on the row itself plus `ownsAfterWrite`; there is no parent indirection). Mutation-tested.
- Retention: every machine deletes rows of either table older than 30 days by row timestamp, for every origin. Age deletes are never exported, never imported as deletes, never count as drift in reconcile. A row arriving already older than the window is applied (or skipped) and pruned, never re-exported. Mutation-tested both directions (prune exports nothing; import of an old row emits nothing further).
- Migration: kv `availability`/`latency` documents move once into the tables as own rows (origin = own, fresh ULIDs in `at` order), behind an idempotent local kv marker (pattern: `sync.config_local.v1` in `internal/config/config.go`; marker name chosen by Round 3). Migrated rows export like new history. Afterwards kv rows deleted; nothing reads/writes them. No shared-doc write stamps involved (history tables, not docs).
- Readers combine all origins for display/stats; behaviour-deciding reads stay on own rows (below). `host` on samples kept.
- Writes go through existing append points (`AppendEntryLocked` ledger mirror `internal/availability/gates.go:64-76`, `Available` clear path `gates.go:261-278`, `recordLatency` `internal/availability/probe.go:272-287`), one `INSERT` per event/sample instead of document rewrite.
- `SCOPES.md` updated for the two tables in the round that adds them; kv rows removed from it in the round that deletes them. Last round amends spec §2/§4.2 to what was built.

### Complete reader list (audited before planning)

Display/stats — combine all origins after the change:

- `internal/relevo/statsinputs.go:17-83` `StatsInputs` (History + Gates + `TTFT` p50 closure over `LatencyHistory.Summary`) → `internal/stats` `Build` and `:stats` (`internal/ui/view_stats.go:194-206`).
- `internal/relevo/policy_view.go:483-496` `peakText` (`HourCounts`), `policy_view.go:504-581` `formatHistory` (`HourCounts`, `BlockedDurations`) → `FormatPolicy`/`FormatPolicyFor`.
- `cmd/relevo/candidates.go:102-120` `formatCandidates` (`LoadLatency` + per-ref `Summary`), `candidates.go:126-133` `loadHistory`, `candidates.go:137-141` `formatPolicy`; `cmd/relevo/config.go:132`, `cmd/relevo/gate.go:118`, `cmd/relevo/doctor.go:381,395`, `internal/view/candidates_list.go:27-131` (`Summary` map render), `internal/ui/view_log.go:135` (`LoadHistory`).
- `internal/serve/candidates.go:20`, `internal/serve/admin.go:448`, `internal/serve/actors.go:66`, `internal/serve/bindings.go:587` (gates/history display surfaces).

Behaviour-deciding — keep on this machine's own rows, never peers':

- `resolveCandidate`/`resolveRole` candidate picks (`internal/relevo/candidate.go:560`, `bind.go:406,695,801`, `placement.go:100`, `stale.go:28`, `chain_start.go:311`, `runtime.go:425`, `add.go:251`, `builder_change.go:37`, `served.go:330`) read `availability.Gates` (the live ledger incl. shared rate-limit doc) — **not** history. Peers' rate limits gate via the gates doc by design (provider quota); peers' outages/latency must not pick this machine's candidate.
- `LedgerGates`/`LedgerGatesFrom` (`internal/availability/gates_projection.go:13-32`) in `internal/relevo/switch.go:30,234,523,581`, `send.go:443`, `status.go:182` — live-ledger projection for switch/send/status gating; same rule: ledger (with gates doc) yes, history tables no.
- `RecordSpawnFailure`/`RecordSpawnFailureLocked` (`internal/availability/gates.go:88-127`, via `internal/relevo/headless.go:421`, `switch` paths) and `stampedSubjects`/`gateItems` (`internal/availability/gatedoc.go:28-37,87-96`): `spawn_failed` stays local, never exported — history mirrors of it stay own-origin too.
- Probe has no schedule/daemon tick; `Probe`/`ProbeCandidate` (`internal/availability/probe.go:115-266`, driven from `cmd/relevo/candidates.go:90` and `internal/ui/actions.go:509`) is CLI-driven. `peakText` is display-only by construction (`policy_view.go:485-487` "History never changes a pick"); `GatedNote` is advisory. No probe-schedule or gate-decision reader moves to combined rows.

## Seams (files, types, functions, line ranges)

- `internal/availability/history.go:1-153` (`Event`, `History`, `HistoryRetainWindow`, `loadHistory`/`SaveHistory`, `Prune`/`Append`, `FromEntry`, `HourCounts`, `BlockedDurations`); `internal/availability/latency.go:1-119` (`Sample`, `LatencyHistory`, `LatencyRetainWindow`, `LoadLatency`/`SaveLatency`, `Prune`/`Append`, `Summary`); `internal/availability/gates.go:23-36,54-76,172-287` (`LoadHistory`, `AppendEntryLocked`, `Unavailable`, `Available`+`clearLedger`); `internal/availability/probe.go:228-287` (`Probe`, `recordLatency`); `internal/availability/deps.go:1-41` (`Deps`: `Gates`, `Latency`, `Local`, `SyncNow`); `internal/availability/gates_projection.go:1-93`; `internal/availability/gatedoc.go:1-66` (`GatesName`, `GatesDoc` local-file binding pattern for the migration marker).
- `internal/relevo/runtime.go:135-188,369-386` (`Runtime.Gates/Latency/GatesDB`, `AvailabilityDeps`); `internal/relevo/statsinputs.go:17-83`; `internal/relevo/policy_view.go:483-581`; `cmd/relevo/candidates.go:100-154`; `internal/ui/view_stats.go:194-206`; `internal/ui/view_log.go:135`; `internal/view/candidates_list.go:27-131`.
- `internal/db/shared_tables.go:22-38` (list), `:436-478` (`OwnerRules`), `:486-575` (`ownerExpression`, `ResolveOwner`); `internal/db/sync_exchange.go:263-341` (`DrainOutbox`), `:398-478` (owner rules); `internal/db/sync_reconcile.go:32-133` (`SharedOwnedRowPage`); `internal/db/ulid.go:8-29` (`NewID`); `internal/db/migrations/022_sync_outbox.sql:1-232` (trigger pattern), `internal/db/migrations/README.md:62-83` (trigger rules), `internal/db/migrations/SCOPES.md:23-52,60-76` (shared table + kv rows); `internal/db/shared_tables_test.go:70-163`, `internal/db/outbox_drift_test.go:116-147,261-484`.
- `internal/synclog/import.go:187-212,329-367,456-541` (drop semantics, `owns`/`ownsAfterWrite`); `internal/synclog/export.go:157-239` (`entries`, `owned`, `coalesce`); `internal/synclog/reconcile.go:265-466` (`differences`, `nextUpserts`/`nextDeletes`, `deleteFor` — where the age-delete exclusion lands); `internal/synclog/converge_test.go:38-121` (`runRound`/`settle` steady-pipeline pattern); `internal/synclog/import_owner_test.go:1-313` (refusal-test pattern).
- `internal/sync/join.go`, `internal/sync/enable.go`, `internal/sync/steady.go`, `internal/sync/unlock.go`, `internal/relevo/syncverb_steady.go:15-50`, `internal/relevo/syncverb.go`, `internal/relevo/sync_tick.go:28-142,163-215`, `internal/relevo/daemon.go:108-142,203-279` (daemon/join/unlock/sync-verb wiring each round names).
- Gates: `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`. Focused: `make check-quick PKGS='./internal/<pkg>/...'` or `go test ./internal/<pkg>/ -run <Name>`. Host-independence: run every new test with a `PATH` holding only `git` before calling it done. Style: no history in code, functions ≤70 lines, non-test files ≤600 lines (`scripts/check-comments.sh`, `scripts/check-filesize.sh`, `.golangci.yml`); coverage baseline `testdata/coverage-baseline.txt` via `sh scripts/check-coverage.sh --write` only when moving code between packages, said so in the report, never lowered to get green.

## Rounds (4; each names daemon/join/unlock/sync-verb reach and runs through the real steady pipeline on memfake, two machines)

### Round 1 — tables, triggers, SharedTables, reconcile walk (no behaviour change; kv still authoritative)

- Step 1 — deliverable: migration `029_history_tables.sql` creating `availability_event`/`latency_sample` (ULID `id` PK, `origin`, all `Event`/`Sample` fields incl. `host`, `at` as RFC3339 UTC text) plus outbox triggers per `022` pattern and root `OwnerRules` entries; check: `TestSyncOutboxDriftCoversSharedTables` and `TestOwnerResolutionMatchesTriggers` pass with the two tables, `TestSharedTablesAreParentsBeforeChildren` holds (no-FK tables sort last). SCOPES.md gains both tables here.
- Step 2 — deliverable: `SharedOwnedRowPage`/reconcile walk covers the two tables with zero rows (walk-only, no prune yet); check: `go test ./internal/db/ -run 'TestSyncOutbox|TestSharedTables|TestOwnerResolution'`.
- Step 3 — deliverable: production wiring named (daemon tick needs no change this round; join/unlock/steady reach the tables only via the existing row walk; `push`/`pull`/`reconcile --dry-run` exercise them empty); check: `TestSyncPushAndPullMoveTheLog`-shaped memfake round with empty history tables moves nothing and reports nothing.
- Named tests: `TestSyncOutboxDriftCoversSharedTables` (extended), `TestOwnerResolutionMatchesTriggers` (extended), `TestSharedTablesAreParentsBeforeChildren`, `TestOutboxLeavesLocalOnlyTablesAlone` (still green). Gate: `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`.

### Round 2 — writes, origin refusal, retention prune with never-export (kv still read; tables written alongside)

- Step 1 — deliverable: `AppendEntryLocked` (`gates.go:64-76`), `Available` clear (`gates.go:261-278`), `recordLatency` (`probe.go:272-287`) each do one `INSERT` (own `origin`, `NewID`, `at`) into the new tables beside the existing kv write (kv still authoritative for reads); check: memfake two-machine steady round (`runRound`/`settle` pattern in `converge_test.go:38-121`) shows own rows exporting/importing, `TestImporterAppliesEntriesForRowsTheOriginOwns`-shaped case green.
- Step 2 — deliverable: origin refusal matching existing drop semantics (`owns`/`ownsAfterWrite` → `Drop` + mark moved past, never `Hold`); check: mutation-tested `TestImporterRefusesACrossOriginHistoryRow` (upsert + delete over both tables; fails with the check removed) plus `TestImportAdvancesPastARefusedBatch`-shaped follow-batch case.
- Step 3 — deliverable: retention prune (SQL delete of rows older than 30d by row timestamp, all origins) on the daemon/verb path Round 1 lacked, plus the trigger-skip mechanism Round 1 chose (migration `WHEN` age clause or connection-local marker — state which and why: `WHEN` keeps every connection identical and survives the `README` never-raise rule; a connection marker needs per-verb plumbing and a test per writer) so age deletes write no outbox row; importer drops history-table deletes for rows older than the window (applied-or-skipped locally, never re-exported); reconcile `nextDeletes`/`deleteFor` never proposes age deletes nor counts them as drift; check: three mutation tests each failing without its rule (prune-exports-nothing, old-row-import-exports-nothing, reconcile-ignores-age-deletes) plus old-arriving-row applied-then-pruned case.
- Step 4 — deliverable: wiring named (daemon `Tick`/`idleSync`/`queueSync` prune slot, `push`/`pull` steady, join snapshot path carries history rows, unlock drains them like any row batch); check: focused `make check-quick PKGS='./internal/synclog/...'` + `PKGS='./internal/db/...'` + `PKGS='./internal/availability/...'`, new tests re-run with `PATH` holding only `git`.
- Named tests: `TestImporterRefusesACrossOriginHistoryRow`, `TestHistoryPruneExportsNothing`, `TestOldHistoryRowImportsWithoutReexport`, `TestReconcileIgnoresAgedHistoryDeletes`. Gate: `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`.

### Round 3 — readers combine all origins, behaviour stays own, kv migration + kv removal, join

- Step 1 — deliverable: `LoadHistory`/`LoadLatency` (or same-named successors) read combined all-origin table rows, pruned to 30d, into the unchanged `History`/`LatencyHistory` shapes so `HourCounts`, `BlockedDurations`, `Summary`, `StatsInputs`, `formatHistory`/`peakText`, `formatCandidates` need no signature change; check: `TestStatsInputsAssemblesFromDB`-shaped case with two origins' rows combined in `Rows`/`History`/`TTFT`.
- Step 2 — deliverable: behaviour pin — `resolveCandidate`/`resolveRole`, `LedgerGates` switch/send/status paths, `spawn_failed` locality keep reading own ledger/own rows only (peers' history never gates a pick, schedules a probe, or lifts a gate); check: `TestPeerHistoryNeverGatesAPick` (peer outage + slow peer samples present, own pick unchanged; fails if readers feed picks from combined rows) and `TestSpawnFailedStaysLocal`-shaped gates-doc case.
- Step 3 — deliverable: idempotent kv→table migration (own `origin`, fresh ULIDs in `at` order, marker-gated, exports like new history) then kv `availability`/`latency` row deletion with all readers/writers off kv; SCOPES.md drops the two kv rows here; check: double-run migration idempotent, migrated rows visible on peer after a memfake steady round, `TestOutboxLeavesLocalOnlyTablesAlone`-shaped assertion that kv writes record nothing.
- Step 4 — deliverable: join test through memfake — fresh joiner receives peers' history rows and its own migrated rows export; joiner sees both origins' rows and pushes none of the peers' back (echo check via `owned()` + head assertion as in `TestJoinWithHistoryOnBothSides:146-202`); check: `TestJoinReceivesPeerHistoryAndExportsOwn`.
- Named tests: `TestStatsInputsCombinesBothOrigins`, `TestPeerHistoryNeverGatesAPick`, `TestHistoryMigrationMovesKvOnce`, `TestJoinReceivesPeerHistoryAndExportsOwn`. Gate: `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`; regenerate `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` iff code moved between packages, said so in the report.

### Round 4 — docs to what was built, final gate

- Step 1 — deliverable: spec §2 table + §4.2 amended to the built tables/retention/migration/reader split (three sentences max per section, no history in code); check: `TestSharedTablesMatchTheScopesClassification` + drift test green against final SCOPES.md.
- Step 2 — deliverable: full gate once (`RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`) plus focused suites re-run only if a fix changed code (chain re-runs the gate anyway); `cmd/relevo` tests touch memfake/pure rules only, no Turso/R2, no harness spawn.
- Named tests: none new (docs only). Gate: `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`.

## What is deleted when the work deletes behaviour (closed list)

1. The kv `availability` document rewrite-on-append path (`SaveHistory` + `loadHistory` kv reads in `internal/availability/history.go:47-74` and callers `gates.go:68-74,271-277`) — replaced by per-event `INSERT`s; kv row deleted after migration.
2. The kv `latency` document rewrite-on-append path (`SaveLatency`/`LoadLatency` kv reads in `internal/availability/latency.go:44-72` and caller `probe.go:277-283`, `candidates.go:103-112`, `statsinputs.go:34-49`) — replaced by per-sample `INSERT`s; kv row deleted after migration.
3. In-memory-only 30-day pruning as the sole retention (`History.Prune`/`LatencyHistory.Prune` as the enforcement point at `gates.go:35`, `probe.go:282`) — replaced by SQL age deletes plus read-time prune; the pure `Prune` helpers stay as the read-time filter.
4. `SCOPES.md` kv `availability`/`latency` machine-local rows — removed when the kv rows are deleted (tables added in Round 1).

## What each round's report must include

- Files changed vs this plan's seam list; any seam added or dropped and why.
- Named tests run (focused command + full gate), host-independence proof (`PATH` holding only `git`), mutation result for every refusal and every never-exported rule (test fails with the rule removed).
- Production-wiring statement: where daemon, join, unlock and sync verbs reach the new path, and which memfake two-machine steady run proved it.
- Coverage baseline: regenerated or not, and why; style checks (`gofmt`, `go vet`, `golangci-lint`, `check-comments.sh`, `check-filesize.sh`) green.
- Which of the two seed/code contradictions (§trigger-skip, §prune-path) the round resolved and how; writer-check drop semantics confirmed.
- `not_done`: secrets (step 5) and ownership (step 6) untouched; no remote format change; no `AGENTS.md`/`CLAUDE.md` process change.
