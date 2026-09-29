# Delete the relay→relevo migration machinery (relevo#411)

Owner decision: no relay-era install is upgraded any more. A pre-0.13 install gets a fresh relevo, never a migration. This plan removes `relevo migrate`, the unmigrated-state guard, the relay-era readers and every import-on-presence path from before the database. It is **removals only**: a machine that already migrated sees no change in live behaviour.

Line numbers are from HEAD `9fe6d5cb`. If a line has moved, find the named function instead. Halt and report rather than improvise when a step is impossible as written or contradicts the code. Never delete a test just to get green. A test whose *subject* is a deleted behaviour is on the closed list below. A test that only used a legacy file as a convenient way to seed state is retargeted to seed through the database.

Commit this plan as `docs/plans/2026-09-29-drop-relay-migration.md` in the last phase's commit. Plans ship with their implementation.

## 0. Where the seed disagrees with the code

1. **`probeFailedRenameRow` location.** It is in `cmd/relevo/doctor.go:239-249`, not `doctor_checks.go`. The call block is `doctor.go:298-310`. `doctor_checks.go` has only a comment about "no migrate row" (lines 27-28), which is about db migrations and stays.
2. **`store.MasterMindsDir` has to stay.** `AgyCredsDir` (`internal/store/paths.go:309`) is `planners/.agy`, so the directory is live. Only `DBRegistry.Root` and its import go. `MasterMindsDir`'s comment is rewritten to say it is the agy credentials' parent.
3. **`ingest.DedupeStats.TranscriptRowsRenamed` is stranded too.** The field is at `internal/ingest/dedupe.go:28`, it is incremented at line 237, and `cmd/relevo/daemon.go:215` logs it. It goes along with the `renames` parameter. The field is stored as JSON in kv, so an old row that still carries the key decodes fine.
4. **Legacy `bind.json` / `log.jsonl` readers that stay.**
   - `internal/ingest` (`DirSource` / `StoreSource`, `source.go`) is the live ingest mirror, not an import-on-presence path. It is out of scope and untouched.
   - The runner-rename fallbacks (`NNN-builder.jsonl`, `NNN-builder.log`, the `*PreRename*` tests) are a different rename and also stay.
   - So do agy credential file adoption (`delivery/agy_creds.go`), the `~/.config/relevo/*.json` import (`internal/config/import.go`), `internal/db/migrate.go`, the store's `held`/`orphaned` state mapping (`lifecycle.go:357-363`) and `scripts/rename-mastermind.sh` / `scripts/rename-relevo.sh`.
5. **Two "no live behaviour change" risks the builder cannot check.** The MasterMind checks both before install and deploy (§6):
   - a client key stored with the `RELAY ED25519 PRIVATE KEY` PEM header. Nothing ever rewrote it, and after this change `remote.ParsePrivate` refuses it, so every remote round would fail.
   - contabo's unit, whose `ExecStartPre=relevo migrate` lines become an unknown verb and would stop the unit from starting.

## 1. The deletions and what they touch

**Phase A: the verb, the guard and the relay-era readers.** After Phase A, `internal/legacy` and `internal/migrate` are gone.

- **The verb.**
  - `cmd/relevo/migrate.go` and `migrate_test.go` are deleted, and so is all of `internal/migrate/`.
  - `cmd/relevo/main.go`: the `legacy` import (line 17), the usage line (63), the guard block (140-150) and the `case "migrate"` (196-197).
- **The guard.**
  - `cmd/relevo/rename.go` and `rename_test.go` are deleted (`renameRoots`, `guardExempt`, `refuseUnmigrated`).
- **The doctor rename row.**
  - `internal/doctor/rename.go` and `rename_test.go` are deleted.
  - `cmd/relevo/doctor.go`: the `legacy` import (22), `probeFailedRenameRow` (239-249) and the row block (298-310).
- **The dist Go package.**
  - `dist/dist.go` and `dist/dist_test.go` are deleted.
  - `relevo.service`, `relevo-serve.service` and `com.github.fuad-daoud.relevo.plist.in` stay, because `make service` and `scripts/relevo-service-template_test.sh` read them from disk.
