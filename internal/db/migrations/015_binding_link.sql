-- Schema v15: the link between the two copies of a remote binding. A client's
-- row records the server installation and the server's binding_record id; the
-- server's row records the client installation and the client's record id.
-- Both columns are nullable: every row written before this migration, every
-- binding created by an older client, and every binding created against an
-- older server carries no link.
--
-- The link is not origin: it names another installation's row, so it is read
-- together with the row's origin and never rewritten. There is no backfill --
-- an existing remote binding stays unlinked until it is bound again.
--
-- Compatibility rules: internal/db/migrations/README.md. Add-only and
-- Turso-safe like 001-014: ALTER TABLE ADD COLUMN only. ALTER TABLE ADD COLUMN
-- has no IF NOT EXISTS form, so this file relies on applyOneMigration's version
-- guard: it runs once, in one transaction, and schema_version records it.

ALTER TABLE binding_record ADD COLUMN link_origin TEXT;
ALTER TABLE binding_record ADD COLUMN link_id TEXT;
