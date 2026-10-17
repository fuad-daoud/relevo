package db

import (
	"errors"
	"testing"
)

func TestCreatorColumn(t *testing.T) {
	if got := CreatorColumn("binding"); got != "origin" {
		t.Errorf("CreatorColumn(binding) = %q, want origin", got)
	}
	if got := CreatorColumn("binding_record"); got != "origin" {
		t.Errorf("CreatorColumn(binding_record) = %q, want origin", got)
	}
	if got := CreatorColumn("round"); got != "" {
		t.Errorf("CreatorColumn(round) = %q, want empty", got)
	}
	if got := CreatorColumn("repo"); got != "" {
		t.Errorf("CreatorColumn(repo) = %q, want empty", got)
	}
}

type rootTestCase struct {
	tbl  string
	pk   string
	want string
}

func rootTestCases() []rootTestCase {
	return []rootTestCase{
		{"repo", `["repo1"]`, "repo1"},
		{"mastermind", `["mm1"]`, "mm1"},
		{"chains", `["ch1"]`, "ch1"},
		{"binding", `["b1"]`, "b1"},
		{"round", `["rd1"]`, "b1"},
		{"event", `["e1"]`, "b1"},
		{"binding_record", `["rec1"]`, "rec1"},
		{"round_file", `["rec1","report.md"]`, "rec1"},
		{"transcript", `["tr1"]`, "b1"},
		{"transcript", `["tr2"]`, "mm1"},
	}
}

func seedRootTestDB(t *testing.T) *DB {
	t.Helper()
	d := openTestDB(t)
	err := d.Tx(func(tx *Tx) error {
		stmts := []string{
			`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen)
			 VALUES ('repo1', 'm1', 'https://git.example/repo', '/r', 't')`,
			`INSERT INTO mastermind (id, origin, harness_kind, session_id, first_seen, last_seen)
			 VALUES ('mm1', 'm1', 'harness', 'sess', 't', 't')`,
			`INSERT INTO chains (id, origin, name, status, phase, step, plan, plans, plan_paths, builder, created_at, updated_at)
			 VALUES ('ch1', 'm1', 'ch', 'open', 'p', 's', 1, 1, '[]', 'b', 't', 't')`,
			`INSERT INTO binding (id, origin, writer, name, repo_id, cwd, created_at, builder_mode, ingest_source)
			 VALUES ('b1', 'm1', 'm1', 'b1', 'repo1', '/w', 't', 'headless', '')`,
			`INSERT INTO round (id, binding_id, number, started_at, outcome, switches)
			 VALUES ('rd1', 'b1', 1, 't', 'green', 0)`,
			`INSERT INTO event (id, binding_id, round_id, seq, ts, kind, direction, confirmed, late, entry_json)
			 VALUES ('e1', 'b1', 'rd1', 1, 't', 'k', 'in', 1, 0, '{}')`,
			`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin, writer)
			 VALUES ('rec1', 'o', 'r', 'open', 1, '/w', '{}', 't', 't', 'm1', 'm1')`,
			`INSERT INTO round_file (record_id, name, round, body, body_codec, bytes, sha256, mtime, sealed_at)
			 VALUES ('rec1', 'report.md', 1, X'00', 1, 1, 'sha', 't', 't')`,
			`INSERT INTO transcript (id, owner_kind, owner_id, seq, record_json, rendered)
			 VALUES ('tr1', 'round', 'rd1', 1, '{}', '')`,
			`INSERT INTO transcript (id, owner_kind, owner_id, seq, record_json, rendered)
			 VALUES ('tr2', 'mastermind', 'mm1', 1, '{}', '')`,
			`INSERT INTO transcript (id, owner_kind, owner_id, seq, record_json, rendered)
			 VALUES ('tr-orphan', 'round', 'rd-missing', 1, '{}', '')`,
		}
		for _, stmt := range stmts {
			if _, err := tx.exec(stmt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	return d
}

func TestResolveRootDB(t *testing.T) {
	d := seedRootTestDB(t)
	for _, c := range rootTestCases() {
		root, found, err := d.ResolveRoot(c.tbl, c.pk)
		if err != nil || !found || root != c.want {
			t.Errorf("DB.ResolveRoot(%s, %s) = (%q, %t, %v), want (%q, true, nil)",
				c.tbl, c.pk, root, found, err, c.want)
		}
	}
}

func TestResolveRootTx(t *testing.T) {
	d := seedRootTestDB(t)
	err := d.Tx(func(tx *Tx) error {
		for _, c := range rootTestCases() {
			root, found, err := tx.ResolveRoot(c.tbl, c.pk)
			if err != nil || !found || root != c.want {
				t.Errorf("Tx.ResolveRoot(%s, %s) = (%q, %t, %v), want (%q, true, nil)",
					c.tbl, c.pk, root, found, err, c.want)
			}
			owner, ofound, oerr := tx.ResolveOwner(c.tbl, c.pk)
			if oerr != nil || !ofound || owner != "m1" {
				t.Errorf("Tx.ResolveOwner(%s, %s) = (%q, %t, %v), want (m1, true, nil)",
					c.tbl, c.pk, owner, ofound, oerr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx checks: %v", err)
	}
}

func TestResolveRootErrors(t *testing.T) {
	d := seedRootTestDB(t)

	if root, found, err := d.ResolveRoot("round", `["nonexistent"]`); err != nil || found || root != "" {
		t.Errorf("ResolveRoot absent = (%q, %t, %v), want empty", root, found, err)
	}
	if root, found, err := d.ResolveRoot("transcript", `["tr-orphan"]`); err != nil || found || root != "" {
		t.Errorf("ResolveRoot orphan = (%q, %t, %v), want empty", root, found, err)
	}
	if _, _, err := d.ResolveRoot("not_a_shared_table", `["x"]`); !errors.Is(err, ErrInvalid) {
		t.Errorf("ResolveRoot unknown table err = %v, want ErrInvalid", err)
	}
	if _, _, err := d.ResolveRoot("kv", `["k"]`); !errors.Is(err, ErrInvalid) {
		t.Errorf("ResolveRoot kv err = %v, want ErrInvalid", err)
	}
	if _, _, err := d.ResolveRoot("round", `["a", "b"]`); err == nil {
		t.Errorf("ResolveRoot invalid pk expected error, got nil")
	}
}