- **The db path rewrite.** `internal/db/rewrite.go` and `rewrite_test.go` are deleted.
- **The dedupe renames.**
  - `internal/ingest/dedupe.go`: the `renames []legacy.Prefix` parameter is dropped from `DedupeMirror` (97), `planBinding` (149), `planTranscript` (198), `streamLinesCover` (249), `streamCoversRecord` (294) and `DedupeMirrorOnce` (481).
  - `streamCoversRecord` loses the `RewriteJSON` arm and its `renamed` result, and `TranscriptRowsRenamed` goes.
  - `cmd/relevo/daemon.go:186-216` loses its `renameRoots` / `Prefixes` block and the log attribute.
  - In the tests: `internal/ingest/helpers_test.go` loses `rehearsalRenames` (334-353), and its caller `dedupe_test.go:486` passes nothing. `dedupe_test.go`'s `TestDedupeMirrorPlansRenamedStreamLines` (216) is deleted, and the `TranscriptRowsRenamed` assertions at 181-182 go.
- **The relay-era trailers.**
  - `internal/proc/proc.go`: `ExitCode` (340-362) and `Rusage` (411).
  - `internal/proc/scope.go`: `ParseRusageTrailer` (153-165).
  - `internal/store/seal.go`: `StreamDrained` (245) and `trailerLinesOnly` (270-285).
  - `internal/transcript/trailers.go`: the two legacy constants and their `trailerPrefixes` entries.
  - `internal/usage/source.go`: `legacyExitTrailer`, `LegacyExitTrailerForTest` (72-77) and the `streamClosed` arm (106).
  - `internal/relevo/gate.go`: the `tailLines` arm and its comment (193-204).
  - Only the relevo spellings remain everywhere.
- **The legacy PEM type.** `internal/remote/key.go:102-112` accepts only `pemTypePrivate`.
- **The ledger source rewrite.** `internal/availability/ledger.go:104-106,136-140` is deleted. A stored `"relay"` source entry now falls into `Ledger.Other`, which is kept raw and never lost.
- **The serve pointer file.**
  - `internal/serve/pointer.go`: `PointerFileName` (14-15) and `ReadPointer` (68-81) are deleted, along with `pointer_test.go`'s `TestReadPointerFile` (47).
  - `cmd/relevo/gate.go:162-170`: the "a serve daemon runs here" note reads the machine database instead. It uses `openMachineDB()` (`serve.go:101`), `serve.ReadDaemonPointer(d)`, `pidAlive(p.PID)` and closes `d`.
  - If `openMachineDB` fails, the note is silently skipped, as today. The comment's "pointer lives under the serve root / daemon.json" history goes.
- **`store.ListFiles`.** `internal/store/lifecycle.go:396-432` goes, together with `loadBindingFile` if nothing else uses it.
- **`internal/legacy/`** is deleted last.
- **Guards and docs.**
  - `scripts/check-name.sh:37`: the `internal/legacy/**` exclude. `scripts/check-name_test.sh:117-125`: its scenario.
  - `.golangci.yml:33-34`: the `^internal/legacy/` exclusion. `.golangci.yml:27-28`: `^dist/`, which now has no Go files.
  - `scripts/check-comments.allow` lines 28, 29, 34, 48, 53, 54. Stale entries are skipped silently, but they are dead.
  - `testdata/coverage-baseline.txt` lines 21 (`internal/legacy`) and 24 (`internal/migrate`).
  - `README.md`: the "Upgrading from relay" section (100-123, including its `name-guard off/on` markers) and the `relevo migrate` verb entry (429-430).

**Phase B: the pre-database binding files and archive tarballs.**

- **`internal/store/db.go`.**
  - `importPresent` (71-143), `readIfPresent`, `statIfPresent`, `importBindingFile`, `importLogFile`, `adoptViewed`, `importAll` (239-274), `warnNewerFormatOnce`, `legacyPresent`, `anyLegacyPresent` (286-326).
  - `splitLogLines` and `decodeLog` only if `unused` flags them. `recordEventsOf` stays, because `fork.go:156` uses it.
- **Every `importPresent` / `importAll` call site.**
  - `lifecycle.go:174, 244, 282, 324, 369, 476`, `log.go:290, 344, 371, 477` and `paths.go:225, 243`.
  - `save` returns nil after `RecordPut`. `saveWithLog` loses both adoptions.
