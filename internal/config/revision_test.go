package config

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

func TestPutRecordsRevision(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":2}`)); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if _, err := s.As("cli", "config set policy.max_switches").Put(Policy, []byte(`{"max_switches":3}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Log = %d rows, want 2", len(rows))
	}

	r := rows[0]
	if r.Rev != 2 || r.Version != 2 {
		t.Errorf("revision rev/version = %d/%d, want 2/2", r.Rev, r.Version)
	}
	if r.Source != "cli" || r.Message != "config set policy.max_switches" {
		t.Errorf("revision = %q / %q, want cli / config set policy.max_switches", r.Source, r.Message)
	}
	want := []Change{{Path: "policy.max_switches", Op: "change", Before: json.RawMessage(`2`), After: json.RawMessage(`3`)}}
	if got := revChanges(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %#v, want %#v", got, want)
	}

	full, err := s.Revision(2)
	if err != nil {
		t.Fatalf("Revision(2): %v", err)
	}
	wantSnap, err := EncodeDoc(Doc{Policy: []byte(`{"max_switches":3}`)})
	if err != nil {
		t.Fatalf("EncodeDoc: %v", err)
	}
	if string(full.Snapshot) != string(wantSnap) {
		t.Errorf("snapshot = %q, want the export %q", full.Snapshot, wantSnap)
	}
}

func TestIdenticalPutRecordsNothing(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	for i := 0; i < 2; i++ {
		if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":2}`)); err != nil {
			t.Fatalf("Put %d: %v", i+1, err)
		}
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("Log = %d rows after an identical second put, want 1", len(rows))
	}
	v, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != 2 {
		t.Errorf("version = %d, want 2: the counter still goes up", v)
	}
}

func TestFirstRevisionAddsBaseline(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	// Seed the section with raw Txs: no Store, so no revision. Two writes put
	// the config version at 2.
	if err := s.db.Tx(func(t *db.Tx) error {
		if err := t.ConfigPut(string(Policy), []byte(`{"max_switches":0}`), revAt); err != nil {
			return err
		}
		return t.ConfigPut(string(Policy), []byte(`{"max_switches":1}`), revAt)
	}); err != nil {
		t.Fatalf("seed ConfigPut: %v", err)
	}

	if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":2}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Log = %d rows, want a baseline and one change", len(rows))
	}
	if rows[0].Source != "cli" {
		t.Errorf("newest revision source = %q, want cli", rows[0].Source)
	}
	if rows[0].Version != 3 {
		t.Errorf("new revision version = %d, want 3 (the version after the write)", rows[0].Version)
	}
	baseline := rows[1]
	if baseline.Source != "baseline" || baseline.Message != "config before revisions" {
		t.Errorf("baseline = %q / %q", baseline.Source, baseline.Message)
	}
	if baseline.Version != 2 {
		t.Errorf("baseline version = %d, want 2 (the version before the write)", baseline.Version)
	}
	if string(baseline.Changes) != "[]" {
		t.Errorf("baseline changes = %q, want []", baseline.Changes)
	}

	base, err := s.Revision(baseline.Rev)
	if err != nil {
		t.Fatalf("Revision(baseline): %v", err)
	}
	want, err := EncodeDoc(Doc{Policy: []byte(`{"max_switches":1}`)})
	if err != nil {
		t.Fatalf("EncodeDoc: %v", err)
	}
	if string(base.Snapshot) != string(want) {
		t.Errorf("baseline snapshot = %q, want the seeded doc %q", base.Snapshot, want)
	}
}

func TestNoBaselineOnEmptyConfig(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 || rows[0].Source != "cli" {
		t.Errorf("Log = %#v, want one cli revision and no baseline", rows)
	}
}

func TestPutDocOneRevision(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	doc := Doc{
		Candidates: json.RawMessage(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`),
		Policy:     json.RawMessage(`{"order":{"builder":["claude/p/m"]}}`),
		Hooks:      json.RawMessage(`{"state_changed":[["/bin/true"]]}`),
	}
	if _, err := s.As("cli", "config edit").PutDoc(doc); err != nil {
		t.Fatalf("PutDoc: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Log = %d rows, want 1", len(rows))
	}
	cs := revChanges(t, rows[0])
	if len(cs) != 3 {
		t.Fatalf("changes = %#v, want three section adds", cs)
	}
	for _, c := range cs {
		if c.Op != "add" {
			t.Errorf("change %+v: op = %q, want add", c, c.Op)
		}
	}
}

func TestSecretRevisionHasNoValue(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))
	value := []byte("super-secret-typesafe-value")

	if err := s.As("cli", "config secret set typesafe").PutSecret(SecretTypesafe, value); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Log = %d rows, want 1", len(rows))
	}
	cs := revChanges(t, rows[0])
	want := []Change{{Path: "secret.typesafe", Op: "set"}}
	if !reflect.DeepEqual(cs, want) {
		t.Errorf("changes = %#v, want %#v", cs, want)
	}
	if bytes.Contains(rows[0].Changes, value) {
		t.Error("the changes JSON contains the secret bytes")
	}
	full, err := s.Revision(rows[0].Rev)
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if bytes.Contains(full.Snapshot, value) {
		t.Error("the snapshot contains the secret bytes")
	}
}

func TestRevisionInsertFailureRollsBackWrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := Open(d).WithClock(atTime(revAt))

	// Seed a section so the write is a real change, not a no-op.
	if err := s.db.Tx(func(t *db.Tx) error {
		return t.ConfigPut(string(Policy), []byte(`{"max_switches":1}`), revAt)
	}); err != nil {
		t.Fatalf("seed ConfigPut: %v", err)
	}

	// Drop the revision table through a separate connection, so the write's
	// insert fails and the whole transaction must roll back.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`DROP TABLE config_revision`); err != nil {
		_ = raw.Close()
		t.Fatalf("drop config_revision: %v", err)
	}
	_ = raw.Close()

	if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":2}`)); err == nil {
		t.Fatal("Put with no config_revision table: want an error, got nil")
	}

	body, ok, err := s.Body(Policy)
	if err != nil || !ok {
		t.Fatalf("Body after the failed write = (_, %v, %v), want present", ok, err)
	}
	if string(body) != `{"max_switches":1}` {
		t.Errorf("body = %q, want the pre-write body: the write must roll back", body)
	}
}

