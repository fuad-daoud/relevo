-- Schema v24: the first sequence this machine has verified a signed entry at,
-- per origin. A remote that deletes or raises its own signed-from meta cannot
-- switch checking off for an origin this machine has already seen sign.
--
-- Machine-local and trigger-free, beside sync_import_mark and for the same
-- reason: it is about what this machine has verified, so it is neither in the
-- shared table list nor in the outbox, and it commits in the same transaction
-- as the batch that wrote it.
--
-- Compatibility rules: internal/db/migrations/README.md.

CREATE TABLE IF NOT EXISTS sync_signed_from (
    origin TEXT PRIMARY KEY,
    seq INTEGER NOT NULL
);
