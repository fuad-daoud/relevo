package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

var updateWire = flag.Bool("update-wire", false, "update wire golden files")

func wireNormalize(b []byte) []byte {
	cwdRE := regexp.MustCompile(`"cwd":\s*"[^"]*"`)
	b = cwdRE.ReplaceAll(b, []byte(`"cwd": "/tmp/test-state/api"`))
	return b
}

func wireAssertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	filename := name
	if !strings.HasSuffix(filename, ".golden") {
		filename += ".golden"
	}
	path := filepath.Join("testdata", "contract", filename)
	if *updateWire {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s: re-run with 'go test ./cmd/relevo -run ServeContract -update-wire' to generate", path)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch in %s: re-run with 'go test ./cmd/relevo -run ServeContract -update-wire' to update\n--- got ---\n%s\n--- want ---\n%s", path, string(got), string(want))
	}
}

func seedSecondOwner(t *testing.T, root, label string) (*store.Store, remote.ClientID) {
	t.Helper()
	serveRoot := filepath.Join(root, "serve")
	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer d.Close()

	clients, err := serve.LoadClients(d)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cl, err := clients.Add(label, remote.MarshalPublic(kp.Public, label), time.Now())
	if err != nil {
		t.Fatalf("clients.Add: %v", err)
	}
	dir, ok := cl.ID.Dir()
	if !ok {
		t.Fatalf("client id %q has no owner dir", cl.ID)
	}

	owner := store.NewShared(filepath.Join(serveRoot, "bindings", dir), string(cl.ID), d)
	seedOwnerStore(t, owner, string(cl.ID))
	return owner, cl.ID
}

func TestServeContractStatusJSON(t *testing.T) {
	_, root := seedServeOwnerState(t, "alice")
	_, bobID := seedSecondOwner(t, root, "bob")

	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer d.Close()

	clients, err := serve.LoadClients(d)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}

	var aliceID remote.ClientID
	for _, cl := range clients.List() {
		if cl.Label == "alice" {
			aliceID = cl.ID
			break
		}
	}
	if aliceID == "" {
		t.Fatalf("alice not found in clients")
	}

	// `serve status` reads the machine's policy from XDG_CONFIG_HOME; give it
	// one whose builder cap is not the machine's NumCPU default.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	policyDir := filepath.Join(configHome, "relevo")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "policy.json"), []byte(`{"serve":{"max_builders":7}}`), 0o644); err != nil {
		t.Fatalf("write policy.json: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"serve", "status", "--json", "--state", root})
	})
	if err != nil {
		t.Fatalf("run serve status --json: %v (stderr: %s)", err, string(stderr))
	}

	// Normalize dynamic values (temp cwd paths and generated owner ClientIDs)
	normalized := wireNormalize(stdout)
	normalized = bytes.ReplaceAll(normalized, []byte(aliceID), []byte("SHA256:alice000000000000000000000000000000000000000"))
	normalized = bytes.ReplaceAll(normalized, []byte(bobID), []byte("SHA256:bob0000000000000000000000000000000000000000"))

	wireAssertGolden(t, "serve-status", normalized)
}
