-- Schema v29: availability events and latency samples as shared tables.
--
-- Until now both were one rewritten kv document per machine. A row that nobody
-- may read is a fine shape for a document, and a poor one for history: every
-- append rewrote the whole window, a peer could hold none of it, and the only
-- way to prune was to filter in memory on every read. Here each event and each
-- sample is a row, so an append is one insert, a peer can read the window, and
-- age can be a delete.
--
-- Both are roots: a row carries its own origin and resolves to it without a hop.
-- They sort last in SharedTables because neither has a foreign key at all.
--
-- The tables are append-only, and that is what shapes the triggers below. A row
-- is never updated -- an upsert of an id this file already holds changes nothing,
-- because the row is immutable -- and it is deleted only by age. So each table
-- gets an insert trigger alone. There is no update trigger and no delete trigger,
-- and that is the whole of the trigger-skip mechanism: a delete by age reaches
-- the outbox through nothing at all, so it exports nothing without a WHEN clause
-- to age-test, without a clock in SQL, and without a marker on the connection to
-- say which of the two it was. Every connection keeps the identical trigger set,
-- which is the rule internal/db/migrations/README.md holds triggers to.
--
-- Time is stored as at_ms, Unix milliseconds in UTC, and never as RFC3339 text.
-- Today's events carry mixed offsets (+03:00 and Z) and variable-width
-- nanoseconds, and a text comparison misorders them: '2026-10-14T09:00:00Z' sorts
-- after '2026-10-14T10:00:00+03:00' because 'Z' is greater than '0'. An integer
-- orders them correctly, and it is what the age delete compares against. The
-- conversion happens at the write and at the migration; the read converts back
-- to a time.Time.
--
-- The tables arrive empty and nothing writes them yet: the kv availability and
-- latency documents stay authoritative for reads until they are migrated, so a
-- file at this version behaves exactly as the file before it did.

-- One availability observation: a ledger entry mirrored into the 30-day window
-- the ledger itself does not keep. at_ms is when the observation happened,
-- since_ms is on a cleared event the at of the oldest ledger entry the clear
-- removed (At - Since is how long the provider was blocked) and NULL on every
-- other kind, which is how a cleared event that cleared nothing reads.
CREATE TABLE IF NOT EXISTS availability_event (
    id TEXT PRIMARY KEY,
    at_ms INTEGER NOT NULL,
    kind TEXT NOT NULL,
    provider TEXT NOT NULL,
    account TEXT NOT NULL DEFAULT '',
    token TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    binding TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    since_ms INTEGER,
    origin TEXT NOT NULL
);

-- One probe of one candidate: how long it took to produce its first model
-- output and how long the whole run took. err is empty on success and a sample
-- counts as successful only when it is. host is kept because a latency figure
-- is only readable next to the machine that produced it.
CREATE TABLE IF NOT EXISTS latency_sample (
    id TEXT PRIMARY KEY,
    at_ms INTEGER NOT NULL,
    token TEXT NOT NULL,
    host TEXT NOT NULL DEFAULT '',
    ttft_ms INTEGER NOT NULL,
    total_ms INTEGER NOT NULL,
    err TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL
);

-- Append-only, so one trigger each. See the header: a delete is the age prune
-- and deliberately records nothing.
CREATE TRIGGER IF NOT EXISTS sync_outbox_availability_event_ins AFTER INSERT ON availability_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('availability_event', json_array(NEW.id), 'insert', NEW.origin);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_latency_sample_ins AFTER INSERT ON latency_sample BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('latency_sample', json_array(NEW.id), 'insert', NEW.origin);
END;

-- Compatibility rules: internal/db/migrations/README.md.
-- Scope classification: internal/db/migrations/SCOPES.md.