-- Schema v1. Turso-safe
-- dialect: CREATE TABLE/INDEX IF NOT EXISTS only, no AUTOINCREMENT, no
-- WITHOUT ROWID, no triggers/views/FTS/virtual tables, no RETURNING. Ids are
-- text ULIDs; timestamps are RFC3339 UTC text with millisecond precision;
-- booleans are INTEGER 0/1.

CREATE TABLE IF NOT EXISTS repo (
    id TEXT PRIMARY KEY,
    origin_url TEXT,
    common_dir TEXT,
    first_seen TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS repo_origin_url_uidx ON repo(origin_url) WHERE origin_url IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS repo_common_dir_uidx ON repo(common_dir) WHERE common_dir IS NOT NULL;

CREATE TABLE IF NOT EXISTS planner (
    id TEXT PRIMARY KEY,
    harness_kind TEXT NOT NULL,
    session_id TEXT NOT NULL,
    transcript_locator TEXT,
    first_seen TEXT NOT NULL,
    last_seen TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS planner_harness_session_uidx ON planner(harness_kind, session_id);

CREATE TABLE IF NOT EXISTS binding (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    repo_id TEXT REFERENCES repo(id),
    planner_id TEXT REFERENCES planner(id),
    feature TEXT,
    forked_from_binding_id TEXT REFERENCES binding(id),
    forked_from_round INTEGER,
    cwd TEXT NOT NULL,
    worktree TEXT,
    branch TEXT,
    base_commit TEXT,
    tier TEXT,
    gate TEXT,
    builder_mode TEXT NOT NULL,
    server TEXT,
    created_at TEXT NOT NULL,
    final_state TEXT,
    archived_at TEXT,
    archive_path TEXT,
    ingest_source TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS binding_name_created_at_uidx ON binding(name, created_at);
CREATE INDEX IF NOT EXISTS binding_repo_id_idx ON binding(repo_id);
CREATE INDEX IF NOT EXISTS binding_planner_id_idx ON binding(planner_id);
CREATE INDEX IF NOT EXISTS binding_feature_idx ON binding(feature);

CREATE TABLE IF NOT EXISTS round (
    id TEXT PRIMARY KEY,
    binding_id TEXT NOT NULL REFERENCES binding(id),
    number INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    closed_at TEXT,
    outcome TEXT NOT NULL,
    builder_candidate TEXT,
    builder_harness TEXT,
    builder_provider TEXT,
    builder_model TEXT,
    builder_mode TEXT,
    tier TEXT,
    commits INTEGER,
    tree TEXT,
    gate_result TEXT,
    gate_exit INTEGER,
    gate_duration_ms INTEGER,
    in_tokens INTEGER,
    cache_tokens INTEGER,
    write_tokens INTEGER,
    out_tokens INTEGER,
    cost_usd REAL,
    cost_basis TEXT,
    report_outcome TEXT,
    switches INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS round_binding_number_uidx ON round(binding_id, number);
CREATE INDEX IF NOT EXISTS round_started_at_idx ON round(started_at);
CREATE INDEX IF NOT EXISTS round_builder_idx ON round(builder_harness, builder_provider, builder_model);

CREATE TABLE IF NOT EXISTS event (
    id TEXT PRIMARY KEY,
    binding_id TEXT NOT NULL REFERENCES binding(id),
    round_id TEXT REFERENCES round(id),
    seq INTEGER NOT NULL,
    ts TEXT NOT NULL,
    kind TEXT NOT NULL,
    direction TEXT NOT NULL,
    note TEXT,
    path TEXT,
    delivered_at TEXT,
    confirmed INTEGER NOT NULL,
    late INTEGER NOT NULL,
    flagged INTEGER,
    flagged_by TEXT,
    entry_json TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS event_binding_seq_uidx ON event(binding_id, seq);
CREATE INDEX IF NOT EXISTS event_binding_round_seq_idx ON event(binding_id, round_id, seq);

CREATE TABLE IF NOT EXISTS artifact (
    id TEXT PRIMARY KEY,
    round_id TEXT NOT NULL REFERENCES round(id),
    kind TEXT NOT NULL,
    consult_id TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    captured_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS artifact_round_kind_consult_uidx ON artifact(round_id, kind, consult_id);

CREATE TABLE IF NOT EXISTS transcript (
    id TEXT PRIMARY KEY,
    owner_kind TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    ts TEXT,
    record_json TEXT NOT NULL,
    rendered TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS transcript_owner_seq_uidx ON transcript(owner_kind, owner_id, seq);

CREATE TABLE IF NOT EXISTS ingest_cursor (
    source TEXT PRIMARY KEY,
    byte_offset INTEGER NOT NULL,
    head_sha TEXT NOT NULL,
    whole_sha TEXT,
    updated_at TEXT NOT NULL
);
