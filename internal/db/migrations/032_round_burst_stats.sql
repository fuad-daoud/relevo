-- Schema v32: a local round's queue wait, start kind and resource usage.
-- queue_wait_ms is admitTS minus queuedTS in milliseconds; start_kind is
-- 'local' on the host that ran the round; rusage_cpu_ms and
-- rusage_peak_mem_bytes copy the report entry's rusage. Every column stays
-- NULL on rounds that never reported, on rounds that never queued, and on rows
-- recorded before this migration, so old rows keep reading back.
--
-- Scope: mirror `round` is shared history (inherits origin through
-- `binding_id`). Row exchange needs no sync-side change: `readExchangeRow`
-- discovers columns from `pragma_table_info`, and the round triggers carry
-- only the pk.
--
-- Compatibility rules: internal/db/migrations/README.md. Add-only and
-- Turso-safe: ALTER TABLE ADD COLUMN only, relying on applyOneMigration's
-- version guard because ADD COLUMN has no IF NOT EXISTS.

ALTER TABLE round ADD COLUMN queue_wait_ms INTEGER;
ALTER TABLE round ADD COLUMN start_kind TEXT;
ALTER TABLE round ADD COLUMN rusage_cpu_ms INTEGER;
ALTER TABLE round ADD COLUMN rusage_peak_mem_bytes INTEGER;
