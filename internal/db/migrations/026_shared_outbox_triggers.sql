-- Schema v26: the trigger that says the config doc changed.
--
-- A write to any config_doc section records one row in shared_outbox naming the
-- doc and nothing else. The row is a signal to diff the doc against what was
-- last exported, not a payload: the value is read out of the doc at export time,
-- so the signal cannot hold a copy that has since gone stale, and a second write
-- before the export is still one row.
--
-- The sync section is left out because it never syncs, so a write to it has
-- nothing to export. A trigger writes one row and never raises.
--
-- The seed row makes a file whose config predates this migration export it once:
-- the triggers do not see rows written before they existed.
--
-- Compatibility rules: internal/db/migrations/README.md.

CREATE TRIGGER IF NOT EXISTS shared_outbox_config_doc_ins AFTER INSERT ON config_doc
WHEN NEW.name <> 'sync' BEGIN
    INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
    VALUES ('config', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
CREATE TRIGGER IF NOT EXISTS shared_outbox_config_doc_upd AFTER UPDATE ON config_doc
WHEN NEW.name <> 'sync' BEGIN
    INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
    VALUES ('config', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
CREATE TRIGGER IF NOT EXISTS shared_outbox_config_doc_del AFTER DELETE ON config_doc
WHEN OLD.name <> 'sync' BEGIN
    INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
    VALUES ('config', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;

INSERT OR IGNORE INTO shared_outbox (doc, dirtied_at)
VALUES ('config', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
