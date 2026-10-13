-- Schema v27: a doc signal no two writes share.
--
-- The v26 triggers stamp the signal with a millisecond, and the exporter clears
-- the signal it read only while the stamp is unchanged. Two writes in one
-- millisecond left the same stamp, so a write landing between the exporter's
-- read and its clear was cleared unexported. The stamp now ends in random
-- bytes, so a later write always replaces the signal with a different value.
--
-- A trigger writes one row and never raises.
--
-- Compatibility rules: internal/db/migrations/README.md.

DROP TRIGGER IF EXISTS shared_outbox_config_doc_ins;
DROP TRIGGER IF EXISTS shared_outbox_config_doc_upd;
DROP TRIGGER IF EXISTS shared_outbox_config_doc_del;

CREATE TRIGGER IF NOT EXISTS shared_outbox_config_doc_ins AFTER INSERT ON config_doc
WHEN NEW.name <> 'sync' BEGIN
    INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
    VALUES ('config', strftime('%Y-%m-%dT%H:%M:%fZ', 'now') || '-' || hex(randomblob(8)));
END;
CREATE TRIGGER IF NOT EXISTS shared_outbox_config_doc_upd AFTER UPDATE ON config_doc
WHEN NEW.name <> 'sync' BEGIN
    INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
    VALUES ('config', strftime('%Y-%m-%dT%H:%M:%fZ', 'now') || '-' || hex(randomblob(8)));
END;
CREATE TRIGGER IF NOT EXISTS shared_outbox_config_doc_del AFTER DELETE ON config_doc
WHEN OLD.name <> 'sync' BEGIN
    INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
    VALUES ('config', strftime('%Y-%m-%dT%H:%M:%fZ', 'now') || '-' || hex(randomblob(8)));
END;
