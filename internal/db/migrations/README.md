# Migrations

One file per schema version, applied in name order, once each, in one
transaction (`applyOneMigration`, `internal/db/migrate.go`). The file that
created a table is its history: existing files are never edited, only
added to.

New migrations cite this file for the compatibility rules instead of
copying them into their header. Migrations 001-013 carry an older, fuller
list in their headers; that text is applied history and stays as it is.

## House rules

- One version per file, `NNN_name.sql`; a fresh database applies the whole
  series in order, so every statement must run on the schema its
  predecessors left.
- Add-only where possible (`ALTER TABLE ... ADD COLUMN`); a table rebuild
  is the last resort and must copy its data in the same transaction.
- Nothing inside a file guards its version: the runner applies each file
  once and records it.

## The movable list

The schema stays movable to Turso. Checked against Turso v0.8.0-pre.12
(2026-09-25) and re-checked at v0.8.1 (2026-09-29); sources: Turso's
`COMPAT.md`, `docs/manual.md`, and the embedded `tursogo` driver at those
tags.

What binds a migration:

- No dependence on in-place `VACUUM` (it needs Turso's experimental
  `vacuum` flag); `VACUUM INTO` is fine.
- No pragmas outside Turso's compatibility list. In particular,
  `foreign_key_check`, `defer_foreign_keys` and `wal_autocheckpoint` are
  not supported; `journal_mode` (`wal` and `mvcc`), `busy_timeout` and
  `foreign_keys` are.
- Driver errors are mapped in one place: `mapBusy` and `mapPlannerKey` in
  `internal/db`.
- One process opens the file until upstream's `multiprocess_wal` leaves
  experimental status (#466); nothing here may assume several openers.
- `RETURNING`, `AUTOINCREMENT`, triggers and plain views are supported by
  Turso and are not banned on its account. The phase-1 schema still avoids
  triggers, FTS, virtual tables and generated columns as its own choice
  (persistence spec, decision 2).

## Known gaps (checked, avoided)

- `CREATE VIEW IF NOT EXISTS` errors on the second run.
- A second active write statement on one connection returns BUSY.
- Window functions lack `lag`, `lead`, `ntile` and custom frames;
  `WITH RECURSIVE` is unsupported.
- Page codecs cannot combine with `multiprocess_wal`, MVCC or partial sync.
