package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func TestImportAllFiles(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	seedConfigDir(t, dir)
	now := time.Now().UTC()

	res, err := s.ImportFiles(dir, now)
	if err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}
	if !contains(res.Imported, "hooks") {
		t.Errorf("Imported = %v, want it to name hooks", res.Imported)
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if L.Candidates.Len() != 1 || L.Candidates.Refs()[0] != "claude/anthropic/sonnet" {
		t.Errorf("Candidates = %v, want one claude candidate", L.Candidates.Refs())
	}
	if got := L.Policy.OrderFor("builder"); !reflect.DeepEqual(got, []string{"claude/anthropic/sonnet"}) {
		t.Errorf("order.builder = %v", got)
	}
	if L.RolesFile == nil {
		t.Error("RolesFile = nil, want the stored roles section")
	}
	if L.Prices.Source != "file" || L.Prices.AsOf != "2026-01-01" {
		t.Errorf("Prices = %+v, want the file's source and as_of", L.Prices)
	}
	if _, ok := L.Servers["zen"]; !ok {
		t.Errorf("Servers = %v, want zen", L.Servers)
	}
	if L.ClientKey == nil {
		t.Error("ClientKey = nil, want the stored key")
	}
	if L.Typesafe != "test-key" {
		t.Errorf("Typesafe = %q, want test-key", L.Typesafe)
	}

	wantHooks := HooksMap{
		"state_changed": {
			{filepath.Join(dir, "hooks", "state_changed.d", "05-zero")},
			{filepath.Join(dir, "hooks", "state_changed.d", "10-first")},
		},
		"round_started": {
			{filepath.Join(dir, "hooks", "round_started.d", "only")},
		},
	}
	if !reflect.DeepEqual(L.Hooks, wantHooks) {
		t.Errorf("Hooks = %#v, want %#v", L.Hooks, wantHooks)
	}

	for _, name := range []string{
		"candidates.json", "policy.json", "roles.json", "prices.json",
		"servers.json", "client.key", "client.pub", "typesafe.key",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still present after import (err=%v)", name, err)
		}
	}

	// The hooks scripts are never removed, so the config dir stays.
	if _, err := os.Stat(filepath.Join(dir, "hooks", "state_changed.d", "10-first")); err != nil {
		t.Errorf("hook script removed: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("config dir removed while hooks/ is inside it: %v", err)
	}
}

func TestImportInvalidPolicyWritesNothing(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "policy.json"), `{"max_switches":-1}`, 0o644)

	_, err := s.ImportFiles(dir, time.Now().UTC())
	if err == nil {
		t.Fatal("ImportFiles with an invalid policy.json: want error, got nil")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "policy.json")) {
		t.Errorf("error %v does not name the offending path", err)
	}

	for _, name := range []string{"candidates.json", "policy.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed or missing after a refused import: %v", name, err)
		}
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if L.Candidates.Len() != 0 {
		t.Errorf("Candidates = %v, want nothing stored", L.Candidates.Refs())
	}
	if v, err := s.Version(); err != nil || v != 0 {
		t.Errorf("Version = (%d, %v), want (0, nil): nothing was written", v, err)
	}
}

func TestImportEmptyDirIsNoOp(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := s.ImportFiles(dir, time.Now().UTC())
	if err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}
	if len(res.Imported) != 0 || len(res.Removed) != 0 {
		t.Errorf("ImportResult = %+v, want empty", res)
	}
	if v, err := s.Version(); err != nil || v != 0 {
		t.Errorf("Version = (%d, %v), want (0, nil)", v, err)
	}
}

func TestImportLeavesBakFilesAlone(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "candidates.json.bak-20260101"), `[]`, 0o644)

	if _, err := s.ImportFiles(dir, time.Now().UTC()); err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "candidates.json.bak-20260101")); err != nil {
		t.Errorf(".bak file removed or missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "candidates.json")); !os.IsNotExist(err) {
		t.Errorf("candidates.json still present (err=%v)", err)
	}
}