func TestRollbackRestoresAndDeletes(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	bodyA := []byte(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`)
	bodyP := []byte(`{"order":{"builder":["claude/p/m"]}}`)
	bodyS := []byte(`{"zen":{"url":"https://zen:7777","insecure":true}}`)

	for _, put := range []struct {
		sec  Section
		body []byte
	}{
		{Candidates, bodyA},
		{Policy, bodyP},
		{Servers, bodyS},
	} {
		if _, err := s.As("cli", "config set "+string(put.sec)).Put(put.sec, put.body); err != nil {
			t.Fatalf("Put %s: %v", put.sec, err)
		}
	}

	plan, err := s.RollbackPlan(2)
	if err != nil {
		t.Fatalf("RollbackPlan(2): %v", err)
	}
	if len(plan) != 1 || plan[0].Path != "servers" || plan[0].Op != "remove" {
		t.Errorf("RollbackPlan(2) = %#v, want one servers remove", plan)
	}

	row, err := s.Rollback(2)
	if err != nil {
		t.Fatalf("Rollback(2): %v", err)
	}
	if row.Rev != 4 {
		t.Errorf("new rev = %d, want 4", row.Rev)
	}
	if row.Source != "rollback" || row.Message != "rollback to #2" {
		t.Errorf("new revision = %q / %q, want rollback / rollback to #2", row.Source, row.Message)
	}

	wantCandidates, _, _ := fillCandidateNames(bodyA)
	if body, ok, err := s.Body(Candidates); err != nil || !ok || string(body) != string(wantCandidates) {
		t.Errorf("candidates after rollback = %q (ok %v, err %v), want revision 2's body", body, ok, err)
	}
	if body, ok, err := s.Body(Policy); err != nil || !ok || string(body) != string(bodyP) {
		t.Errorf("policy after rollback = %q (ok %v, err %v), want revision 2's body", body, ok, err)
	}
	if _, ok, err := s.Body(Servers); err != nil || ok {
		t.Errorf("servers after rollback = (ok %v, err %v), want deleted", ok, err)
	}

	rows, err := s.Log(1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 || rows[0].Source != "rollback" {
		t.Errorf("Log newest = %#v, want the rollback revision", rows)
	}
}

func TestRollbackNoChange(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))
	if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := s.Rollback(1); !errors.Is(err, ErrNoChange) {
		t.Fatalf("Rollback(1) = %v, want ErrNoChange", err)
	}
	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("Log = %d rows after a no-change rollback, want 1", len(rows))
	}
	if v, err := s.Version(); err != nil || v != 1 {
		t.Errorf("version = (%d, %v), want (1, nil): nothing written", v, err)
	}
}

func TestRollbackUnknown(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	if _, err := s.Rollback(99); !errors.Is(err, ErrNoRevision) {
		t.Errorf("Rollback(99) = %v, want ErrNoRevision", err)
	}
	if _, err := s.RollbackPlan(99); !errors.Is(err, ErrNoRevision) {
		t.Errorf("RollbackPlan(99) = %v, want ErrNoRevision", err)
	}
}

func TestImportFilesRecordsImport(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))
	dir := filepath.Join(t.TempDir(), "relevo")
	seedConfigDir(t, dir)

	if _, err := s.ImportFiles(dir, revAt); err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Log = %d rows, want 1", len(rows))
	}
	if rows[0].Source != "import" {
		t.Errorf("source = %q, want import", rows[0].Source)
	}

	got := map[string]string{}
	for _, c := range revChanges(t, rows[0]) {
		if strings.HasPrefix(c.Path, "secret.") {
			got[c.Path] = c.Op
		}
	}
	if got["secret."+SecretClientKey] != "set" || got["secret."+SecretTypesafe] != "set" {
		t.Errorf("secret changes = %v, want both secrets set", got)
	}
}

func TestRevisionDoc(t *testing.T) {
	t.Parallel()

	s := openStore(t).WithClock(atTime(revAt))

	if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":2}`)); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if _, err := s.As("cli", "config set policy.max_switches").Put(Policy, []byte(`{"max_switches":3}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc, err := s.RevisionDoc(1)
	if err != nil {
		t.Fatalf("RevisionDoc(1): %v", err)
	}
	if got := string(compactCopy(doc[Policy])); got != `{"max_switches":2}` {
		t.Errorf("RevisionDoc(1)[policy] = %q, want the body revision 1 wrote", got)
	}

	if _, err := s.RevisionDoc(99); !errors.Is(err, ErrNoRevision) {
		t.Errorf("RevisionDoc(99) = %v, want ErrNoRevision", err)
	}

	cur, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := string(compactCopy(cur[Policy])); got != `{"max_switches":3}` {
		t.Errorf("Current()[policy] = %q, want the newest stored body", got)
	}
}