- **The locked-read path.**
  - `lifecycle.go:108-129`: `read` / `readAll` lose the presence probe and run `fn(&Tx{s: s})` directly. Inline them if they become trivial.
  - The fork comments about a "waiting dst/log.jsonl" (`lifecycle.go:172-173, 241-242`, `fork.go:155`) are reworded or dropped.
- **Paths.**
  - `paths.go:216-220` `ViewedPath`, and `bindingPath` (285-287) if unused.
  - `logPath` (`log.go:203`) if unused.
  - The `ScratchWorktreeDir` comment (329) stops naming `ListFiles` / `importAll`.
- **The archive tarballs.**
  - `internal/store/archive.go`: `importTarballs`, `importTarball`, `importArchivedRecord`, `importTarballLog`, `importTarballRoundFiles`, `tarballWarned` / `warnTarballOnce`, `parseArchiveStamp`, `tarFile`, `readTarFiles` (160-415).
  - `ListArchived` (50-56) no longer imports, and its comment follows.
  - `paths.go:289-295`: `ArchiveDir` and `archiveStampLayout`. `store.go:36`: `archiveDirName`.
  - `internal/serve/daemon.go`: the `importOwnerTarballs` startup call (77) and the function (108-135).

**Phase C: the legacy kv files, the serve imports and the dead parameters.**

- **`db.KVImportFile`** (`internal/db/kv.go:179-215`) is deleted after all of its callers. `kv_test.go`'s five `TestKVImportFile*` tests (89-190) go, and so does `helpers_test.go`'s one use.
- **Ledger, availability history and latency.** The parameters go from `availability.LoadLedger(kv)`, `loadHistory(kv)` (including the `history.json` fallback) and `LoadLatency(kv)`. These use `KVGet` directly.
  - `LegacyGatesPath` (`gates.go:22-31`) is deleted.
  - `Deps.GatesDir` (`deps.go:21`) and `relevo.Runtime.GatesDir` (`runtime.go:134-137, 335`) are deleted.
  - `cmd/relevo/candidates.go:105, 122-126` (`legacyGatesPath`) goes.
  - Call sites: `gates.go:41, 73, 89, 197, 231, 257`, `probe.go:278`, `relevo/statsinputs.go:35`, `relevo/unused_gates.go:20`, `cmd/relevo/wire.go:314-324, 357`, `cmd/relevo/serve.go:235`, `internal/serve/serve.go:232` and `internal/serve/admin.go:397`.
- **The rest of the kv files.**
  - `daemon.json`: `store.ReadDaemonInfo` reads with `KVGet`. `daemonInfoPath` and `daemonInfoFileName` (`daemoninfo.go:47`, `store.go:47-49`) go.
  - `ui.json`: `ui.PrefsStore.LegacyPath` (`prefs.go:29-50`) goes. Call sites are `cmd/relevo/main.go:317` and `serve_admin.go:279`.
  - `release-check.json`: `release.Load(kv)` (`cache.go:26-47`). Call sites are `doctor/env.go:159` and `relevo/daemon.go:246`.
  - `agents-manifest.json`:
    - `harness.ReadManifest(kv)` (`manifest.go:25-45`).
    - `OSInstallEnvKV(kv)` drops `legacyPath`, and `manifestPath` goes (`install.go:350-370, 402`).
    - Call sites are `relevo/installenv.go:31` and `doctor/env.go:114`.
  - `planners/`: `mastermind.DBRegistry.Root`, `ensureImported` and `importFiles` (`registry.go:51-112`). `cmd/relevo/mastermind.go:92` stops passing it.
  - `channels/`: `delivery.KVClaims.Root`, `ensureImported` and `importFiles` (`channel.go:62-125`). `store.ChannelsDir` (`paths.go:299-301`) and `cmd/relevo/wire.go:325` go.
  - `hooks.log`: `hooks.KVLog.Root`, `legacyLogName`, `ensureImported`, `importFile`, `fileModTime` and `lastBytes` if unused (`runlog.go:17-135`). `NewKVLog(kv)` drops the root. Call sites are `cmd/relevo/wire.go:48`, `serve.go:462` and `doctor.go:422`.