func TestLoadFilesEqualsLoadAfterImport(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	seedConfigDir(t, dir)

	fromFiles, err := LoadFiles(dir)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if _, err := s.ImportFiles(dir, time.Now().UTC()); err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}
	fromDB, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !reflect.DeepEqual(fromFiles.Candidates.Refs(), fromDB.Candidates.Refs()) {
		t.Errorf("Candidates differ: %v vs %v", fromFiles.Candidates.Refs(), fromDB.Candidates.Refs())
	}
	if !reflect.DeepEqual(fromFiles.Policy, fromDB.Policy) {
		t.Errorf("Policy differs: %+v vs %+v", fromFiles.Policy, fromDB.Policy)
	}
	if !reflect.DeepEqual(fromFiles.RolesFile, fromDB.RolesFile) {
		t.Errorf("RolesFile differs: %+v vs %+v", fromFiles.RolesFile, fromDB.RolesFile)
	}
	if !reflect.DeepEqual(fromFiles.Prices, fromDB.Prices) {
		t.Errorf("Prices differ: %+v vs %+v", fromFiles.Prices, fromDB.Prices)
	}
	if !reflect.DeepEqual(fromFiles.Servers, fromDB.Servers) {
		t.Errorf("Servers differ: %+v vs %+v", fromFiles.Servers, fromDB.Servers)
	}
	if !reflect.DeepEqual(fromFiles.Hooks, fromDB.Hooks) {
		t.Errorf("Hooks differ: %#v vs %#v", fromFiles.Hooks, fromDB.Hooks)
	}
	if !reflect.DeepEqual(fromFiles.ClientKey, fromDB.ClientKey) {
		t.Error("ClientKey differs")
	}
	if fromFiles.Typesafe != fromDB.Typesafe {
		t.Errorf("Typesafe differs: %q vs %q", fromFiles.Typesafe, fromDB.Typesafe)
	}
	if !reflect.DeepEqual(fromFiles.Warnings, fromDB.Warnings) {
		t.Errorf("Warnings differ: %v vs %v", fromFiles.Warnings, fromDB.Warnings)
	}
}

// TestImportTxFailureKeepsFiles holds a write transaction on the same database
// while ImportFiles runs, so its own BEGIN IMMEDIATE cannot take the write
// lock; a failed transaction must leave every file in place.
func TestImportTxFailureKeepsFiles(t *testing.T) {
	t.Parallel()

	s := openStoreWith(t, db.Options{BusyTimeout: 20 * time.Millisecond, BeginRetry: time.Millisecond})
	dir := filepath.Join(t.TempDir(), "relevo")
	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "policy.json"),
		`{"order":{"builder":["claude/anthropic/sonnet"]}}`, 0o644)

	err := s.db.Tx(func(*db.Tx) error {
		_, ierr := s.ImportFiles(dir, time.Now().UTC())
		return ierr
	})
	if err == nil {
		t.Fatal("ImportFiles beside a held write transaction: want error, got nil")
	}
	if !errors.Is(err, db.ErrBusy) {
		t.Fatalf("ImportFiles error = %v, want it to wrap db.ErrBusy", err)
	}

	for _, name := range []string{"candidates.json", "policy.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed after a failed commit: %v", name, err)
		}
	}
}

