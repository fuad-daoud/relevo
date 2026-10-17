-- Schema v31: binding_record and binding gain writer column; outbox triggers
-- attribute roots by NEW.writer on insert, OLD.writer on update/delete;
-- children resolve writer through their root. Four machine-local tables support
-- terms, hand-overs and fences.
--
-- Compatibility rules: internal/db/migrations/README.md.

-- 1. Drop the 24 outbox triggers before updating anything.
DROP TRIGGER IF EXISTS sync_outbox_binding_record_ins;
DROP TRIGGER IF EXISTS sync_outbox_binding_record_upd;
DROP TRIGGER IF EXISTS sync_outbox_binding_record_del;
DROP TRIGGER IF EXISTS sync_outbox_binding_ins;
DROP TRIGGER IF EXISTS sync_outbox_binding_upd;
DROP TRIGGER IF EXISTS sync_outbox_binding_del;
DROP TRIGGER IF EXISTS sync_outbox_binding_event_ins;
DROP TRIGGER IF EXISTS sync_outbox_binding_event_upd;
DROP TRIGGER IF EXISTS sync_outbox_binding_event_del;
DROP TRIGGER IF EXISTS sync_outbox_round_file_ins;
DROP TRIGGER IF EXISTS sync_outbox_round_file_upd;
DROP TRIGGER IF EXISTS sync_outbox_round_file_del;
DROP TRIGGER IF EXISTS sync_outbox_round_ins;
DROP TRIGGER IF EXISTS sync_outbox_round_upd;
DROP TRIGGER IF EXISTS sync_outbox_round_del;
DROP TRIGGER IF EXISTS sync_outbox_event_ins;
DROP TRIGGER IF EXISTS sync_outbox_event_upd;
DROP TRIGGER IF EXISTS sync_outbox_event_del;
DROP TRIGGER IF EXISTS sync_outbox_artifact_ins;
DROP TRIGGER IF EXISTS sync_outbox_artifact_upd;
DROP TRIGGER IF EXISTS sync_outbox_artifact_del;
DROP TRIGGER IF EXISTS sync_outbox_transcript_ins;
DROP TRIGGER IF EXISTS sync_outbox_transcript_upd;
DROP TRIGGER IF EXISTS sync_outbox_transcript_del;

-- 2. Add writer column, backfill from origin, and create indexes.
ALTER TABLE binding_record ADD COLUMN writer TEXT NOT NULL DEFAULT '';
ALTER TABLE binding ADD COLUMN writer TEXT NOT NULL DEFAULT '';
UPDATE binding_record SET writer = origin;
UPDATE binding SET writer = origin;
CREATE INDEX IF NOT EXISTS binding_record_writer_idx ON binding_record(writer);
CREATE INDEX IF NOT EXISTS binding_writer_idx ON binding(writer);
CREATE UNIQUE INDEX IF NOT EXISTS binding_record_writer_owner_name_uidx ON binding_record(writer, owner, name) WHERE archived_at IS NULL;

