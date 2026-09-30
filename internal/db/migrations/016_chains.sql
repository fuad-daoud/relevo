-- Schema v16: a chain is one row in chains plus ordinary bindings, its
-- members, and one append-only trace row per transition in chain_event. The
-- chains row owns the state machine's state, the resolved settings, the plan
-- copies it holds and the four member binding names; membership lives here and
-- nowhere else, so a member's close finds its chain by a single indexed
-- lookup on those columns.
--
-- event and action stay JSON strings, so a later slice adds fields to them
-- without a migration. reason carries a halt reason when there is one.
--
-- Compatibility rules: internal/db/migrations/README.md. Add-only and
-- Turso-safe like 001-015: CREATE TABLE/INDEX IF NOT EXISTS only -- no
-- triggers, views, FTS or virtual tables, no RETURNING.

CREATE TABLE IF NOT EXISTS chains (
    id TEXT PRIMARY KEY,
    origin TEXT NOT NULL DEFAULT '',
    owner TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    phase TEXT NOT NULL,
    step TEXT NOT NULL,
    plan INTEGER NOT NULL,
    plans INTEGER NOT NULL,
    plan_paths TEXT NOT NULL,            -- JSON array of the stored plan copies
    corrections INTEGER NOT NULL DEFAULT 0,
    awaiting_member TEXT NOT NULL DEFAULT '',
    awaiting_round INTEGER NOT NULL DEFAULT 0,
    settings TEXT NOT NULL DEFAULT '{}', -- JSON of the resolved settings
    builder TEXT NOT NULL,
    reviewer TEXT NOT NULL DEFAULT '',
    planner TEXT NOT NULL DEFAULT '',
    security TEXT NOT NULL DEFAULT '',
    base TEXT NOT NULL DEFAULT '',
    branch TEXT NOT NULL DEFAULT '',
    repo TEXT NOT NULL DEFAULT '',
    worktree TEXT NOT NULL DEFAULT '',
    feature TEXT NOT NULL DEFAULT '',
    ticket TEXT NOT NULL DEFAULT '',
    server TEXT NOT NULL DEFAULT '',     -- reserved for slice 3
    mastermind_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS chains_origin_owner_name_uidx ON chains(origin, owner, name);
CREATE INDEX IF NOT EXISTS chains_owner_builder_idx  ON chains(owner, builder);
CREATE INDEX IF NOT EXISTS chains_owner_reviewer_idx ON chains(owner, reviewer);
CREATE INDEX IF NOT EXISTS chains_owner_planner_idx  ON chains(owner, planner);
CREATE INDEX IF NOT EXISTS chains_owner_security_idx ON chains(owner, security);

CREATE TABLE IF NOT EXISTS chain_event (
    chain_id TEXT NOT NULL REFERENCES chains(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    ts TEXT NOT NULL,
    phase TEXT NOT NULL,
    step TEXT NOT NULL,
    member TEXT NOT NULL,
    round INTEGER NOT NULL,
    event TEXT NOT NULL,   -- JSON: {"kind":"builder_closed","outcome":"done","gate":"green"}
    action TEXT NOT NULL,  -- JSON: {"kind":"send","member":"x-rev","seed":"reviewer"}
    reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (chain_id, seq)
);
