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

## The rules the live driver runs under

The schema runs on `turso.tech/database/tursogo` v0.8.1 behind `internal/db`,
the default build; `-tags modernc` is the way back. Checked against v0.8.1
(2026-09-29); sources: Turso's `COMPAT.md`, `docs/manual.md`, and the embedded
`tursogo` driver at that tag.

What binds a migration:

- No dependence on in-place `VACUUM` (it needs Turso's experimental
  `vacuum` flag); `VACUUM INTO` is fine. `VACUUM INTO` takes a string
  literal, so the path cannot hold a quote, and the target must not exist
  yet.
- No pragmas outside the live driver's list. In particular,
  `foreign_key_check`, `defer_foreign_keys` and `wal_autocheckpoint` are
  not supported, and `journal_size_limit` applies under `-tags modernc`
  only; `journal_mode` (`wal` and `mvcc`), `busy_timeout` and
  `foreign_keys` are.
- Driver errors are mapped in one place: `mapBusy` and `mapMasterMindKey`
  in `internal/db`.
- The daemon is the only process that opens the file, and
  `multiprocess_wal` is never set (#466).
- A database path must not contain `?`: Turso ends the path at the first
  one, so a path that carries one would silently open another file.
- Text that is not valid UTF-8 is repaired to U+FFFD on the way in, in both
  engines: an invalid TEXT value makes a file Turso refuses to read.
- No foreign key that cascades or takes NO ACTION to one parent, and no
  partial index on a foreign-key column: Turso mishandles both, so a
  migration must not rely on either.
- `RETURNING`, `AUTOINCREMENT`, triggers and plain views are supported by
  Turso and are not banned on its account. The phase-1 schema still avoids
  triggers, FTS, virtual tables and generated columns as its own choice
  (persistence spec, decision 2).

## Known gaps (checked, avoided)

- `CREATE VIEW IF NOT EXISTS` errors on the second run.
- A second active write statement on one connection returns BUSY.
- Recursive aggregate queries are unsupported; Turso's `COMPAT.md` still
  lists them as missing.
- Page codecs cannot combine with `multiprocess_wal`, MVCC or partial sync.
