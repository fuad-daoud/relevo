package serve

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func TestDaemonPointerKVRoundTrip(t *testing.T) {
	d := testServeDB(t)
	startedAt := time.Date(2026, 9, 20, 12, 34, 56, 0, time.UTC)
	want := DaemonPointer{
		Root:      "/srv/data/relevo-serve/serve",
		PID:       4242,
		Listen:    ":7777",
		StartedAt: startedAt,
	}
	if err := WriteDaemonPointer(d, want); err != nil {
		t.Fatalf("WriteDaemonPointer: %v", err)
	}
	got, ok, err := ReadDaemonPointer(d)
	if err != nil {
		t.Fatalf("ReadDaemonPointer: %v", err)
	}
	if !ok {
		t.Fatal("ReadDaemonPointer ok = false, want true")
	}
	if got != want {
		t.Errorf("ReadDaemonPointer = %+v, want %+v", got, want)
	}
	if err := RemoveDaemonPointer(d); err != nil {
		t.Fatalf("RemoveDaemonPointer: %v", err)
	}
	_, ok, err = ReadDaemonPointer(d)
	if err != nil {
		t.Fatalf("ReadDaemonPointer after remove: %v", err)
	}
	if ok {
		t.Error("ReadDaemonPointer after remove ok = true, want false")
	}
}

func emptyRoot(t *testing.T, _ *db.DB) string { return t.TempDir() }

func missingRoot(t *testing.T, _ *db.DB) string { return filepath.Join(t.TempDir(), "nope") }

func rootWithClientsRow(t *testing.T, d *db.DB) string {
	t.Helper()
	if err := d.KVPut(clientsKVKey, []byte("[]")); err != nil {
		t.Fatal(err)
	}
	return t.TempDir()
}

func rootWithTLSKey(t *testing.T, d *db.DB) string {
	t.Helper()
	if err := d.Tx(func(tx *db.Tx) error {
		return tx.SecretPut(tlsKeySecret, []byte("key"), time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	return t.TempDir()
}

func rootWithBindingsDir(t *testing.T, _ *db.DB) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bindings"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func rootWithLegacyClients(t *testing.T, _ *db.DB) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "clients.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestInitialised is a table because each row only differs in which marker it
// seeds; every row must answer exactly that marker's presence.
func TestInitialised(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, d *db.DB) string
		want  bool
	}{
		{"empty database and root", emptyRoot, false},
		{"clients kv row", rootWithClientsRow, true},
		{"tls key secret", rootWithTLSKey, true},
		{"bindings directory", rootWithBindingsDir, true},
		{"a clients.json file alone is not serve state", rootWithLegacyClients, false},
		{"missing directory", missingRoot, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testServeDB(t)
			root := tc.setup(t, d)
			got, err := Initialised(root, d)
			if err != nil {
				t.Fatalf("Initialised: %v", err)
			}
			if got != tc.want {
				t.Errorf("Initialised = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveAdminRoot(t *testing.T) {
	cases := []struct {
		name          string
		explicitState string
		pointer       *DaemonPointer
		alive         bool
		wantRoot      string
		wantNote      string
	}{
		{"explicit state", "/explicit", nil, false, "/explicit/serve", ""},
		{"missing pointer", "", nil, false, "default", ""},
		{"live different pointer", "", &DaemonPointer{Root: "/daemon/serve", PID: 4242}, true, "/daemon/serve", "using the running daemon's state: /daemon/serve (pid 4242)"},
		{"live default pointer", "", &DaemonPointer{Root: "default", PID: 4242}, true, "default", ""},
		{"stale pointer", "", &DaemonPointer{Root: "/daemon/serve", PID: 4242}, false, "default", "stale daemon pointer: /daemon/serve (pid 4242 is not running)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testServeDB(t)
			defaultRoot := filepath.Join(t.TempDir(), "default")
			if tc.pointer != nil {
				p := *tc.pointer
				if p.Root == "default" {
					p.Root = defaultRoot
				}
				if err := WriteDaemonPointer(d, p); err != nil {
					t.Fatalf("WriteDaemonPointer: %v", err)
				}
			}
			wantRoot := tc.wantRoot
			if wantRoot == "default" {
				wantRoot = defaultRoot
			}
			root, note, err := ResolveAdminRoot(tc.explicitState, d, defaultRoot, func(int) bool { return tc.alive })
			if err != nil {
				t.Fatalf("ResolveAdminRoot: %v", err)
			}
			if root != wantRoot || note != tc.wantNote {
				t.Errorf("ResolveAdminRoot = (%q, %q), want (%q, %q)", root, note, wantRoot, tc.wantNote)
			}
		})
	}
}
