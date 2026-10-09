-- Schema v22: the outbox every shared-table write lands in. Each shared table
-- gets three AFTER triggers -- insert, update and delete -- that append
-- (tbl, pk, op, origin) here, so a later sync round can drain the change log
-- without asking any writer to report anything. Triggers fire for every
-- connection and every writer, which is the point: there is no "every write
-- must go through X" rule to keep.
--
-- pk is json_array() of the table's primary-key columns, for one-column keys
-- as well as several, so a reader parses one shape. An entry is an identity and
-- an operation, not a payload: the row's state is read at drain time.
--
-- origin is resolved inside the trigger rather than passed in, so a delete
-- still knows its owner. A child row's owner is read through its parent, and a
-- parent that is already gone -- a child removed by a cascade -- resolves to
-- NULL: the entry is recorded with no owner and the reader skips it, because
-- the parent's own delete entry cascades on every importer. A trigger never
-- fails the statement that fired it.
--
-- installation is a root table like any other, and its id is the installation
-- id, so its owner is the row itself.
--
-- The triggers do not see rows written before this migration: a database that
-- upgrades has an empty outbox until its next write, and the reconcile that
-- compares this origin's rows against the remote covers that gap.
--
-- Compatibility rules: internal/db/migrations/README.md. CREATE TABLE,
-- CREATE TRIGGER and AUTOINCREMENT are all IF NOT EXISTS here, and this file
-- still runs once, in one transaction, under applyOneMigration's version guard.

CREATE TABLE IF NOT EXISTS sync_outbox (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    tbl TEXT NOT NULL,
    pk TEXT NOT NULL,
    op TEXT NOT NULL,
    origin TEXT
);

