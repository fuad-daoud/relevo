-- Schema v30: the epoch cut this machine has verified for each origin -- the
-- epoch that origin's entries above the cut must verify under, and the
-- sequence the cut sits at. A remote that deletes, lowers or replays its own
-- cut row cannot switch checking off for a cut this machine has already
-- verified.
--
-- Machine-local and trigger-free, beside sync_signed_from and for the same
-- reason: it is about what this machine has verified, so it is neither in the
-- shared table list nor in the outbox, and it commits alongside the import
-- run that verified the row.
--
-- Compatibility rules: internal/db/migrations/README.md.

CREATE TABLE IF NOT EXISTS sync_epoch_cut (
    origin TEXT PRIMARY KEY,
    epoch INTEGER NOT NULL,
    seq INTEGER NOT NULL
);
