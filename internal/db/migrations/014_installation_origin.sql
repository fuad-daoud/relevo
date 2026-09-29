-- Schema v14: every shared row says which installation wrote it, before two
-- machines can share one database. An installation's id is minted into
-- <state root>/installation.json and stamped on the rows it writes; the
-- installation table below is the directory that lets another installation
-- show a label for it after rows cross a machine boundary.
--
-- Child rows inherit their origin through their parent, and get no column of
-- their own: binding_event and round_file through record_id, and the mirror's
-- round, event, artifact and transcript through binding_id or round_id. The
-- parent is a ULID, so the inheritance is unambiguous even when two origins
-- hold rows with the same name.
--
-- Compatibility rules: internal/db/migrations/README.md. Add-only and
-- Turso-safe like 001-013: ALTER TABLE ADD COLUMN, DROP INDEX IF EXISTS and
-- CREATE ... IF NOT EXISTS only -- no triggers, views, FTS or virtual tables,
-- no RETURNING. ALTER TABLE ADD COLUMN and DROP INDEX have no IF NOT EXISTS
-- form, so this file relies on applyOneMigration's version guard: it runs
-- once, in one transaction, and schema_version records it.
--
-- Two live indexes are re-keyed by origin rather than dropped: a name is
-- unique within (origin, owner), and a mirror natural key within its origin.

CREATE TABLE IF NOT EXISTS installation (
    id TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    last_seen TEXT NOT NULL
);

ALTER TABLE binding_record ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE binding ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE repo ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE mastermind ADD COLUMN origin TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS binding_record_owner_name_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS binding_record_origin_owner_name_uidx
    ON binding_record(origin, owner, name) WHERE archived_at IS NULL;

DROP INDEX IF EXISTS binding_name_created_at_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS binding_name_created_at_uidx
    ON binding(origin, name, created_at);

DROP INDEX IF EXISTS repo_origin_url_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS repo_origin_origin_url_uidx
    ON repo(origin, origin_url) WHERE origin_url IS NOT NULL;

DROP INDEX IF EXISTS repo_common_dir_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS repo_origin_common_dir_uidx
    ON repo(origin, common_dir) WHERE common_dir IS NOT NULL;

DROP INDEX IF EXISTS mastermind_harness_session_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS mastermind_origin_harness_session_uidx
    ON mastermind(origin, harness_kind, session_id);

CREATE INDEX IF NOT EXISTS binding_record_origin_idx ON binding_record(origin);
