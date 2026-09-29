-- Schema v7 (docs/specs/2026-09-26-a4-state-rename-design.md §3.4): the round
-- table names its candidate and its actor. Same dialect as 001, 003 and 005:
-- CREATE ... IF NOT EXISTS, no AUTOINCREMENT, no WITHOUT ROWID, no
-- triggers/views/FTS/virtual tables, no RETURNING. 005 shows that DROP INDEX
-- IF EXISTS is part of that dialect.
--
-- This is the first ALTER TABLE. RENAME COLUMN needs SQLite >= 3.25, and
-- modernc.org/sqlite embeds a much newer SQLite, so it is available. RENAME
-- COLUMN and ADD COLUMN have no IF NOT EXISTS form, so this file relies on
-- applyOneMigration's version guard: it runs once, in one transaction, and
-- schema_version records it.

ALTER TABLE round RENAME COLUMN builder_candidate TO candidate;
ALTER TABLE round RENAME COLUMN builder_harness   TO harness;
ALTER TABLE round RENAME COLUMN builder_provider  TO provider;
ALTER TABLE round RENAME COLUMN builder_model     TO model;
ALTER TABLE round RENAME COLUMN builder_mode      TO mode;
ALTER TABLE round ADD COLUMN actor TEXT NOT NULL DEFAULT 'builder';
DROP INDEX IF EXISTS round_builder_idx;
CREATE INDEX IF NOT EXISTS round_candidate_idx ON round(harness, provider, model);
