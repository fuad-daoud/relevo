-- Schema v18: the commit a chain's current plan started at. A plan's review
-- shows its whole span -- the plan-start commit through the closing round's
-- closed tree -- so the chain records where the plan began. Plan 1's value is
-- the commit the builder's worktree was cut from; a later plan's is the
-- baseline head the plan's own send recorded, and a resumed chain keeps it.
--
-- Add-only and Turso-safe like 001-017: ALTER TABLE ADD COLUMN only, with a
-- NOT NULL default so every row written before this migration reads ''. There
-- is no backfill: a chain created before this round simply has no recorded
-- start, and its seeds omit the cumulative line.
--
-- Compatibility rules: internal/db/migrations/README.md. ALTER TABLE ADD COLUMN
-- has no IF NOT EXISTS form, so this file relies on applyOneMigration's version
-- guard: it runs once, in one transaction, and schema_version records it.

ALTER TABLE chains ADD COLUMN plan_start_commit TEXT NOT NULL DEFAULT '';
