# Sync step 3b: shared docs for config

## MasterMind corrections (binding; where these disagree with the plan below, these win)

C1. **Two files, no shared transaction.** `relevo.db` and `relevo-local.db` are separate handles (`OpenSplit`, no ATTACH), so no transaction spans both.
- `config_doc`, `config_revision`, and every new 3b table (`shared_outbox`, `shared_export_state`, `shared_applied`, `shared_pending`, `config_local`) and the hybrid-clock kv live in the LOCAL file. Bind them through `LocalOrSelf()`. `sync_import_mark` stays where it is.
- The importer therefore applies, or parks, a batch's doc entries in ONE local-file transaction that runs BEFORE the shared-file transaction that applies the batch's rows and moves the mark.
- A doc apply must be idempotent, so a crash between the two transactions re-delivers the entry and changes nothing:
  - an entry whose (stamp, origin) equals `shared_applied`'s for that key is a no-op, and writes no second revision;
  - `shared_pending` is keyed by (origin, seq).
- Test it: fail the mark transaction once, re-import, and assert one revision and the same config.
- Wherever the plan says "inside the batch transaction" for doc state, read "in the local transaction before the mark moves".

C2. **Tombstones.** A doc delete is an `OpUpsert` whose Body is `{"stamp": "...", "value": null}`. It is never an `OpDelete`: 3a's shape rule forbids a body on a delete, and the stamp has to travel. `Apply(key, nil)` deletes the key.

C3. **Machine-field move.** Moving machine fields from `config_doc` into `config_local` is a Go step, not SQL. It is idempotent and recorded by a local kv marker. The exporter refuses to export the config doc until the marker is set. Run it at store open or daemon start, whichever is reached first, and say which.

C4. **Validate the merged config.** `Apply` validates the merged (shared + local) prospective config, and only ever writes the shared layer, never `config_local`. An item that would make the merged config invalid is recorded as `sync-superseded:<label>` and changes nothing.

C5. **Writes and reads.** `Put`, `PutDoc`, `Delete` and `Rollback` strip machine fields from the shared body and write them to `config_local`. `Load`, `config get`, `config export` and `relevo config` all show the merged view. Existing callers see exactly what they saw before, which existing tests must keep proving.

C6. **Deferred.** The "hook argv outside the config root" machine field (§4.1) is 3c, with hook files. Do not build it here.

C7. **The hybrid clock** observes every doc stamp the importer sees: applied, superseded or parked.

C8. **Migrations.** Round 1 creates the tables only, in `025_shared_docs.sql`. The `config_doc` -> `shared_outbox` triggers go in a separate `026_shared_outbox_triggers.sql` in round 3, so no outbox fills before an exporter exists. Update the schema goldens and drift tests in the round that changes them.

C9. **Routing.** `SharedTable()` keeps refusing `doc:` tables, and doc entries are taken out of a batch or region BEFORE row apply. In the snapshot path, doc entries leave the region before `splitRegion`, and the latest entry per key goes through the doc path.

C10. **An applied config change takes effect as a local `relevo config set` does.** Use the same version bump and the same post-write path, so no restart is needed. Name the function the round uses.

C11. **Commits.** Commit your work on the branch before the report; never leave it uncommitted.

---


Saves to: `docs/plans/2026-10-12-sync-shared-config.md`. Spec: `docs/specs/2026-10-10-sync-everything-design.md` §4 (intro, change capture, hybrid clock, apply) and §4.1. Step 3a (merged, `7bd68c2a`) signs log entries (§5; `internal/synclog/{mac,sign,verify}.go`). 3b covers the SharedDoc framework plus ONE instance, config. Gates, consents, files, agent re-render are 3c. Five builder rounds, each a new commit (never amend/rebase a pushed branch).

