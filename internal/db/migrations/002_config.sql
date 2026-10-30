-- Schema v2. Turso-safe
-- dialect, same rules as 001: CREATE TABLE IF NOT EXISTS only, no
-- AUTOINCREMENT, no WITHOUT ROWID, no triggers/views/FTS/virtual tables, no
-- RETURNING. config_import.id is an INTEGER PRIMARY KEY rowid alias, which
-- auto-assigns on insert exactly as AUTOINCREMENT would; AUTOINCREMENT itself
-- is forbidden by 001's header. Timestamps are RFC3339 UTC text with
-- millisecond precision.

CREATE TABLE IF NOT EXISTS config_doc (
    name TEXT PRIMARY KEY,
    body TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);

INSERT OR IGNORE INTO config_meta (id, version) VALUES (1, 0);

CREATE TABLE IF NOT EXISTS secret (
    name TEXT PRIMARY KEY,
    value BLOB NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS kv (
    key TEXT PRIMARY KEY,
    value_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_import (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    source_path TEXT NOT NULL,
    body BLOB NOT NULL,
    imported_at TEXT NOT NULL
);
