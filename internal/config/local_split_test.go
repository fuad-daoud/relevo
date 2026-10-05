//go:build unix

package config

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// A machine that has run the split keeps its config and secrets in the local
// file. Every reader has to find them there, whether it reached the database
// directly or through the owner, or a config that is on disk reads back empty
// exactly when the daemon is the one serving it.

// serveSplit opens a split pair in a fresh directory, serves the shared file on
// a socket short enough for sun_path, and returns the direct handle and the
// socket.
func serveSplit(t *testing.T) (*db.DB, string) {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	dir, err := os.MkdirTemp("/tmp", "rvo-cfg-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := db.NewOwner(shared)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = shared.Close()
	})
	return shared, sock
}

// seedEverySection stores every section the registry names, plus both secrets,
// so a reader that finds nothing is looking in the wrong file.
//
// It writes through the store, so it pins the direction a caller writes.
func seedEverySection(t *testing.T, s *Store) {
	t.Helper()
	bodies := localSectionBodies(t)
	for _, sec := range Sections {
		if _, err := s.As("test", "seed "+string(sec)).Put(sec, []byte(bodies[sec])); err != nil {
			t.Fatalf("Put(%s): %v", sec, err)
		}
	}
	if err := s.As("test", "seed typesafe").PutSecret(SecretTypesafe, []byte("test-key")); err != nil {
		t.Fatalf("PutSecret(typesafe): %v", err)
	}
}

// seedLocalFile writes the same config straight into the machine-local file,
// without going through a config store. A reader that finds it there is
// therefore bound to the local file by the routing and not by the writer that
// put it there, which is what makes these tests pin the read direction.
func seedLocalFile(t *testing.T, local *db.DB) {
	t.Helper()
	now := time.Now().UTC()
	if err := local.Tx(func(tx *db.Tx) error {
		for sec, body := range localSectionBodies(t) {
			if err := tx.ConfigPut(string(sec), []byte(body), now); err != nil {
				return err
			}
		}
		return tx.SecretPut(SecretTypesafe, []byte("test-key"), now)
	}); err != nil {
		t.Fatalf("seed the local file: %v", err)
	}
}

// sectionBodies is one valid stored body for every section the registry names.
func localSectionBodies(t *testing.T) map[Section]string {
	t.Helper()
	bodies := map[Section]string{
		Candidates: `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`,
		Policy:     `{"order":{"builder":["claude/anthropic/sonnet"]}}`,
		Roles:      `{}`,
		Prices:     `{"as_of":"2026-01-01","source":"file","models":{}}`,
		Servers:    `{"zen":{"url":"https://zen:7777","fingerprint":"sha256:abcd"}}`,
		Hooks:      `{"state_changed":[["/tmp/hook"]]}`,
		Agents:     `{"my-exec":{"shape":"writer","native":{"claude":{"agent":"my-exec"}}}}`,
		Actors:     `{"builder-1":{"shape":"writer","agent":"my-exec"}}`,
		Accounts:   `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/tmp/claude-work"}]`,
		Sync:       syncStored,
	}
	// The workflows section stores a source and the parse of that source, so it
	// is seeded through the pair its own validator requires rather than a body
	// written by hand.
	const wfSource = "name: ship\nstart: build\nsteps:\n  build: { run: builder }\n"
	bodies[Workflows] = string(wfBody(t, "ship", wfSource, wfRaw(t, wfParse(t, wfSource))))

	for _, sec := range Sections {
		if _, ok := bodies[sec]; !ok {
			t.Fatalf("no body for section %s; every stored section needs one here", sec)
		}
	}
	return bodies
}

// assertFullConfig reads through s and reports every section and secret missing.
func assertFullConfig(t *testing.T, s *Store) {
	t.Helper()
	for _, sec := range Sections {
		body, ok, err := s.Body(sec)
		if err != nil {
			t.Fatalf("Body(%s): %v", sec, err)
		}
		if !ok || len(body) == 0 {
			t.Errorf("section %s is not readable through this handle", sec)
		}
	}
	ts, ok, err := s.Secret(SecretTypesafe)
	if err != nil {
		t.Fatalf("Secret(typesafe): %v", err)
	}
	if !ok || string(ts) != "test-key" {
		t.Errorf("the typesafe secret = %q (present %t), want the seeded value", ts, ok)
	}
}

