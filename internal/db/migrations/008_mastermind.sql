-- Schema v8 (the MasterMind rename, D3): the session's records move from the
-- planner spelling to the mastermind one -- the table, the binding's column,
-- both indexes and the registry's kv prefix. This is the one place the stored
-- bytes change; every other stored spelling keeps its old value (D2).
--
-- Same Turso-safe dialect as 001-007: ALTER TABLE RENAME, DROP INDEX IF
-- EXISTS, CREATE ... IF NOT EXISTS, no AUTOINCREMENT, no WITHOUT ROWID, no
-- triggers/views/FTS/virtual tables, no RETURNING. Like 007, this relies on
-- applyOneMigration's version guard: RENAME and RENAME COLUMN have no IF NOT
-- EXISTS form, so the whole file runs once, in one transaction, and
-- schema_version records it.

ALTER TABLE planner RENAME TO mastermind;
ALTER TABLE binding RENAME COLUMN planner_id TO mastermind_id;

DROP INDEX IF EXISTS planner_harness_session_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS mastermind_harness_session_uidx ON mastermind(harness_kind, session_id);

DROP INDEX IF EXISTS binding_planner_id_idx;
CREATE INDEX IF NOT EXISTS binding_mastermind_id_idx ON binding(mastermind_id);

-- The registry's kv prefix moves with the table: `planner/<id>` (8 characters
-- before the id) becomes `mastermind/<id>`.
UPDATE kv SET key = 'mastermind/' || substr(key, 9) WHERE key LIKE 'planner/%';