Seed decisions (not reopened): doc entries ride the existing log as ordinary entries (`doc:<name>`, `PK = json_array(item key)`, upsert/delete, `Body = {"stamp","value"}`); importer routes `doc:` tables to a SharedDoc registry in normal and snapshot paths, reconcile ignores doc tables; signed-only with locked-reader `shared_pending` hold; 64-bit hybrid clock (`wall ms << 16 | counter`, kv-persisted, observe-moves-past, ties to higher origin); `shared_outbox(doc)` trigger + exporter diff vs `shared_export_state` + `shared_applied` self-write; apply-with-export-state-write to suppress echo, losers to `config_revision` as `sync-superseded:<label>`, applied as `sync:<label>`; key split (per-item: candidates/agents/actors/accounts/servers as `<section>/<name>`; per-section: policy/roles/prices/hooks/workflows; `sync` never syncs); `config_local` merge-patch layer + `Load` returns shared+local, machine-field routing, `--local` flag, pre-export migration; first-export stamps from `config_doc.updated_at`, join clears export state; no remote format change.

Seed-vs-code check: no contradictions found. `config_doc` lives in the machine-local file after the split (`internal/config/config.go:115`, `local_split_test.go`), so a `shared_outbox` trigger in `relevo-local.db` fires where `config_doc` writes land. `Entry` (`internal/synclog/entry.go:46-73`) already carries Table/PK/Op/Body/MAC/At, so `doc:` entries with the stamp inside Body need no transport or remote change; `MemTransport` (`internal/synclog/memfake.go:28-60`) already stores any table name. `SharedTable()` (`entry.go:133-140`) only knows `db.SharedTables`, so the importer must route the `doc:` prefix before that check — new code, not a conflict.

## Behaviour and cases

1. Hybrid clock: monotonic across restarts (kv-persisted in local file); `Now()` never goes backwards; `Observe(stamp)` moves past it; first-export path stamps from `config_doc.updated_at`, never `Now()`.
2. Registry: `SharedDoc` interface (`Name`, `Items`, `Apply`) in `synclog`; `doc:`-prefixed tables route to it in `applyBatch`/`applyEntries` (`import.go:322-383`), in `snapshotOrigin`/`splitRegion`/`applyUntilDone` (`snapshot.go:82-252`), and are skipped by the reconciler walk (`reconcile.go:265-330`). Unknown doc name, malformed `doc:` table, non-array PK, bad stamp shape, non-object body are refusals (each with its own failing-without-it test).
3. Config doc: `Items()` splits per decision 6; `Apply()` writes one key through section validation plus whole-doc `validateProspective` (`document.go:101-104`); an item that fails validation never lands and never breaks `Load`.
4. Local layer: `Load` returns shared + `config_local` merge patch; writes to `accounts.*.config_dir`, `accounts.*.home`, `policy.serve.*` land local by default; `--local` on `config set/edit/unset` sends any edit local; migration moves those fields from `config_doc` to `config_local` before anything exports.
5. Export: trigger records only which doc changed; exporter diffs `Items()` vs `shared_export_state`, emits one signed entry per changed key with a fresh stamp, records `shared_applied=self`; locked machine emits no doc entries and keeps the outbox; export-state write on apply suppresses echo; first export stamps from section `updated_at`; join clears `shared_export_state`.
6. Import: unlocked reader refuses unsigned/bad-MAC doc entries (whole batch held per origin, mark unmoved); locked reader parks unverified doc entries in `shared_pending` and moves the mark; on unlock and on every keyed import, pending entries verify then apply-or-refuse; newer-than-`shared_applied` (tie: higher origin wins) applies in the batch transaction and lands `config_revision` `sync:<label>`; older or validation-failing lands `sync-superseded:<label>` with config untouched; every applied key also writes `shared_export_state` in the same transaction.
7. Convergence: three machines with concurrent config edits converge, loser in `config_revision`; no echo storms (apply does not re-export).
8. Threat (§5/3a model): doc entries are attacker bytes until verified — verify-before-apply, key/body shape validation, `Load` never breaks.

## Seams

