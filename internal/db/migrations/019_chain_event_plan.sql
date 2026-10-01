-- Schema v19: the plan a chain_event row was written on. The trace renders each
-- row with the plan it was written under, so a row survives a chain's advance
-- onto a later plan; a row written before this migration reads 0 and the trace
-- falls back to the chain's current plan.
--
-- Add-only and Turso-safe like 001-018: ALTER TABLE ADD COLUMN only, with a
-- NOT NULL default so every row written before this migration reads 0. There is
-- no backfill: a trace row that predates this column has no recorded plan, and
-- its line falls back to the chain's current plan.
--
-- Compatibility rules: internal/db/migrations/README.md. ALTER TABLE ADD COLUMN
-- has no IF NOT EXISTS form, so this file relies on applyOneMigration's version
-- guard: it runs once, in one transaction, and schema_version records it.

ALTER TABLE chain_event ADD COLUMN plan INTEGER NOT NULL DEFAULT 0;
