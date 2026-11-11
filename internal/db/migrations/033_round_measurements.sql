-- Schema v33: a round's base/burst sample, pressure stall totals and memory
-- event counts, copied from the report entry's rusage. Every column stays
-- NULL on rounds whose report carried no rusage, on rows recorded before this
-- migration, and on the measurements a pre-change report never named, so old
-- rows keep reading back.
--
-- Scope: mirror `round` is shared history (inherits origin through
-- `binding_id`). Row exchange needs no sync-side change: `readExchangeRow`
-- discovers columns from `pragma_table_info`, and the round triggers carry
-- only the pk.
--
-- Compatibility rules: internal/db/migrations/README.md. Add-only and
-- Turso-safe: ALTER TABLE ADD COLUMN only, relying on applyOneMigration's
-- version guard because ADD COLUMN has no IF NOT EXISTS.

ALTER TABLE round ADD COLUMN rusage_base_bytes INTEGER;
ALTER TABLE round ADD COLUMN rusage_burst_bytes INTEGER;
ALTER TABLE round ADD COLUMN rusage_burst_secs INTEGER;
ALTER TABLE round ADD COLUMN rusage_cpu_some_usec INTEGER;
ALTER TABLE round ADD COLUMN rusage_cpu_full_usec INTEGER;
ALTER TABLE round ADD COLUMN rusage_mem_some_usec INTEGER;
ALTER TABLE round ADD COLUMN rusage_mem_full_usec INTEGER;
ALTER TABLE round ADD COLUMN rusage_io_some_usec INTEGER;
ALTER TABLE round ADD COLUMN rusage_io_full_usec INTEGER;
ALTER TABLE round ADD COLUMN rusage_ev_high INTEGER;
ALTER TABLE round ADD COLUMN rusage_ev_max INTEGER;
ALTER TABLE round ADD COLUMN rusage_ev_oom_kill INTEGER;
