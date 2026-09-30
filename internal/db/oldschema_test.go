package db

import "testing"

// TestReadsOnAnOlderSchemaAreAbsent pins that a read-only open of a database
// migrated only past an older schema reports the newer surface as absent
// rather than erroring, one row per surface a migration added.
func TestReadsOnAnOlderSchemaAreAbsent(t *testing.T) {
	schemaFive := []string{
		"001_initial.sql",
		"002_config.sql",
		"003_binding_record.sql",
		"004_round_file.sql",
		"005_owner_scope.sql",
	}
	cases := []struct {
		name       string
		migrations []string
		probe      func(t *testing.T, d *DB)
	}{
		{"config on schema 1", []string{"001_initial.sql"}, func(t *testing.T, d *DB) {
			if _, ok, err := d.ConfigGet("policy"); err != nil || ok {
				t.Fatalf("ConfigGet on schema 1 = (_, %v, %v), want (_, false, nil)", ok, err)
			}
			if v, err := d.ConfigVersion(); err != nil || v != 0 {
				t.Fatalf("ConfigVersion on schema 1 = (%d, %v), want (0, nil)", v, err)
			}
			if _, ok, err := d.SecretGet("client.key"); err != nil || ok {
				t.Fatalf("SecretGet on schema 1 = (_, %v, %v), want (_, false, nil)", ok, err)
			}
			if names, err := d.SecretNames(); err != nil || len(names) != 0 {
				t.Fatalf("SecretNames on schema 1 = (%v, %v), want ([], nil)", names, err)
			}
		}},
		{"kv on schema 1", []string{"001_initial.sql"}, func(t *testing.T, d *DB) {
			if _, ok, err := d.KVGet("ledger"); err != nil || ok {
				t.Fatalf("KVGet on schema 1 = (_, %v, %v), want (_, false, nil)", ok, err)
			}
			if keys, err := d.KVKeys(""); err != nil || len(keys) != 0 {
				t.Fatalf("KVKeys on schema 1 = (%v, %v), want ([], nil)", keys, err)
			}
		}},
		{"revisions on schema 5", schemaFive, func(t *testing.T, d *DB) {
			rows, err := d.Revisions(0)
			if err != nil || len(rows) != 0 {
				t.Fatalf("Revisions on schema 5 = (%v, %v), want (empty, nil)", rows, err)
			}
			if _, ok, err := d.Revision(1); err != nil || ok {
				t.Fatalf("Revision on schema 5 = (_, %v, %v), want (_, false, nil)", ok, err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.probe(t, openSchema(t, c.migrations...))
		})
	}
}

// schemaFifteen is the migration series up to and including the one before the
// chain tables, so a read on it exercises the absent-table path.
var schemaFifteen = []string{
	"001_initial.sql",
	"002_config.sql",
	"003_binding_record.sql",
	"004_round_file.sql",
	"005_owner_scope.sql",
	"006_config_revision.sql",
	"007_round_actor.sql",
	"008_mastermind.sql",
	"009_prompt_artifact_kind.sql",
	"010_repo_consent.sql",
	"011_session_consent.sql",
	"012_binding_ticket.sql",
	"013_column_codec.sql",
	"014_installation_origin.sql",
	"015_binding_link.sql",
}

// TestChainReadsAreAbsentOnAnOlderSchema pins that the chain reads migration
// 016 adds report a schema that predates the chain tables as absent, never an
// error.
func TestChainReadsAreAbsentOnAnOlderSchema(t *testing.T) {
	d := openSchema(t, schemaFifteen...)

	if _, ok, err := d.ChainGet("", "x"); err != nil || ok {
		t.Fatalf("ChainGet on schema 15 = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	if _, ok, err := d.ChainGetByMember("", "x"); err != nil || ok {
		t.Fatalf("ChainGetByMember on schema 15 = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	if rows, err := d.ChainList(""); err != nil || len(rows) != 0 {
		t.Fatalf("ChainList on schema 15 = (%v, %v), want (empty, nil)", rows, err)
	}
	if rows, err := d.ChainEvents("x"); err != nil || len(rows) != 0 {
		t.Fatalf("ChainEvents on schema 15 = (%v, %v), want (empty, nil)", rows, err)
	}
}
