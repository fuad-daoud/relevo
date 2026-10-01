-- Schema v20: relevo no longer opens or records the MasterMind's own
-- transcript, so the planner-owned transcript rows and the cursors that tracked
-- them are state no reader wants. One DELETE per kind of derived data, nothing
-- else touched.
--
-- The mastermind.transcript_locator column is deliberately left in place.
-- Migrations are add-only where possible (internal/db/migrations/README.md), and
-- dropping the column would be a table rebuild whose Turso support is not
-- pinned. The column holds a path, not transcript content, so keeping it costs
-- nothing: this binary never writes it and never reads it.
--
-- The transcript table and its transcript_owner_seq_uidx unique index stay:
-- legacy round rows still live there.
--
-- Compatibility rules: internal/db/migrations/README.md.

DELETE FROM transcript WHERE owner_kind = 'planner';
DELETE FROM ingest_cursor WHERE source LIKE 'planner::%';
