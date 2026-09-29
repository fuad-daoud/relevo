-- Schema v13 (#672): the three bulk-history columns gain a per-row codec
-- beside them. codec 0 means the column holds its plain value; codec 1 means
-- it holds a zstd frame. Each compressed column carries its own flag because
-- the per-row size guard decides each one independently.
--
-- Add-only and Turso-safe like 001-012: no triggers, views, FTS or virtual
-- tables, no RETURNING. ALTER TABLE ADD COLUMN needs no version guard of its
-- own; applyOneMigration runs the file once, in one transaction.

ALTER TABLE round_file ADD COLUMN body_codec INTEGER NOT NULL DEFAULT 0;
ALTER TABLE transcript ADD COLUMN record_json_codec INTEGER NOT NULL DEFAULT 0;
ALTER TABLE transcript ADD COLUMN rendered_codec INTEGER NOT NULL DEFAULT 0;