- `internal/db/migrations/002_config.sql:9-13` (`config_doc`), `022_sync_outbox.sql:30-47` (trigger pattern), `024_sync_signed_from.sql:12-15` (local-table pattern); new `025_shared_docs.sql`.
- `internal/db/shared_tables.go:22-38` (`SharedTables` — doc tables are NOT added here); `internal/db/sync_exchange.go:263-341` (`DrainOutbox`), `525-559` (`ResolveOwner` — never consulted for `doc:` tables); `internal/db/config.go:25-66` (`ConfigGet/Put/Delete`), `68-96` (version).
- `internal/synclog/entry.go:46-108` (`Entry`, `NewUpsert/NewDelete`), `133-174` (`SharedTable`, `KeyOf`); `export.go:24-61` (`Exporter`), `100-153` (`ExportBatch`, `build`); `import.go:208-278` (`Import`), `322-413` (`applyBatch`, `applyEntries`, `mark`, `past`), `450-522` (`applyEntry`, `owns`); `snapshot.go:46-123` (`snapshots`, `snapshotOrigin`), `203-252` (`splitRegion`, `applyUntilDone`); `reconcile.go:105-124` (`NewReconciler`), `265-330` (`differences`, `nextUpserts`); `transport.go:17-105` (`LogTransport` — unchanged); `sign.go:24-92` (`keyed`, `sign`, `guard`, `appendTo`); `verify.go:31-127` (`loadSigned`, `verify`, `recordSigned`); `mac.go:25-68` (`EntryMAC`, `VerifyMAC`); `codec.go:37-106` (`EncodeBody`, `DecodeBody`); `memfake.go:28-124` (`MemTransport`, append numbering).
- `internal/config/config.go:26-43` (`Sections`), `102-138` (`Store`, `Open`, `Load`), `383-495` (`Put`, `Delete`, `PutDoc`), `336-379` (`Validate`); `document.go:12-69` (`decodeDoc`), `101-104` (`validateProspective`); `revision.go:71-135` (`record`), `182-231` (`Rollback`); `sectionread.go:8-22` (`Body`, `Has`).
- `internal/sync/steady.go:98-149` (`attempt`, `exchange`), `226-243` (`drainImport`); `join.go:125-179` (`Preflight`), `333-393` (`join`, `FinishJoin`); `unlock.go:34-66` (`Unlock`).
- `cmd/relevo/config_edit.go:75-152` (`configSet`), `154-237` (`configUnset`), `243-337` (`configEdit`), `365-387` (`configPath`).
- `docs/specs/2026-10-10-sync-everything-design.md` §4/§4.1/§5; `testdata/coverage-baseline.txt`; `scripts/check-coverage.sh`, `scripts/check-comments.sh`, `scripts/check-filesize.sh`.

## Ordered steps (one line each: deliverable + how it is known good)

