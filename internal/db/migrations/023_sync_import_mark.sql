-- Schema v23: how far this machine has applied each other installation's
-- entries. One row per origin, holding the last sequence number applied from
-- that origin's log, so a reader picks up where the last round left off rather
-- than re-reading the whole log.
--
-- The marks live in the same file as the rows they describe, in the same
-- transaction as the batch that moved them, so a restore of that file rewinds
-- the marks to exactly the state the rows are in: a machine brought back from a
-- backup re-applies the entries that came after the backup rather than skipping
-- them.
--
-- This table is machine-local and carries no trigger: a mark is about what this
-- machine has applied, not about shared history, so it is neither in the shared
-- table list nor in the outbox. A mark must never leave the machine, because two
-- machines sharing one mark would each believe the other had applied the entries
-- between them.
--
-- origin is the installation id the mark belongs to, so it is the primary key
-- and one origin has exactly one mark. seq is NOT NULL because a mark with no
-- position would read as "nothing applied" either way, and the reader should
-- have to say which it means.
--
-- Compatibility rules: internal/db/migrations/README.md. CREATE TABLE is IF NOT
-- EXISTS here, and this file still runs once, in one transaction, under
-- applyOneMigration's version guard.

CREATE TABLE IF NOT EXISTS sync_import_mark (
    origin TEXT PRIMARY KEY,
    seq INTEGER NOT NULL
);