-- A root table that carries its own owner column.
CREATE TRIGGER IF NOT EXISTS sync_outbox_repo_ins AFTER INSERT ON repo BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('repo', json_array(NEW.id), 'insert', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_repo_upd AFTER UPDATE ON repo BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('repo', json_array(NEW.id), 'update', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_repo_del AFTER DELETE ON repo BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('repo', json_array(OLD.id), 'delete', OLD.origin);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_mastermind_ins AFTER INSERT ON mastermind BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('mastermind', json_array(NEW.id), 'insert', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_mastermind_upd AFTER UPDATE ON mastermind BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('mastermind', json_array(NEW.id), 'update', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_mastermind_del AFTER DELETE ON mastermind BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('mastermind', json_array(OLD.id), 'delete', OLD.origin);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_record_ins AFTER INSERT ON binding_record BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_record', json_array(NEW.id), 'insert', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_record_upd AFTER UPDATE ON binding_record BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_record', json_array(NEW.id), 'update', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_record_del AFTER DELETE ON binding_record BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_record', json_array(OLD.id), 'delete', OLD.origin);
END;

-- The id is the installation id, so the row names its own owner.
CREATE TRIGGER IF NOT EXISTS sync_outbox_installation_ins AFTER INSERT ON installation BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('installation', json_array(NEW.id), 'insert', NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_installation_upd AFTER UPDATE ON installation BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('installation', json_array(NEW.id), 'update', NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_installation_del AFTER DELETE ON installation BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('installation', json_array(OLD.id), 'delete', OLD.id);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_ins AFTER INSERT ON binding BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding', json_array(NEW.id), 'insert', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_upd AFTER UPDATE ON binding BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding', json_array(NEW.id), 'update', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_del AFTER DELETE ON binding BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding', json_array(OLD.id), 'delete', OLD.origin);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_chains_ins AFTER INSERT ON chains BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chains', json_array(NEW.id), 'insert', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chains_upd AFTER UPDATE ON chains BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chains', json_array(NEW.id), 'update', NEW.origin);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chains_del AFTER DELETE ON chains BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chains', json_array(OLD.id), 'delete', OLD.origin);
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_event_ins AFTER INSERT ON binding_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_event', json_array(NEW.record_id, NEW.seq), 'insert',
        (SELECT origin FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_event_upd AFTER UPDATE ON binding_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_event', json_array(NEW.record_id, NEW.seq), 'update',
        (SELECT origin FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_binding_event_del AFTER DELETE ON binding_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('binding_event', json_array(OLD.record_id, OLD.seq), 'delete',
        (SELECT origin FROM binding_record WHERE id = OLD.record_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_round_file_ins AFTER INSERT ON round_file BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round_file', json_array(NEW.record_id, NEW.name), 'insert',
        (SELECT origin FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_file_upd AFTER UPDATE ON round_file BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round_file', json_array(NEW.record_id, NEW.name), 'update',
        (SELECT origin FROM binding_record WHERE id = NEW.record_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_file_del AFTER DELETE ON round_file BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round_file', json_array(OLD.record_id, OLD.name), 'delete',
        (SELECT origin FROM binding_record WHERE id = OLD.record_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_event_ins AFTER INSERT ON chain_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_event', json_array(NEW.chain_id, NEW.seq), 'insert',
        (SELECT origin FROM chains WHERE id = NEW.chain_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_event_upd AFTER UPDATE ON chain_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_event', json_array(NEW.chain_id, NEW.seq), 'update',
        (SELECT origin FROM chains WHERE id = NEW.chain_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_event_del AFTER DELETE ON chain_event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_event', json_array(OLD.chain_id, OLD.seq), 'delete',
        (SELECT origin FROM chains WHERE id = OLD.chain_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_member_ins AFTER INSERT ON chain_member BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_member', json_array(NEW.chain_id, NEW.binding), 'insert',
        (SELECT origin FROM chains WHERE id = NEW.chain_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_member_upd AFTER UPDATE ON chain_member BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_member', json_array(NEW.chain_id, NEW.binding), 'update',
        (SELECT origin FROM chains WHERE id = NEW.chain_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_member_del AFTER DELETE ON chain_member BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_member', json_array(OLD.chain_id, OLD.binding), 'delete',
        (SELECT origin FROM chains WHERE id = OLD.chain_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_check_ins AFTER INSERT ON chain_check BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_check', json_array(NEW.chain_id, NEW.run), 'insert',
        (SELECT origin FROM chains WHERE id = NEW.chain_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_check_upd AFTER UPDATE ON chain_check BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_check', json_array(NEW.chain_id, NEW.run), 'update',
        (SELECT origin FROM chains WHERE id = NEW.chain_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_chain_check_del AFTER DELETE ON chain_check BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('chain_check', json_array(OLD.chain_id, OLD.run), 'delete',
        (SELECT origin FROM chains WHERE id = OLD.chain_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_round_ins AFTER INSERT ON round BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', json_array(NEW.id), 'insert',
        (SELECT origin FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_upd AFTER UPDATE ON round BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', json_array(NEW.id), 'update',
        (SELECT origin FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_round_del AFTER DELETE ON round BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', json_array(OLD.id), 'delete',
        (SELECT origin FROM binding WHERE id = OLD.binding_id));
END;

CREATE TRIGGER IF NOT EXISTS sync_outbox_event_ins AFTER INSERT ON event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('event', json_array(NEW.id), 'insert',
        (SELECT origin FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_event_upd AFTER UPDATE ON event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('event', json_array(NEW.id), 'update',
        (SELECT origin FROM binding WHERE id = NEW.binding_id));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_event_del AFTER DELETE ON event BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('event', json_array(OLD.id), 'delete',
        (SELECT origin FROM binding WHERE id = OLD.binding_id));
END;

-- Two hops to an owner: an artifact belongs to a round, and the round belongs
-- to a binding.
CREATE TRIGGER IF NOT EXISTS sync_outbox_artifact_ins AFTER INSERT ON artifact BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('artifact', json_array(NEW.id), 'insert',
        (SELECT origin FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.round_id)));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_artifact_upd AFTER UPDATE ON artifact BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('artifact', json_array(NEW.id), 'update',
        (SELECT origin FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.round_id)));
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_artifact_del AFTER DELETE ON artifact BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('artifact', json_array(OLD.id), 'delete',
        (SELECT origin FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = OLD.round_id)));
END;

-- A transcript has no parent column: owner_kind names the table owner_id is an
-- id in, and each owner resolves to an installation differently. An owner kind
-- this build does not write resolves to NULL rather than to a guess.
CREATE TRIGGER IF NOT EXISTS sync_outbox_transcript_ins AFTER INSERT ON transcript BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('transcript', json_array(NEW.id), 'insert',
        CASE NEW.owner_kind
            WHEN 'round' THEN (SELECT origin FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.owner_id))
            WHEN 'mastermind' THEN (SELECT origin FROM mastermind WHERE id = NEW.owner_id)
            ELSE NULL
        END);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_transcript_upd AFTER UPDATE ON transcript BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('transcript', json_array(NEW.id), 'update',
        CASE NEW.owner_kind
            WHEN 'round' THEN (SELECT origin FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = NEW.owner_id))
            WHEN 'mastermind' THEN (SELECT origin FROM mastermind WHERE id = NEW.owner_id)
            ELSE NULL
        END);
END;
CREATE TRIGGER IF NOT EXISTS sync_outbox_transcript_del AFTER DELETE ON transcript BEGIN
    INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('transcript', json_array(OLD.id), 'delete',
        CASE OLD.owner_kind
            WHEN 'round' THEN (SELECT origin FROM binding WHERE id = (SELECT binding_id FROM round WHERE id = OLD.owner_id))
            WHEN 'mastermind' THEN (SELECT origin FROM mastermind WHERE id = OLD.owner_id)
            ELSE NULL
        END);
END;