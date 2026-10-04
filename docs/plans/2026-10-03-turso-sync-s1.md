# S1 builder-round plan — second-file split (spec §6 S1, issue #473)

## 0. Behaviour and cases

S1 ends with two files splitting correctly and nothing syncing yet (spec `docs/specs/2026-10-03-turso-sync-design.md:184-188`).

- **Routing table.** Every table / kv-namespace has exactly one home. Shared file (`relevo.db`): all `SCOPES.md` shared-history tables and nothing else (`internal/db/migrations/SCOPES.md:15-44`; spec `§1:47-50`). Local file (`relevo-local.db`): all `SCOPES.md` machine-local kv namespaces (`SCOPES.md:52-75`), the whole `secret` table (`SCOPES.md:76-77`; spec `§5:156-160`), `session_consent` / `ingest_cursor` / `config_import` (`SCOPES.md:78-83`), all `config_doc` / `config_revision` / `config_meta` rows per spec §2 decision (`spec:63-68`; SCOPES.md leaning `SCOPES.md:92-103`), plus per-machine `sync.*` kv state residents. Acceptance mirrors spec §1 (`spec:59-61`): a seeded database splits with zero shared-table rows in the local file and zero secret/config/kv-local rows in the shared file.
- **Owner opens both.** The daemon's one direct handle becomes two same-schema files opened together: same migrations on both (spec D0, `spec:52-56`), routing by table/namespace in the owner, no schema fork.
- **Backup-then-migrate pass.** One migration-style run moves the local rows once, backup first, like the zstd and origin passes (`internal/db/compress.go:75-114`; `internal/db/origin.go:30-65`). Backup failure converts nothing and records nothing (`compress.go:95-99` precedent; `compress_test.go:401-421` pattern).
- **Idempotent re-run.** A finished pass records itself under a kv marker and re-runs as a no-op (`compress.go:18,76-80`; `origin.go:12,31-35`).
- **Rollback on failure.** A mid-pass failure leaves the source database usable with no marker row, so the next start retries from the top (`origin.go:9-12`; `compress.go:70-74` comment pattern).
- **Seed-vs-code note.** The seed's parenthetical "(SCOPES.md machine-local list, plus `client.key`, `typesafe`, `serve.tls.*`, `config_import`, all `config_doc` rows)" is redundant, not contradictory: `SCOPES.md:76-77` already names those three secret residents and `SCOPES.md:82` already names `config_import`; "all `config_doc` rows" is spec §2's decision (`spec:63-68`) resolving SCOPES.md's stated leaning (`SCOPES.md:94-98`). The plan encodes the full table: whole `secret` table (all names, not just the three), all `config_doc`/`config_revision`/`config_meta` rows, `session_consent`, `ingest_cursor`, `config_import`, and the full kv-namespace allowlist. The seed's "enable refuses while any secret row remains shared" is spec §5 (`spec:156-160`) enforced by the **S4** preflight (`spec:200-206`), not by S1: S1 performs the move; S1 adds no enable path and no refusal.

## 1. Seams (file:line for every step)

