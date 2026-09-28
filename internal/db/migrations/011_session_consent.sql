-- Schema v11: a session's own consent answer and the last status token it was
-- told, so a hook process can remember both across separate invocations.
-- answer is 'yes', 'no', or NULL (unset: the session has no answer of its own
-- and the repository's answer applies); answer_at is when it was written.
-- told is the status token last delivered to the session (see the mastermind
-- package), NULL when nothing has been delivered yet; told_at is when.
--
-- The composite primary key covers the only lookup, (harness_kind, session_id),
-- so no extra index is needed. Same Turso-safe dialect as 001-010: CREATE TABLE
-- IF NOT EXISTS only, no WITHOUT ROWID, no triggers/views/FTS/virtual tables,
-- no RETURNING. applyOneMigration runs the file once, in one transaction.

CREATE TABLE IF NOT EXISTS session_consent (
    harness_kind TEXT NOT NULL,
    session_id TEXT NOT NULL,
    answer TEXT,
    answer_at TEXT,
    told TEXT,
    told_at TEXT,
    PRIMARY KEY (harness_kind, session_id)
);