- **The serve imports.**
  - `serve.LoadClients(kv)` drops `legacyPath` (`clients.go:78-92`). Call sites are `internal/serve/serve.go:108-109` and `cmd/relevo/serve_admin.go:99, 135, 167`.
  - `serve.SecretStore.Root` and `importLegacy` (`tls.go:30-105`) go, along with the three calls at 114, 187 and 231. Call sites are `cmd/relevo/serve.go:532` and `serve_admin.go:50, 199`.
  - `serve.Initialised` (`pointer.go:20, 83-117`): the markers reduce to the `bindings` directory beside the kv row and the TLS secret, so `initialisedMarkers` goes.
- **Docs.** In `README.md`, these sentences go:
  - 950-951 and 996-997: the `.archive` tarball import.
  - 1574: "Older installs are migrated on first read".
  - 2108: "A legacy `<root>/hooks.log` is imported once and removed".
  - Keep 995's `~/.config/relevo` sentence and 2088, which are the config import.

## 2. Ordered steps

The focused loop per step is `go build ./... && go vet ./...` plus the step's `go test` line, run with `-count=1`, and every reported error is fixed before the next run. Each phase ends with `make check`, `sh scripts/check-name.sh` and `sh scripts/check-name_test.sh`, then one commit.

- No test may spawn a harness or reach the network.
- Tests in `cmd/relevo` touched here stay pure: the guard and migrate tests are deleted, not rewritten to execute verbs.
- `golangci-lint`'s `unused` check is the list of stranded helpers. Delete what it flags in the files named here, and nothing it flags elsewhere.

### Phase A

1. **Drop the verb and the guard.**
   - Delete `cmd/relevo/{migrate,rename}{,_test}.go` and `internal/migrate/`.
   - Edit `main.go` (import, usage line, guard block, case) and `doctor.go` (import, row, `probeFailedRenameRow`), and delete `internal/doctor/rename{,_test}.go`.
   - Delete `dist/dist.go` and `dist/dist_test.go`, then `internal/db/rewrite{,_test}.go`.
   - Verify: `go build ./... && go test ./cmd/relevo/ ./internal/doctor/ ./internal/db/ -count=1`. `relevo migrate` now answers the unknown-verb path; confirm it via main's dispatch default, without a test that runs it.
2. **Drop the dedupe renames.** Edit `ingest/dedupe.go`, the `daemon.go` block and the ingest tests as in §1.
   - Verify: `go test ./internal/ingest/ ./cmd/relevo/ -count=1`.
   - Mutation check: make `streamCoversRecord` return true unconditionally, and confirm a named dedupe test fails. Restore it.
3. **Drop the relay-era readers.** Edit proc, scope, seal, transcript, usage, gate `tailLines`, remote key and the availability ledger.
   - Delete the tests pinning the legacy spellings (closed list items 10-16). Every other assertion in those table tests stays.
   - Verify: `go test ./internal/proc/ ./internal/store/ ./internal/transcript/ ./internal/usage/ ./internal/relevo/ ./internal/remote/... ./internal/availability/ -count=1`.
   - Mutation check: re-add the `relay-exit:` arm to `proc.ExitCode` temporarily, and confirm no remaining test notices. That proves nothing still pins it. Restore.
4. **Drop the pointer file and `ListFiles`.** Delete `serve.ReadPointer` and `PointerFileName`, and `TestReadPointerFile`. Switch `cmd/relevo/gate.go`'s note to the machine database. Delete `store.ListFiles` and `loadBindingFile`.
   - Verify: `go test ./internal/serve/ ./internal/store/ ./cmd/relevo/ -count=1`, then `git grep -n 'ReadPointer\b\|PointerFileName\|ListFiles'` finds nothing.
5. **Delete `internal/legacy/`, then the guards and docs.** Covers `check-name.sh` and its test scenario, the two `.golangci.yml` exclusions, the `check-comments.allow` lines, the two coverage-baseline lines, and the README section and verb entry.
   - Verify: `git grep -n 'internal/legacy\|internal/migrate\|legacy\.\(Name\|Prefix\|Roots\|ExitTrailer\|RusageTrailer\|KeyPEMType\|LedgerSource\)'` finds only `docs/` and `scripts/rename-mastermind.sh`.
   - Also `sh scripts/check-name.sh` is green (every remaining `relay` hit carries its marker), and `make check` passes.
   - **Commit A.**