- Routing source: `internal/db/migrations/SCOPES.md:15-44` (shared), `:52-86` (local kv + secret + consent/cursor/import + installation file), `:92-103` (per-user config).
- Owner-open path: `cmd/relevo/daemon.go:188-201` (daemon's one direct handle via `openDBDirect`), `cmd/relevo/wire.go:254-276` (`openDB` / `openDBDirect`), `internal/db/db.go:99-107` (`Open`/`OpenWith`), `:119-135` (`open`, owner-hop branch), `:137-213` (`openDirect`), `:218-246` (`finishDirectOpen`), `internal/db/handles.go:58-96` (per-path lock/count — a second file needs its own entry), `internal/db/dial.go:23-35,56-87` (dial stays single-file), `internal/db/dial.go:93-96` (`NewOwner` serves one pool).
- Pass precedent: `internal/db/compress.go:14-22` (marker key + batch bound), `:75-114` (`CompressHistoryOnce`: kv short-circuit, candidate check, `BackupTo`, convert, finish, record stats), `:282-317` (checkpoint/vacuum finish, stats write), `internal/db/vacuum.go:16-25` (`BackupTo`), `:27-72` (`vacuumInto`: refuses existing target, owner-only temp + link), `internal/db/origin.go:9-21` (marker + stats), `:30-65` (`BackfillOriginOnce`: kv short-circuit, single-`Tx` update, no marker on failure), `internal/db/migrate.go:21-39,112-178` (migration runner rules), `internal/db/migrations/README.md:1-20` (house rules), `internal/ingest/dedupe.go:480` (second backup-naming precedent `relevo.db.pre-dedupe-`).
- Row surfaces the pass moves: `internal/db/kv.go:29-31,50-74` (`KVGet`/`KVPut`/`KVDelete`), `:78-103` (`KVKeys`), `internal/db/config.go:25-56` (`ConfigGet`/`ConfigPut`), `:100-157` (`SecretGet`/`SecretPut`/`SecretDelete`/`SecretNames`), `:177-183` (`ConfigImportRecord`), table DDL `internal/db/migrations/002_config.sql:9-34` (`config_doc`, `config_meta`, `secret`, `kv`, `config_import`), `internal/db/migrations/011_session_consent.sql:13`, `internal/db/migrations/001_initial.sql:134` (`ingest_cursor`), `internal/db/migrations/006_config_revision.sql:8`.
- Test harness: `internal/db/main_test.go:82-97` (`TestMain` owner-hop switch — new tests run under both modes), `internal/db/helpers_test.go:17-41` (`openTestDB`, `directOpen`), `internal/db/compress_test.go:210-236,288-305,401-421` (end-to-end / one-time / backup-failure patterns to mirror), `internal/db/origin_test.go:247-297` (one-time-pass pattern).
- Checks: `Makefile:30-32` (`check`), `:39-63` (`check-static`: gofmt, vet, lint, comments, filesize, tidy), `:71-73` (`check-test`: `go test -race -count=1 -cover ./...` + coverage script); repo rules `CLAUDE.md:39-47` (verify + mutation), `:60-84` (style/size/coverage), `:93-102` (`cmd/relevo` test isolation).

## 2. Ordered steps (one line each: deliverable + how it is known to work)

1. Routing table as a pure function in `internal/db` (table/namespace → file) with a unit test over the full SCOPES.md allowlist — deliverable: table + `TestSplitRoutesEveryScope`; worked: `go test ./internal/db/ -run 'TestSplitRoutes' -count=1` green and deleting any one allowlist entry fails it.
2. Owner-open change (`cmd/relevo/daemon.go:198`, `cmd/relevo/wire.go:257-276`, `internal/db/db.go:137-246`): open `relevo-local.db` beside `relevo.db`, same migrations both files — deliverable: two same-version files from one open; worked: version assertion in the new test plus `go test ./internal/db/ -run 'TestSplitOpen' -count=1` green.
3. Backup-then-migrate pass (`SplitOnce`, after `compress.go:75-114`): backup shared file first (`vacuum.go:20-25`, `relevo.db.pre-split-<stamp>` beside the database per `compress.go:95` / `dedupe.go:480` patterns), then move secret/config/kv-local rows into the local file — deliverable: pass + `TestSplitMovesLocalRowsOnly`; worked: seeded DB splits with zero shared rows local / zero local rows shared (spec §1 acceptance, `spec:59-61`).
4. Idempotent re-run via a local-file kv marker (after `origin.go:12`, `compress.go:76-80`): finished pass short-circuits to `ran=false` — deliverable: marker + `TestSplitIsOneTime`; worked: second call is a no-op and removing one routed row then clearing the marker re-moves only that row.
5. Rollback on failure inside the pass's transactions (after `origin.go:38-65` single-`Tx` discipline): any move error returns with no marker written — deliverable: atomicity + `TestSplitFailureWritesNoMarker`; worked: injected mid-pass failure leaves source rows intact and marker absent (mirror `compress_test.go:401-421`).
6. Full verification: focused `go test -race -count=1 ./internal/db/` green, then `make check` exits 0 with no new lint/comment/filesize exclusions and no coverage-baseline lowering (`CLAUDE.md:74-84`; regenerate with `sh scripts/check-coverage.sh --write` only if code moved packages, stated in report).
7. Report with commits + SHAs and base SHA, every test command quoted with its output, one mutation line per behaviour test naming the test each mutation failed, and the deliberately-left-out list (§4) — deliverable: report text; worked: each claim traces to a SHA or a pasted output.

## 3. Tests (each named; pure functions with fakes only — no harness spawn, no network, no `cmd/relevo` server-reaching test per `CLAUDE.md:93-102`)

- `TestSplitMovesLocalRowsOnly` (routing end-to-end: shared-only-in-shared, local-only-in-local, incl. `client.key` absent from the shared file post-split).
- `TestSplitIsOneTime` (idempotent re-run: second run `ran=false`, byte-identical files).
- `TestSplitTakesBackupFirst` (backup-then-migrate: backup file exists beside the DB, holds pre-pass history, failure to back up converts nothing).
- `TestSplitFailureWritesNoMarker` (rollback on failure: injected mid-pass error → source intact, marker absent, next run retries).
- `TestSplitRoutesEveryScope` (pure routing table over every SCOPES.md row: each shared table → shared; each local namespace/table → local).

## 4. Mutations (one per behaviour test: what is removed/changed → which named test must fail)

1. Remove `client.key` from the moved-secret set → `TestSplitMovesLocalRowsOnly` must fail.
2. Skip writing the kv marker on success → `TestSplitIsOneTime` must fail (second run reports `ran=true`).
3. Move the `BackupTo` call after the first move transaction → `TestSplitTakesBackupFirst` must fail.
4. Commit the marker before the move transactions → `TestSplitFailureWritesNoMarker` must fail.
5. Route one kv namespace (e.g. `daemon`) to the shared file → `TestSplitRoutesEveryScope` must fail.

## 5. What is deleted (closed list)

1. No behaviour is deleted. Local rows are relocated from the shared file to the local file; no flag, verb, index, or query semantic is removed.

## 6. Deliberately untouched sites (named)

- S2: local `sync` config section, `turso.token` handling, statusline tokens, `SyncClient` interface + fake (`spec:189-194`).
- S3: push/pull wiring, seal/idle-tick triggers, `sync:behind`/`sync:err` markers (`spec:195-199`).
- S4: enable/disable flows, origin-gate and shared-secret refusals, seed/upload/`BootstrapIfEmpty` matrix (`spec:200-206`; the seed's "enable refuses" sentence lives here).
- S5: cockpit `:sync` view (`spec:207-210`).
- Spec §7 cross-machine acceptances, §8 out-of-scope items (zstd dictionary, encryption, team sharing, MVCC toggle, partial sync, token rotation, config sync, cloud delete), §9 directory-gate docs; `installation.json` minting (`SCOPES.md:85`, `wire.go:271`); serve per-owner roots; any `cmd/relevo` verb.

## 7. What the report must include

- Commits with SHAs + base SHA (new commits only; never amend/rebase a commit on a remote binding's branch).
- Test commands quoted with outputs: the focused `go test ./internal/db/ -run …` suites and the final `make check` (exit 0).
- Mutations with the named test each failed (§4).
- What was deliberately left out (§6: S2–S5, seed/upload, UI/docs).
