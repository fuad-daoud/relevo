-- Schema v25: the machine-local bookkeeping a shared doc needs, and the local
-- layer of the config doc.
--
-- A doc entry rides the ordinary log as an ordinary entry whose table name
-- carries the `doc:` prefix, so nothing here travels by itself: the log is the
-- transport and these tables are what this machine remembers about it. That is
-- why all five are machine-local and trigger-free, like sync_import_mark
-- beside them -- a row here is about what this machine has sent, seen or
-- parked, and two machines sharing one would each believe the other had done
-- it. They are reached through LocalOrSelf, which binds them to the local file
-- whether or not a split has run.
--
-- The tables arrive empty and carry no trigger: a trigger on config_doc goes
-- in its own migration, so no outbox row is written before an exporter exists
-- to drain one.
--
-- shared_outbox says which doc changed and nothing else. The row is a signal
-- to diff the doc, not a payload: the value is read out of the doc itself at
-- export time, so the outbox cannot hold a copy that has since gone stale.
-- Keyed by doc alone so a second write of the same doc is still one signal.
CREATE TABLE IF NOT EXISTS shared_outbox (
    doc TEXT PRIMARY KEY,
    dirtied_at TEXT NOT NULL
);

-- shared_export_state is what this machine last put on the log for each key,
-- as (origin, stamp). The exporter diffs the doc's current items against this
-- table and emits an entry per key that differs, which is what makes an export
-- empty when nothing changed and what makes an import write no second revision
-- for a value that came from here in the first place. A join clears it, so the
-- first export after a join re-emits every key the new peer had never seen.
-- origin is NOT NULL because it is half of what says whether an arriving
-- (stamp, origin) is this machine's own echo or another machine's news.
CREATE TABLE IF NOT EXISTS shared_export_state (
    doc TEXT NOT NULL,
    key TEXT NOT NULL,
    origin TEXT NOT NULL,
    stamp TEXT NOT NULL,
    PRIMARY KEY (doc, key)
);

-- shared_applied is the newest doc state this machine has applied for each key,
-- again as (origin, stamp). It is what makes a doc apply idempotent: a batch
-- that is redelivered after a crash carries the same (stamp, origin) as the row
-- here, is a no-op, and writes no second revision.
CREATE TABLE IF NOT EXISTS shared_applied (
    doc TEXT NOT NULL,
    key TEXT NOT NULL,
    origin TEXT NOT NULL,
    stamp TEXT NOT NULL,
    PRIMARY KEY (doc, key)
);

-- shared_pending holds doc entries this machine could not verify yet, because
-- it has no key. The entry is parked whole, because it is only its signature
-- that is missing: an entry stripped of the fields the signature covers could
-- not be checked when the key arrives. Keyed by (origin, seq) rather than by
-- doc and key because the same key may be delivered twice before it is
-- verified, and one parked row per sequence is the only way to re-deliver it.
CREATE TABLE IF NOT EXISTS shared_pending (
    origin TEXT NOT NULL,
    seq INTEGER NOT NULL,
    entry TEXT NOT NULL,
    parked_at TEXT NOT NULL,
    PRIMARY KEY (origin, seq)
);

-- config_local is the machine-local layer of the config doc: one merge-patch
-- fragment per key, applied over the shared body on read. It holds the fields
-- that name things on this machine -- an account's config dir and home, the
-- policy's serve settings -- which are meaningless elsewhere, so they are read
-- from here and written only here and never exported.
CREATE TABLE IF NOT EXISTS config_local (
    key TEXT PRIMARY KEY,
    patch TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Compatibility rules: internal/db/migrations/README.md.