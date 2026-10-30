-- Schema v6. Turso-safe
-- dialect, same rules as 001 and 002: CREATE TABLE IF NOT EXISTS only, no
-- AUTOINCREMENT, no WITHOUT ROWID, no triggers/views/FTS/virtual tables, no
-- RETURNING. rev is an INTEGER PRIMARY KEY rowid alias, which auto-assigns on
-- insert exactly as AUTOINCREMENT would; AUTOINCREMENT itself is forbidden by
-- 001's header. Timestamps are RFC3339 UTC text with millisecond precision.

CREATE TABLE IF NOT EXISTS config_revision (
    rev      INTEGER PRIMARY KEY,   -- rowid alias: 1, 2, 3 ... (never reused)
    at       TEXT NOT NULL,         -- RFC3339 UTC milli (db.formatTime)
    source   TEXT NOT NULL,         -- see internal/config §3.3
    message  TEXT NOT NULL,         -- "" allowed
    version  INTEGER NOT NULL,      -- config_meta.version after this write
    changes  TEXT NOT NULL,         -- JSON array of config.Change; "[]" only for a baseline
    snapshot TEXT NOT NULL          -- JSON object: the whole config document after the write
);