func TestStoreDeleteRemovesAndBumpsVersion(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if _, err := s.Put(Candidates, []byte(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	before, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}

	if err := s.Delete(Candidates); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, err := s.Has(Candidates)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if ok {
		t.Error("candidates still present after Delete")
	}
	after, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if after <= before {
		t.Errorf("Version after Delete = %d, want > %d", after, before)
	}

	// Deleting an absent section still bumps: the version is a change counter.
	if err := s.Delete(Candidates); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	again, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if again <= after {
		t.Errorf("Version after second Delete = %d, want > %d", again, after)
	}
}

type putDocCase struct {
	name        string
	seed        map[Section]string
	doc         Doc
	wantErr     bool
	errContains string
	present     []Section
	absent      []Section
	keep        map[Section]string
	wantVersion int64
}

func TestPutDoc(t *testing.T) {
	t.Parallel()

	cases := []putDocCase{
		{
			name: "writes every named section",
			doc: Doc{
				Candidates: json.RawMessage(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`),
				Policy:     json.RawMessage(`{"order":{"builder":["claude/p/m"]}}`),
				Hooks:      json.RawMessage(`{"state_changed":[["/bin/true"]]}`),
			},
			present:     []Section{Candidates, Policy, Hooks},
			absent:      []Section{Roles},
			wantVersion: 3,
		},
		{
			name: "leaves unmentioned sections alone",
			seed: map[Section]string{
				Servers: `{"zen":{"url":"https://zen:7777","insecure":true}}`,
			},
			doc: Doc{
				Candidates: json.RawMessage(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`),
			},
			present:     []Section{Candidates},
			absent:      []Section{Roles},
			keep:        map[Section]string{Servers: "zen"},
			wantVersion: 2,
		},
		{
			name: "unknown section writes nothing",
			doc: Doc{
				Candidates:          json.RawMessage(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`),
				Section("nonesuch"): json.RawMessage(`{}`),
			},
			wantErr:     true,
			errContains: "nonesuch",
			absent:      []Section{Candidates},
			wantVersion: 0,
		},
		{
			name: "invalid section writes nothing",
			doc: Doc{
				Candidates: json.RawMessage(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`),
				Policy:     json.RawMessage(`{"max_switches":-1}`),
			},
			wantErr:     true,
			absent:      []Section{Candidates, Policy},
			wantVersion: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := openStore(t)
			seedPutDoc(t, s, tc.seed)
			checkPutDoc(t, s, tc)
		})
	}
}

func seedPutDoc(t *testing.T, s *Store, seed map[Section]string) {
	t.Helper()
	for sec, body := range seed {
		if _, err := s.Put(sec, []byte(body)); err != nil {
			t.Fatalf("seed Put(%s): %v", sec, err)
		}
	}
}

func checkPutDoc(t *testing.T, s *Store, tc putDocCase) {
	t.Helper()
	_, err := s.PutDoc(tc.doc)
	if tc.wantErr {
		if err == nil {
			t.Fatal("PutDoc: want error, got nil")
		}
		if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
			t.Errorf("error %v does not name %q", err, tc.errContains)
		}
	} else if err != nil {
		t.Fatalf("PutDoc: %v", err)
	}

	for _, sec := range tc.present {
		if ok, err := s.Has(sec); err != nil || !ok {
			t.Errorf("Has(%s) = %v/%v, want present", sec, ok, err)
		}
	}
	for _, sec := range tc.absent {
		if ok, err := s.Has(sec); err != nil || ok {
			t.Errorf("Has(%s) = %v/%v, want absent", sec, ok, err)
		}
	}
	for sec, want := range tc.keep {
		body, ok, err := s.Body(sec)
		if err != nil || !ok {
			t.Fatalf("Body(%s) = ok %v err %v, want present", sec, ok, err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("Body(%s) = %s, want %q left in place", sec, body, want)
		}
	}
	if v, err := s.Version(); err != nil || v != tc.wantVersion {
		t.Errorf("Version = (%d, %v), want (%d, nil)", v, err, tc.wantVersion)
	}
}

func TestSecretDeleteAndNames(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if err := s.PutSecret(SecretTypesafe, []byte("ts-key")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	names, err := s.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames: %v", err)
	}
	if !reflect.DeepEqual(names, []string{SecretTypesafe}) {
		t.Errorf("SecretNames = %v, want [%s]", names, SecretTypesafe)
	}

	if err := s.SecretDelete(SecretTypesafe); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}
	if _, ok, err := s.Secret(SecretTypesafe); err != nil || ok {
		t.Errorf("secret after delete = ok %v, err %v, want absent", ok, err)
	}
}