// TestOpenReadsTheMachineLocalFileAfterTheSplit pins the routing on a split
// pair opened directly: a store built over the shared handle still reads every
// section and both secrets, because the split moved them to the local file.
func TestOpenReadsTheMachineLocalFileAfterTheSplit(t *testing.T) {
	shared, _ := serveSplit(t)
	seedLocalFile(t, shared.Local())
	assertFullConfig(t, Open(shared))
}

// TestOpenReadsTheMachineLocalFileThroughTheOwner pins the same routing when the
// handle reached the database through the owner, which is how a client and the
// cockpit read it. A dialled handle carries its own local file, and the store
// has to bind that one: the shared file behind the same socket is right there
// and answers with an empty config if the binding picks it.
func TestOpenReadsTheMachineLocalFileThroughTheOwner(t *testing.T) {
	shared, sock := serveSplit(t)
	seedLocalFile(t, shared.Local())

	dialled, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	assertFullConfig(t, Open(dialled))
}

// TestTheSplitMovedEveryConfigRowOutOfTheSharedFile pins the other half of the
// routing, so the two tests above cannot both pass by reading the shared file
// anyway: after the store has written every section, the shared file carries
// none of them and the local file carries all of them.
func TestTheSplitMovedEveryConfigRowOutOfTheSharedFile(t *testing.T) {
	shared, _ := serveSplit(t)
	seedEverySection(t, Open(shared))

	for _, sec := range Sections {
		if body, ok, err := shared.ConfigGet(string(sec)); err != nil {
			t.Fatalf("ConfigGet(%s) on the shared handle: %v", sec, err)
		} else if ok {
			t.Errorf("section %s is still readable on the shared file: %q", sec, body)
		}
		body, ok, err := shared.Local().ConfigGet(string(sec))
		if err != nil {
			t.Fatalf("ConfigGet(%s) on the local handle: %v", sec, err)
		}
		if !ok || len(body) == 0 {
			t.Errorf("section %s is not in the local file", sec)
		}
	}
	if _, ok, err := shared.SecretGet(SecretTypesafe); err != nil {
		t.Fatalf("SecretGet on the shared handle: %v", err)
	} else if ok {
		t.Error("the typesafe secret is still readable on the shared file")
	}
}

// TestAHandleWithoutALocalFileKeepsAnswering pins the fallback: a store over a
// handle opened with no local file is that handle, so a database that predates
// the split still reads its own config rather than reporting every section
// absent.
func TestAHandleWithoutALocalFileKeepsAnswering(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	s := Open(d)
	if _, err := s.As("test", "seed").Put(Candidates, []byte(`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`)); err != nil {
		t.Fatalf("Put(candidates): %v", err)
	}
	if _, ok, err := s.Body(Candidates); err != nil || !ok {
		t.Errorf("Body(candidates) = %v, %t, want the stored body on a handle with no local file", err, ok)
	}
}

// TestTheSecretStoreReadsAndWritesLocallyAfterTheSplit pins the secret surface a
// caller with no transaction handle holds: every call reads or writes this
// machine's local file, even though the literal it was built with names the
// shared handle.
func TestTheSecretStoreReadsAndWritesLocallyAfterTheSplit(t *testing.T) {
	shared, _ := serveSplit(t)
	const secret, value = "serve.tls.cert", "local-only"

	dbStore := db.SecretStore{DB: shared}
	if err := dbStore.SecretPut(secret, []byte(value), time.Now().UTC()); err != nil {
		t.Fatalf("SecretPut: %v", err)
	}
	if _, ok, err := shared.SecretGet(secret); err != nil {
		t.Fatalf("SecretGet on the shared handle: %v", err)
	} else if ok {
		t.Error("a secret written through the store is readable on the shared file")
	}
	got, ok, err := dbStore.SecretGet(secret)
	if err != nil || !ok || string(got) != value {
		t.Errorf("read back through the store = %q (present %t, err %v), want %q", got, ok, err, value)
	}
	if names, err := dbStore.SecretNames(); err != nil {
		t.Fatalf("SecretNames: %v", err)
	} else if !contains(names, secret) {
		t.Errorf("SecretNames = %v, want it to name %s", names, secret)
	}
	if err := dbStore.SecretDelete(secret); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}
	if _, ok, err := dbStore.SecretGet(secret); err != nil {
		t.Fatalf("SecretGet after delete: %v", err)
	} else if ok {
		t.Error("a deleted secret is still readable through the store")
	}
}
