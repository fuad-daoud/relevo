-- Schema v10 (#632): a repository records whether relevo may register and
-- brief its sessions as a MasterMind. NULL (or no row) is "unset": the ask
-- state. 'yes' and 'no' are the remembered answers; consent_at is when the
-- last answer was written.
--
-- Add-only and Turso-safe like 001-009: no triggers, views, FTS or virtual
-- tables, no RETURNING. ALTER TABLE ADD COLUMN needs no version guard of its
-- own; applyOneMigration runs the file once, in one transaction.

ALTER TABLE repo ADD COLUMN mastermind_consent TEXT;
ALTER TABLE repo ADD COLUMN consent_at TEXT;
