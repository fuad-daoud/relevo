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
- The engine still materialises one value (up to SQLite's 1 GB per-value limit)
  before relevo can see it; relevo cannot bound that transient without an engine
  limit. It bounds what it copies instead: an ad-hoc read (`relevo db query`) is
  refused when a single value exceeds 64 MiB.
- No foreign key that cascades or takes NO ACTION to one parent, and no
  partial index on a foreign-key column: Turso mishandles both, so a
  migration must not rely on either.
- `RETURNING`, `AUTOINCREMENT`, triggers and plain views are supported by
  Turso and are not banned on its account. The phase-1 schema still avoided
  triggers, FTS, virtual tables and generated columns as its own choice
  (persistence spec, decision 2); migration 022 adds the first triggers, and
  they carry the outbox only. FTS, virtual tables and generated columns are
  still avoided.

## Triggers

A trigger fires for every connection and every writer, so a migration that
adds one changes what every later write records whether or not that writer
knows. `022_sync_outbox.sql` is the only file that creates any:

- One trigger per operation per shared table, named
  `sync_outbox_<table>_<ins|upd|del>`. A reader enumerates the set from
  `sqlite_schema`, and a table missing one is a gap rather than a slower
  table.
- A trigger writes one row and never raises. A statement that would otherwise
  fail must keep failing on its own terms, not on a trigger's: the owning
  installation is resolved with a scalar subquery, which yields NULL when the
  parent row is already gone -- a child removed by a cascade -- and NULL is a
  value the reader skips, not an error.
- The engine rewrites a call's open paren with a space before it
  (`json_array (x)`), so a test that reads a trigger body out of
  `sqlite_schema` compares against the rewritten text.

`AUTOINCREMENT` on `sync_outbox.seq` is what makes a delete followed by an
insert of the same key still two entries, which is the ordering the drain
depends on.

## Conversion

A file a pre-Turso build wrote is converted once on its first open after the
swap: it is backed up to `<path>.pre-turso`, its `-wal` is drained, its invalid
UTF-8 text is repaired, and then the conversion records that it happened in the
file's own header, in `application_id`, which is relevo's marker. A converted
file keeps the marker, so it is never backed up or repaired twice, and a
database this build creates carries the marker from creation. Running a
pre-Turso relevo against a converted file writes no marker and repairs nothing,
so upgrading again afterwards converts nothing either. To force the conversion
again, clear the marker back to 0 -- with `-tags modernc`, `PRAGMA
application_id = 0`, or by zeroing the four header bytes at offset 68 -- and
open the file once more.

## Known gaps (checked, avoided)

- `CREATE VIEW IF NOT EXISTS` errors on the second run.
- A second active write statement on one connection returns BUSY.
- Recursive aggregate queries are unsupported; Turso's `COMPAT.md` still
  lists them as missing.
- Page codecs cannot combine with `multiprocess_wal`, MVCC or partial sync.
