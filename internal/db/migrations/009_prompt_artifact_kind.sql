-- Schema v9: the round's input artifact kind follows the file name, from the
-- historical "plan" to "prompt". The database is the one place stored bytes may
-- be rewritten; every other stored spelling keeps its old value. A round's
-- prompt is read back through the round-file name, so the artifact kind is only
-- a label: this is a pure rewrite, with no new table and no new index.
--
-- Same Turso-safe dialect as 001-008: no triggers, views, FTS or virtual
-- tables, no RETURNING. applyOneMigration's version guard runs this file once,
-- in one transaction.

UPDATE artifact SET kind = 'prompt' WHERE kind = 'plan';