### Phase B

6. **Remove binding-file adoption.** Edit `store/db.go`, the `importPresent` / `importAll` call sites, `read` / `readAll`, `ViewedPath` and the stale comments.
   - Retarget the seeding tests to write the record through the database. Use `db.RecordPut` with raw record JSON, via a small store test helper beside `bindingRecordJSON` in `helpers_test.go`:
     - `legacy_state_test.go` `TestLoadLegacyStates`
     - `store_test.go`: the 745-760 table (`TestLoadIgnoresRemovedLegacyKeys`), `TestLoadKeepsALegacyEdgesRecord` (768) and `TestLegacyPaneBindingReSavesByteIdentical` (792) if it seeds a file
     - `format_test.go:253-275` and `relevo/daemon_test.go:888` (a newer-format record row, not a file)
     - `log_test.go:137` (seed the entries through `SaveWithLog` / `AppendLog`)
     - `serve/admit_test.go:674` (make carol's list fail through an undecodable record row, not a broken file)
   - Delete the import tests (closed list 17-20) and `helpers_test.go`'s `seedLegacyDir` / `legacyFixture` once unused. `import_test.go` goes whole.
   - Verify: `go test ./internal/store/ ./internal/relevo/ ./internal/serve/ ./internal/ui/ ./cmd/relevo/ -count=1`.
   - Mutation check: make `read` take the lock unconditionally, and confirm nothing fails. Then make `save` skip `RecordPut`, and confirm a named store test fails. Restore both.
7. **Remove tarball import.** Edit `store/archive.go`, `ArchiveDir`, `archiveDirName`, `archiveStampLayout` and `serve/daemon.go`.
   - Retarget `archive_test.go`'s `TestReadFileResolvesTheMostRecentlyArchivedRecord` (222) and `TestArchivedLogDecodesEntries` (253) to seed through the live archive path (`Tx` archive, or `SaveWithLog` then archive). Delete the three import tests (closed list 21).
   - Delete `writeTarGz` from `store/helpers_test.go`, and `writeServeTarball` if unused.
   - Verify: `go test ./internal/store/ ./internal/serve/ -count=1`, then `git grep -n 'importTarball\|ArchiveDir\|\.tar\.gz' -- internal/store internal/serve` finds nothing.
   - Then `make check`. **Commit B.**

### Phase C

8. **Remove the gate-document imports and `GatesDir`.** Covers ledger, history and latency, their call sites, and the `Deps` / `Runtime` fields.
   - Delete the file-import tests: `history_test.go` `TestLoadKVImportsLegacyHistoryFile` (63) and its 114, 131 and 262 file cases; `ledger_test.go`'s file-path cases (70, 180, 295, 323, 350-365); `latency_test.go:24`. Retarget whichever of those pin decode or validation to seed the kv row.
   - Verify: `go test ./internal/availability/ ./internal/relevo/ ./cmd/relevo/ ./internal/serve/ -count=1`.
9. **Remove the other kv file imports.** Covers daemon info, UI prefs, release cache, the agents manifest, the mastermind registry, channel claims and the hooks log, plus their fields and parameters and every call site in §1 Phase C.
   - Delete: `TestReadDaemonInfoImportsLegacyFile`, `TestClaimsImportAdoptsChannelFiles`, `TestKVLogImportsLegacyFile`, the manifest's legacy-path cases (`install_test.go:635-666`), `release/cache_test.go`'s file cases, `prefs_test.go`'s `LegacyPath`, and `registry_test.go:266-337`'s import tests.
   - Retarget `TestReadDaemonInfoMalformedIsAnError` to a malformed kv row. Fix the e2e constructors at `e2e/headless_test.go:456, 468` and `e2e/remote_test.go:190`.
   - Verify: `go test ./internal/store/ ./internal/ui/ ./internal/release/ ./internal/harness/ ./internal/mastermind/ ./internal/delivery/ ./internal/hooks/ ./internal/doctor/ ./internal/relevo/ ./cmd/relevo/ -count=1`.
10. **Remove the serve imports, then `KVImportFile`.** Covers `LoadClients`, `SecretStore.Root` / `importLegacy` and the `Initialised` markers.
    - Delete `TestTLSImportsLegacyFiles`.
    - In `TestInitialised`, the `legacy clients.json file` row becomes "a clients.json file alone is not serve state", with want `false`.
    - Fix `cmd/relevo/{owner_reads,serve_contract}_test.go`, `e2e/remote_test.go:71, 110`, `remote/client/helpers_test.go:127, 191`, and `serve/{serve,tls,listen}_test.go`.
    - Then delete `db.KVImportFile` and its tests.
    - Verify: `go build ./... && go test ./internal/serve/ ./internal/db/ ./internal/remote/... ./internal/e2e/ ./cmd/relevo/ -count=1`, then `git grep -n 'KVImportFile\|LegacyPath\|legacyPath\|GatesDir\|ChannelsDir\|importLegacy'` finds nothing outside `docs/`.
11. **Docs, full check, plan, commit.**
    - Remove the README sentences from Phase C.
    - Run `make check`, `gofmt -l .` (it must print nothing), and `make e2e`.
    - Coverage: only the two deleted-package lines left the baseline (Phase A). If `check-coverage` reports a package more than a point down, compare that package's uncovered-statement count before and after. If the count did not grow, the drop is deletion drift: rewrite only that package's line with `sh scripts/check-coverage.sh --write` over a fresh profile, and report the before and after percentages and statement counts. If it grew, add a test. Never lower a baseline to get green.
    - Copy this plan to `docs/plans/2026-09-29-drop-relay-migration.md`. **Commit C.**

## 3. Closed list of what is deleted

Everything not on this list survives.

1. The `relevo migrate` verb: `cmd/relevo/migrate.go`, its test, its usage line and its dispatch case.
2. The `internal/migrate` package.
3. The unmigrated-state guard: `cmd/relevo/rename.go` and its test, plus `main.go`'s guard block.
4. The doctor `rename` row: `internal/doctor/rename.go`, its test and `probeFailedRenameRow`.
5. The `dist` Go package (`dist.go`, `dist_test.go`). The unit and plist files stay.
6. `internal/db/rewrite.go` (`Prefix`, `RewritePathPrefix`) and its test.
7. The dedupe rename path: the `renames` parameter, `RewriteJSON` matching, `TranscriptRowsRenamed`, `rehearsalRenames` and `TestDedupeMirrorPlansRenamedStreamLines`.
8. The `internal/legacy` package.
9. `serve.ReadPointer`, `serve.PointerFileName` and `TestReadPointerFile`. Also `store.ListFiles` and `loadBindingFile`.
10. The `relay-exit:` and `relay-rusage:` arms in `proc.ExitCode`, `proc.Rusage` and `proc.ParseRusageTrailer`, with the legacy rows in `proc_test.go:288-291` and `scope_test.go:81-82, 117`.
11. The legacy arms in `store.StreamDrained` and `trailerLinesOnly`, with `seal_test.go:104-107`'s legacy case.
12. `transcript`'s two legacy trailer constants.
13. `usage`'s `legacyExitTrailer`, `LegacyExitTrailerForTest` and `TestStreamClosedLegacyTrailer`'s legacy rows.
14. `relevo/gate.go` `tailLines`'s legacy arm and `gate_test.go:119-123`'s legacy case.
15. The `RELAY ED25519 PRIVATE KEY` acceptance and `TestParsePrivateAcceptsLegacyType`.
16. The ledger's `"relay"`→`"relevo"` source rewrite and `TestLoadKVReadsLegacySource`.
17. Store binding-file adoption: `importPresent` and helpers, `importAll`, `legacyPresent`, `anyLegacyPresent` and `ViewedPath`.
18. The flock-on-presence branch of `store.read` / `readAll`.
19. The binding-file import tests: `import_test.go` (`TestImportPresentAdoptsLegacyDirs`, `TestListSkipsANewerFormatBinding`, `TestImportPresentAdoptsViewedSidecar`) and `TestReadImportsLegacyFileUnderTheLock`.
20. The on-disk half of the newer-format tests (`format_test.go:253-275`), replaced by a record-row case.
21. `.archive/*.tar.gz` import: `store/archive.go`'s import functions, `ArchiveDir`, `archiveDirName`, `archiveStampLayout`, `serve`'s `importOwnerTarballs`, and the tests `TestListArchivedImportsAndRemovesATarball`, `TestListArchivedImportDoesNotTouchALiveBinding`, `TestListArchivedKeepsAnUnimportableTarball` and `TestRunImportsArchivedTarballsAtStartup`.
22. The legacy kv-file imports: `ledger.json`, `availability.json`, `history.json`, `latency.json`, `daemon.json`, `ui.json`, `release-check.json`, `agents-manifest.json`, `planners/*.json`, `channels/*.json` and `hooks.log`. Also their legacy path parameters and fields: `Deps.GatesDir`, `Runtime.GatesDir`, `LegacyGatesPath`, `legacyGatesPath`, `PrefsStore.LegacyPath`, `DBRegistry.Root`, `KVClaims.Root`, `store.ChannelsDir`, `KVLog.Root`, `NewKVLog`'s root, `OSInstallEnvKV`'s path, `daemonInfoPath` and `daemonInfoFileName`. And the tests named in steps 8-9.
23. `db.KVImportFile` and its five tests.
24. Serve's legacy imports: the `LoadClients` path, `SecretStore.Root`, `importLegacy`, `TestTLSImportsLegacyFiles` and the `clients.json` / `server.key` initialised markers.
25. The config lines: `check-name.sh`'s `internal/legacy` exclusion and scenario, `.golangci.yml`'s `^internal/legacy/` and `^dist/` exclusions, and six `check-comments.allow` lines.
26. The coverage-baseline lines for `internal/legacy` and `internal/migrate`.
27. README: "Upgrading from relay", the `relevo migrate` verb entry, and the tarball, `hooks.log` and "migrated on first read" sentences.

## 4. Halt conditions

Halt and report if any of these happens:

- A live writer is found still writing `bind.json`, `log.jsonl`, `.viewed`, a tarball or one of the kv files. That means removing its reader would lose data.
- A test outside the closed list fails and can only be fixed by deleting it.
- `internal/config/import.go`, `internal/db/migrate.go`, `internal/ingest/source.go` or `docs/specs/*` would need to change.
- `check-name` turns red on a line that is not a relay-era remnant this plan names.

## 5. Report must include

- The commit per phase with `git diff --stat`, and which phases were done if the round stopped at a boundary.
- The step 1 and 3-5 verification greps and their output, plus step 10's residue grep.
- Every mutation check (steps 2, 3 and 6), each with the failing test's name, or the explicit "nothing fails" where that is the point.
- Every test retargeted, one line each: old seed, new seed, and confirmation that every other assertion was kept.
- The `make check`, `gofmt -l .` and `make e2e` results, and any `unused` finding deleted outside the files named here (there should be none).
- Coverage: the two deleted lines, and for any other moved line, before and after with statement counts and why it is deletion drift.
- The out-of-repo items in §6, restated for the MasterMind.

## 6. Out of repo (MasterMind, before install or deploy)

1. **Client key header.** On every client machine (laptop, zen, contabo), check the stored `client.key` secret's first line. If it reads `-----BEGIN RELAY ED25519 PRIVATE KEY-----`, re-store it with the header renamed `RELEVO` using `relevo config secret set client.key`, which reads stdin and validates the key. Do this *with the old binary*, before installing, or every remote round fails.
2. **Leftover legacy files.** On every machine, in the client state root and in each serve owner root under `bindings/*/`, look for `bind.json`, `log.jsonl`, `.viewed`, `.archive/*.tar.gz`, `clients.json`, `server.key`/`.crt`, `hooks.log`, `ledger.json`, `availability.json`, `history.json`, `latency.json`, `daemon.json`, `ui.json`, `release-check.json`, `agents-manifest.json`, `planners/*.json` and `channels/*.json`. Any survivor is imported by running one read verb (`relevo status --all`, `relevo serve status`) with the *old* binary before upgrading.
3. **contabo unit.** Remove the two `ExecStartPre=... relevo migrate` lines from contabo's unit (servers repo) before deploying this build, or the unit fails to start on an unknown verb.
