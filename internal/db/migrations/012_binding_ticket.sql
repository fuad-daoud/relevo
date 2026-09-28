-- Schema v12 (#637): a binding may name the issue it serves, in stored form
-- #N or owner/repo#N. NULL is "no ticket", the same shape feature uses. The
-- ticket travels beside feature through ingest, history and stats.
--
-- Add-only and Turso-safe like 001-011: no triggers, views, FTS or virtual
-- tables, no RETURNING. ALTER TABLE ADD COLUMN needs no version guard of its
-- own; applyOneMigration runs the file once, in one transaction.

ALTER TABLE binding ADD COLUMN ticket TEXT;
