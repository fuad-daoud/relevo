-- Schema v28: when each shared config item was last written on this machine.
--
-- The exporter stamps an item with this value, so "newest wins" means newest
-- written rather than newest exported: a machine that was offline or locked
-- when it edited an item does not outrank an edit made elsewhere afterwards.
-- The key is the doc key of one item (or of a whole section for a section
-- without items); a removed item keeps its row as the stamp of its tombstone.
--
-- Machine-local, reached through LocalOrSelf like the other doc bookkeeping:
-- a stamp is about what this machine wrote and must not travel. An item that
-- arrived from a peer records no row here.
--
-- Compatibility rules: internal/db/migrations/README.md.

CREATE TABLE IF NOT EXISTS config_write_stamp (
    key TEXT PRIMARY KEY,
    stamp TEXT NOT NULL
);
