package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// The themes section's test palettes: cockpit as the seed writes it, and a
// variant with different colours that is still a whole valid palette. Keys
// sort, so both bodies are keyed blueprints-first and then cockpit, which is
// the byte shape a section body rebuilt from its items always has.
const themesVariant = `{"bg":"#101318","ink":"#e7e9ed","muted":"#9188a3","faint":"#5a6070","line":"#3b4151","panel":"#1b1f29","accent":"#6fb9fe","good":"#5fd39f","warn":"#f2c84b","bad":"#ff6c81","comment":"#b5dcf2"}`
const themesCockpit = `{"bg":"#0f1115","ink":"#e6e8ec","muted":"#9097a3","faint":"#596070","line":"#3a4150","panel":"#1a1f28","accent":"#6ea8fe","good":"#5fd08f","warn":"#f2b84b","bad":"#ff6b81","comment":"#b48cf2"}`
const themesBlueprint = `{"bg":"#0b1220","ink":"#dbe4f0","muted":"#8296b3","faint":"#4c5f7d","line":"#2c3c58","panel":"#111c2f","accent":"#7dd3fc","good":"#86efac","warn":"#fcd34d","bad":"#fca5a5","comment":"#c4b5fd"}`
const themesBase = `{"blueprint":` + themesBlueprint + `,"cockpit":` + themesCockpit + `}`
const themesChanged = `{"blueprint":` + themesVariant + `,"cockpit":` + themesVariant + `}`

func openStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return Open(d)
}

// openRootedStore is one store bound to a temporary config root, which is the
// shape every machine has: the outside-root argv move runs at the binding and
// records its marker, and the exporter refuses until it has. A store with no
// root can neither measure an argv row nor be exported, so a test that exports
// holds a root rather than a weakened gate.
func openRootedStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	return openStore(t).WithFiles(newMemApplier(root), root)
}

// openStoreWith opens a store over a database whose busy waits are the given
// options, for a test that must not wait out the defaults.
func openStoreWith(t *testing.T, o db.Options) *Store {
	t.Helper()
	d, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), o)
	if err != nil {
		t.Fatalf("db.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return Open(d)
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedConfigDir writes one of every file an import consumes, plus a client.pub
// and a hooks directory.
func seedConfigDir(t *testing.T, dir string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "policy.json"),
		`{"order":{"builder":["claude/anthropic/sonnet"]}}`, 0o644)
	writeFile(t, filepath.Join(dir, "roles.json"), `{}`, 0o644)
	writeFile(t, filepath.Join(dir, "prices.json"),
		`{"as_of":"2026-01-01","source":"file","models":{}}`, 0o644)
	writeFile(t, filepath.Join(dir, "servers.json"),
		`{"zen":{"url":"https://zen:7777","fingerprint":"sha256:abcd"}}`, 0o644)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote.Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("remote.MarshalPrivate: %v", err)
	}
	writeFile(t, filepath.Join(dir, "client.key"), string(pem), 0o600)
	writeFile(t, filepath.Join(dir, "client.pub"), "ed25519 fake\n", 0o644)
	writeFile(t, filepath.Join(dir, "typesafe.key"), "  test-key  \n", 0o600)

	writeFile(t, filepath.Join(dir, "hooks", "state_changed.d", "10-first"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(dir, "hooks", "state_changed.d", "05-zero"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(dir, "hooks", "state_changed.d", "20-data"), "not executable\n", 0o644)
	writeFile(t, filepath.Join(dir, "hooks", "round_started.d", "only"), "#!/bin/sh\n", 0o755)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func rm(s string) json.RawMessage { return json.RawMessage(s) }

// seedSections stores each section's raw body in Sections order, so a migration
// test starts from a config with revisions behind it.
func seedSections(t *testing.T, s *Store, sections map[Section]string) {
	t.Helper()
	for _, sec := range Sections {
		body, ok := sections[sec]
		if !ok {
			continue
		}
		if _, err := s.As("test", "seed "+string(sec)).Put(sec, []byte(body)); err != nil {
			t.Fatalf("Put(%s): %v", sec, err)
		}
	}
}

// seedNamelessCandidates stores body without a Store write, simulating legacy
// data written before names were derived on Put.
func seedNamelessCandidates(t *testing.T, s *Store, body string) {
	t.Helper()
	if err := s.db.Tx(func(t *db.Tx) error {
		return t.ConfigPut(string(Candidates), []byte(body), s.now().UTC())
	}); err != nil {
		t.Fatalf("ConfigPut: %v", err)
	}
}

// assertEquivalent pins round 2's equivalence rule: for every role name and
// every kind, the migrated registry gives the same Spec, the same Ranked
// tokens in order, and the same RoleTier as the one before it. Ranked positions
// are not compared: a legacy unlisted entry becomes an ordinary position.
func assertEquivalent(t *testing.T, before, after *roles.Registry) {
	t.Helper()
	if !reflect.DeepEqual(before.Names(), after.Names()) {
		t.Fatalf("names = %v, want %v", after.Names(), before.Names())
	}
	for _, name := range before.Names() {
		for _, h := range harness.All() {
			bs, berr := before.Spec(name, h.Kind)
			as, aerr := after.Spec(name, h.Kind)
			if (berr == nil) != (aerr == nil) {
				t.Errorf("%s/%s: Spec error before = %v, after = %v", name, h.Kind, berr, aerr)
				continue
			}
			if berr == nil && !reflect.DeepEqual(bs, as) {
				t.Errorf("%s/%s: Spec before = %+v, after = %+v", name, h.Kind, bs, as)
			}
		}

		br, _ := before.Role(name)
		ar, _ := after.Role(name)
		if bt, at := rankedTokens(br.Ranked), rankedTokens(ar.Ranked); !reflect.DeepEqual(bt, at) {
			t.Errorf("%s: Ranked before = %v, after = %v", name, bt, at)
		}

		btier, bok := before.RoleTier(name)
		atier, aok := after.RoleTier(name)
		if bok != aok || btier != atier {
			t.Errorf("%s: RoleTier before = %q/%v, after = %q/%v", name, btier, bok, atier, aok)
		}
	}
}

func rankedTokens(ranked []roles.Ranked) []string {
	out := make([]string, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.Token)
	}
	return out
}

// readFixture reads a file under internal/roles/testdata.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "roles", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

var revAt = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func atTime(t time.Time) func() time.Time { return func() time.Time { return t } }

func revChanges(t *testing.T, r db.RevisionRow) []Change {
	t.Helper()
	var cs []Change
	if err := json.Unmarshal(r.Changes, &cs); err != nil {
		t.Fatalf("revision changes %q: %v", r.Changes, err)
	}
	return cs
}