1. (R1) Migration `025_shared_docs.sql` delivering `shared_outbox`, `shared_export_state`, `shared_applied`, `shared_pending`, `config_local` as local-only trigger-free tables — known good when the schema golden and `TestSharedTablesMatchTheScopesClassification`-style drift tests still pass unchanged.
2. (R1) Hybrid clock in `synclog` backed by local kv delivering monotonic `Now`/`Observe` with restart persistence — known good when a restart-then-observe sequence test shows the clock never regresses.
3. (R1) `SharedDoc` registry in `synclog` delivering `doc:` prefix routing helpers plus refusal of unknown doc / malformed table, PK, stamp, body — known good when each refusal has a test that fails with the check removed.
4. (R1) Doc-entry shape on the existing log delivering `NewDocUpsert/NewDocDelete` constructors with `{"stamp","value"}` bodies that verify under the 3a MAC — known good when a round-trip test shows `VerifyMAC` holds and `Head`/`Pull` carry them untouched.
5. (R2) Config `Items()` delivering the decision-6 key split (per-item vs per-section, `sync` excluded) — known good when an items-shape test lists exactly the expected keys for a seeded config.
6. (R2) Config `Apply()` delivering single-key write through section validation + whole-doc prospective check that never breaks `Load` — known good when an invalid-item test leaves `Load` valid and records nothing.
7. (R2) `config_local` layer delivering shared+local `Load`, default-local machine fields, and `--local` on set/edit/unset — known good when a merge test shows local wins and shared is untouched.
8. (R2) Pre-export migration delivering machine-field move from `config_doc` to `config_local` — known good when a pre/post migration test shows the fields moved and the shared doc no longer carries them.
9. (R3) `shared_outbox` trigger on `config_doc` delivering doc-dirtied signals only — known good when a config write test shows one signal row and no payload in the outbox.
10. (R3) Exporter doc-diff delivering one signed entry per changed key with fresh stamps, `shared_applied=self`, echo suppression, first-export `updated_at` stamps, locked-exports-nothing, and reconciler ignoring doc tables — known good when a two-export test shows the second export is empty and a locked-export test appends nothing.
11. (R3) Join clearing `shared_export_state` delivering re-export after join — known good when a clear-then-export test re-emits every key.
12. (R4) Importer doc-route delivering verify-first routing in normal and snapshot paths with unsigned/bad-MAC refusal holding the origin — known good when a bad-signature test holds the origin with the mark unmoved.
13. (R4) Locked `shared_pending` delivering unverified park + mark movement, drained on unlock and on every keyed import — known good when a lock-import-unlock test applies pending only after the key arrives.
14. (R4) Newest-wins apply delivering stamp/origin comparison, `sync:` vs `sync-superseded:` revisions, in-transaction export-state write, and three-machine convergence — known good when a concurrent-edit converge test leaves one state with the loser revisioned.
15. (R5) CLI/config surface + spec amend delivering `--local` on set/edit/unset, `config log` showing `sync:` sources with working rollback, §4/§4.1 amended to what was built, coverage baseline regenerated via `sh scripts/check-coverage.sh --write` iff code moved packages — known good when `GOTOOLCHAIN=go1.27.2 make check` and `RELEVO_REQUIRE_COVERAGE=1` comparison both pass with no lowered baseline.

## Rounds (files, named tests, gate)

