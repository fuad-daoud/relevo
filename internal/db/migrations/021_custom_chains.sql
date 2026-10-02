-- Schema v20: a chain carries a workflow definition and its engine state, its
-- members in their own table, and its check runs. The chains row's fixed
-- legacy columns stay readable until a later migration drops them, and a row
-- written before this one reads the empty default in every new column.
--
-- Add-only and Turso-safe like 001-019: ALTER TABLE ADD COLUMN and
-- CREATE TABLE/INDEX IF NOT EXISTS only. ALTER TABLE ADD COLUMN has no
-- IF NOT EXISTS form, so this file relies on applyOneMigration's version
-- guard: it runs once, in one transaction, and schema_version records it.
--
-- Compatibility rules: internal/db/migrations/README.md. Neither table below
-- takes a foreign key: a cascading or NO ACTION reference is mishandled, so
-- ChainDelete removes the member and check rows in code.
--
-- The workflow and state columns are TEXT so a stored definition and a state
-- survive a read by a binary that does not yet know their shape.

ALTER TABLE chains ADD COLUMN workflow TEXT NOT NULL DEFAULT '';
ALTER TABLE chains ADD COLUMN state TEXT NOT NULL DEFAULT '';
ALTER TABLE chains ADD COLUMN parent TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS chain_member (
    chain_id TEXT NOT NULL,
    binding TEXT NOT NULL,
    actor TEXT NOT NULL,
    seq INTEGER NOT NULL,
    PRIMARY KEY (chain_id, binding)
);
CREATE INDEX IF NOT EXISTS chain_member_binding_idx ON chain_member(binding);

CREATE TABLE IF NOT EXISTS chain_check (
    chain_id TEXT NOT NULL,
    run INTEGER NOT NULL,
    step TEXT NOT NULL,
    visit INTEGER NOT NULL,
    command TEXT NOT NULL,
    pid INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL DEFAULT 0,
    attempt INTEGER NOT NULL DEFAULT 0,
    result TEXT NOT NULL DEFAULT '',
    exit_code INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    log TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (chain_id, run)
);
