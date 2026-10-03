# S4A builder-round plan — enable gates and seed writer (spec §6 S4, issue #473)

Base: spec tip `0ba5e47d` (spec + slice plans). Plan only: no file changed
by the planning rounds; the builder round changes code per §3.

Split from `docs/plans/2026-10-03-turso-sync-s4.md`: this half owns the
decision logic (gates + seed copy). S4B owns the surface (verbs, enable
path, turn-off). S4A lands first; S4B consumes its names.

## 0. What S4A builds (spec §5, §6 S4, §7)

- **Origin-gate counter** (`internal/db`, new file beside `origin.go`):
  per-table `origin=''` counts over the five §5 tables (`binding_record`,
  `binding`, `repo`, `mastermind`, `chains`). Direct counts
  (`SELECT COUNT(*) WHERE origin=''`); never widens `BackfillOriginOnce`
  (history must not mutate inside enable, spec `:175-176`).
- **Preflight package**: one refusal per check, each naming the fix —
  empty origin (five counts, points at `BackfillOriginOnce`/upgrade),
  shared-secret rows (points at S1 split), compress pass incomplete (no
  `zstd-compress.v1` kv row), upload prereqs (non-WAL / non-4096 /
  non-drained WAL).
- **Seed-copy writer**: asserts `journal_mode=wal AND page_size=4096 AND
  wal_is_empty` before the upload path, else writes the asserted seed copy
  with `VACUUM INTO` + pragmas. The tree sets WAL per connection
  (`internal/db/engine_turso.go:90-97` `openPragmas`), checkpoints via
  `walCheckpoint` (`internal/db/compress.go:301-306`), copies via
  `internal/db/vacuum.go:27-72`, and reads `page_size` only in stats
  (`internal/db/stats.go:29-33`) — no `PRAGMA page_size=4096` exists
  anywhere, so the writer owns the literal form per
  `internal/db/engine_turso.go:198-206` `vacuumIntoStmt` (quote-in-path
  refusal included).

## 1. Verified seams (all checked in the tree)

- `BackfillOriginOnce` stamps only `binding_record` + `binding`
  (`internal/db/origin.go:30-65`); `repo`/`mastermind` scoping via
  `UpsertRepo`/`UpsertMasterMind` (`internal/db/origin_test.go:336-382`);
  `chains.origin` exists with no backfill
  (`internal/db/migrations/016_chains.sql:15-18`).
- Migration 015 adds nullable `link_origin`/`link_id`, no backfill
  (`internal/db/migrations/015_binding_link.sql:1-18`).
- Origin columns/indexes in 014
  (`internal/db/migrations/014_installation_origin.sql:23-55`); backfill
  marker `origin-backfill.v1` (`internal/db/origin.go:12`); compress marker
  `zstd-compress.v1` (`internal/db/compress.go:14-18`).
- kv surface `KVGet/KVPut/KVDelete` (`internal/db/kv.go:29-74`); secret
  surface `internal/db/config.go:98-157`; owner direct open +
  installation mint (`cmd/relevo/daemon.go:279-354` pass order,
  `cmd/relevo/wire.go:254-276`).
- Round rules: `CLAUDE.md:39-44` (`make check` gate), `:92-102`
  (`cmd/relevo` test isolation — S4A has no `cmd/relevo` changes at all).

## 2. Ordered steps (deliverable + how it is known to work)

1. **Origin-gate counter** (new file beside `origin.go`): per-table counts
   over the five §5 tables — worked when `TestEnableRefusesEmptyOrigin`
   names all five counts on a fixture with one empty row per table.
2. **Preflight checks** (gate + shared-secret-empty + compress-marker +
   upload-prereqs, each a named refusal): — worked when each named refusal
   test fails before and passes after, with the fix named in the message.
3. **Seed-copy writer** (temp-dir `VACUUM INTO` + hard-link pattern per
   `vacuum.go:27-72`, `walCheckpoint` per `compress.go:301-306`): asserts
   WAL/4096/drained-WAL else writes the asserted copy — worked when
   `TestLateEnableSeedsOnce` (400 MB-class existing DB, no rework/second
   conversion) and the upload-prereq refusal pass, including the
   quote-in-path refusal.
4. **Verification + report**: `go test ./internal/db/ -run
   'TestEnable|TestLateEnable|TestSeed' -count=1` green, then `make check`
   green; report quotes commands/outputs, commits + base SHA, mutations,
   and S4B as the named consumer of the counter/preflight/writer names.

## 3. Tests (all fake-backed or fixture-backed, no network; one mutation per test)

`TestEnableRefusesEmptyOrigin`, `TestEnableRefusesSharedSecrets`,
`TestEnableRefusesCompressIncomplete`, `TestEnableRefusesUploadPrereqs`,
`TestLateEnableSeedsOnce`. Mutations: drop one table from the counter →
origin test fails; check shared secret after (not before) the move set →
secrets test fails; skip the compress-marker read → its test fails; accept
non-WAL without writing the asserted copy → prereq test fails; skip the
second-conversion guard → late-enable test fails.

## 4. What is deleted / untouched

Deleted: nothing — pure addition. Untouched: `BackfillOriginOnce`
semantics, migration 015 no-backfill rule, S1 routing, S2 section/token,
S3 timing, S5 view, any `cmd/relevo` verb, any config-section sync.