- Round 1 — foundation (steps 1-4). Files: `internal/db/migrations/025_shared_docs.sql`, `internal/db/*test*` goldens only, new `internal/synclog/shareddoc.go` + `hlc.go`, `internal/synclog/entry.go` (constructors only). Named tests: `TestSharedDocTablesStayOutOfSharedTables`, `TestHybridClockNeverRunsBackwards`, `TestHybridClockObserveMovesPast`, `TestHybridClockSurvivesRestart`, `TestUnknownSharedDocIsRefused`, `TestDocEntryWithBadStampIsRefused`, `TestDocEntrySurvivesTheLogUnchanged`. Gate: `GOTOOLCHAIN=go1.27.2 make check`.
- Round 2 — config doc + local layer (steps 5-8). Files: `internal/config/sharedoc.go`, `internal/config/config.go` (`Load`, `Put`, `Delete`, `PutDoc`), `internal/config/document.go`, new `internal/db/migrations/026_config_local_migrate.sql` (or folded into 025 if R1 left it placeholder — R2 decides and says so), `cmd/relevo/config_edit.go` (`--local` only). Named tests: `TestConfigItemsSplitPerItemAndPerSection`, `TestSyncSectionNeverSyncs`, `TestConfigApplyRefusesAnInvalidItem`, `TestAppliedItemNeverBreaksLoad`, `TestLocalLayerWinsOverShared`, `TestMachineFieldsLandLocalByDefault`, `TestLocalFlagSendsAnyEditLocal`, `TestMigrationMovesMachineFieldsBeforeExport`. Gate: `GOTOOLCHAIN=go1.27.2 make check`. CLI tests assert flag routing only; rule tests live in `internal/config` (CI has no harness/network).
- Round 3 — export (steps 9-11). Files: `internal/db/migrations/025_shared_docs.sql` (trigger half), `internal/synclog/export.go`, `internal/synclog/reconcile.go` (doc skip), `internal/sync/join.go` (clear export state). Named tests: `TestConfigWriteDirtiesOnlyTheDocSignal`, `TestExporterEmitsOneEntryPerChangedKey`, `TestFirstExportStampsFromUpdatedAt`, `TestAppliedEntryDoesNotReexport`, `TestLockedMachineExportsNoDocEntries`, `TestJoinClearsExportState`, `TestReconcileIgnoresDocTables`. Gate: `GOTOOLCHAIN=go1.27.2 make check`.
- Round 4 — import/apply (steps 12-14). Files: `internal/synclog/import.go`, `internal/synclog/snapshot.go`, `internal/synclog/verify.go` (doc hookup only), `internal/sync/unlock.go` + `internal/sync/steady.go` (pending drain wiring), `internal/synclog/memfake.go` (test helpers only — transport itself unchanged). Named tests: `TestUnsignedDocEntryIsRefusedWhenUnlocked`, `TestBadDocSignatureHoldsTheOrigin`, `TestLockedReaderParksDocEntriesAndMovesItsMark`, `TestUnlockDrainsPending`, `TestKeyedImportDrainsPending`, `TestNewerStampWinsTieGoesToHigherOrigin`, `TestOlderEntryLandsSuperseded`, `TestInvalidAppliedConfigLandsSuperseded`, `TestThreeMachinesConvergeWithLoserRevisioned`. Gate: `GOTOOLCHAIN=go1.27.2 make check`. All tests against memfake; no `cmd/relevo` test touches Turso/R2.
- Round 5 — surface + spec (step 15). Files: `cmd/relevo/config_edit.go`, `cmd/relevo/config.go` (log text only), `docs/specs/2026-10-10-sync-everything-design.md` (§4/§4.1 amend), `testdata/coverage-baseline.txt` (only if regenerated), `docs/plans/2026-10-12-sync-shared-config.md` (this plan, saved verbatim). Named tests: `TestConfigLogShowsSyncSources`, `TestRollbackRestoresASyncedRevision`. Gate: `GOTOOLCHAIN=go1.27.2 make check` plus `RELEVO_REQUIRE_COVERAGE=1` comparison; report states whether the baseline was regenerated.

## What is deleted (closed list)

1. Machine-specific fields (`accounts.*.config_dir`, `accounts.*.home`, `policy.serve.*`) are removed from the shared layer by the R2 migration; they remain readable via the local layer, so no value is lost.
2. A bare `config set/edit/unset` (no `--local`) on a machine field no longer writes the shared section; the old destination (shared write) is deleted in favour of the local write.
3. Nothing else is deleted: no CLI verb, no revision source, no transport method, no shared table, and no 3a signing behaviour is removed or weakened.

## Constraints every round obeys

- Style per `CLAUDE.md`: why-only comments, no history/issue/round numbers in code or test names, functions ≤70 lines, files ≤600, one package per concept; no new `golangci`/`check-comments.sh`/`check-filesize.sh` exclusions.
- `cmd/relevo` tests never reach Turso/R2 or spawn a harness; sync rules tested as pure functions or against memfake.
- Every refusal ships with a test that fails without the refusal; coverage baseline never lowered to get green.
- Spec §4/§4.1 amendment happens only in round 5, to what was actually built.

## What the report must include

- Diff stat vs the round's declared file scope; every deviation named.
- `GOTOOLCHAIN=go1.27.2 make check` output (and round 5: coverage comparison result + whether the baseline was regenerated and why).
- For each named test: what it pins and confirmation it fails with the logic removed (mutation check).
- Refusals added, with the attacker/malformed input each one answers (3a threat model).
- What was deliberately left for a later round; nothing silent.