-- 3. Recreate the 24 triggers with writer resolution.
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_record_ins AFTER INSERT ON binding_record BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_record', json_array(NEW.id), 'insert', NEW.writer);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_record_upd AFTER UPDATE ON binding_record BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_record', json_array(NEW.id), 'update', OLD.writer);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_record_del AFTER DELETE ON binding_record BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_record', json_array(OLD.id), 'delete', OLD.writer);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_ins AFTER INSERT ON binding BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding', json_array(NEW.id), 'insert', NEW.writer);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_upd AFTER UPDATE ON binding BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding', json_array(NEW.id), 'update', OLD.writer);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_del AFTER DELETE ON binding BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding', json_array(OLD.id), 'delete', OLD.writer);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_event_ins AFTER INSERT ON binding_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_event', json_array(NEW.record_id, NEW.seq), 'insert',
        (SELECT writer FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_event_upd AFTER UPDATE ON binding_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_event', json_array(NEW.record_id, NEW.seq), 'update',
        (SELECT writer FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_event_del AFTER DELETE ON binding_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_event', json_array(OLD.record_id, OLD.seq), 'delete',
        (SELECT writer FROM binding_record WHERE id = OLD.record_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_round_file_ins AFTER INSERT ON round_file BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round_file', json_array(NEW.record_id, NEW.name), 'insert',
        (SELECT writer FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_file_upd AFTER UPDATE ON round_file BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round_file', json_array(NEW.record_id, NEW.name), 'update',
        (SELECT writer FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_file_del AFTER DELETE ON round_file BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round_file', json_array(OLD.record_id, OLD.name), 'delete',
        (SELECT writer FROM binding_record WHERE id = OLD.record_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_round_ins AFTER INSERT ON round BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', json_array(NEW.id), 'insert',
        (SELECT writer FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_upd AFTER UPDATE ON round BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', json_array(NEW.id), 'update',
        (SELECT writer FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_del AFTER DELETE ON round BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', json_array(OLD.id), 'delete',
        (SELECT writer FROM binding WHERE id = OLD.binding_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_event_ins AFTER INSERT ON event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('event', json_array(NEW.id), 'insert',
        (SELECT writer FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_event_upd AFTER UPDATE ON event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('event', json_array(NEW.id), 'update',
        (SELECT writer FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_event_del AFTER DELETE ON event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('event', json_array(OLD.id), 'delete',
        (SELECT writer FROM binding WHERE id = OLD.binding_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_artifact_ins AFTER INSERT ON artifact BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('artifact', json_array(NEW.id), 'insert',
        (SELECT writer FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.round_id)));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_artifact_upd AFTER UPDATE ON artifact BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('artifact', json_array(NEW.id), 'update',
        (SELECT writer FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.round_id)));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_artifact_del AFTER DELETE ON artifact BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('artifact', json_array(OLD.id), 'delete',
        (SELECT writer FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = OLD.round_id)));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_transcript_ins AFTER INSERT ON transcript BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('transcript', json_array(NEW.id), 'insert',
        CASE NEW.owner_kind
            WHEN 'round' THEN (SELECT writer FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.owner_id))
            WHEN 'mastermind' THEN (SELECT origin FROM mastermind WHERE id = NEW.owner_id)
            ELSE NULL
        END);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_transcript_upd AFTER UPDATE ON transcript BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('transcript', json_array(NEW.id), 'update',
        CASE NEW.owner_kind
            WHEN 'round' THEN (SELECT writer FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.owner_id))
            WHEN 'mastermind' THEN (SELECT origin FROM mastermind WHERE id = NEW.owner_id)
            ELSE NULL
        END);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_transcript_del AFTER DELETE ON transcript BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('transcript', json_array(OLD.id), 'delete',
        CASE OLD.owner_kind
            WHEN 'round' THEN (SELECT writer FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = OLD.owner_id))
            WHEN 'mastermind' THEN (SELECT origin FROM mastermind WHERE id = OLD.owner_id)
            ELSE NULL
        END);
END;

-- 4. Machine-local, trigger-free tables.
CREATE TABLE IF NOT EXISTS sync_binding_term (
    root_id TEXT NOT NULL,
    writer TEXT NOT NULL,
    upto INTEGER NOT NULL,
    regrant_origin TEXT,
    regrant_seq INTEGER,
    PRIMARY KEY (root_id, writer)
);

CREATE TABLE IF NOT EXISTS sync_writer_after (
    root_id TEXT PRIMARY KEY,
    after_origin TEXT NOT NULL,
    after_seq INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_binding_left (
    root_id TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    to_writer TEXT NOT NULL,
    kind TEXT NOT NULL,
    at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_fenced_change (
    root_id TEXT NOT NULL,
    tbl TEXT NOT NULL,
    pk TEXT NOT NULL,
    at TEXT NOT NULL,
    PRIMARY KEY (root_id, tbl, pk)
);
