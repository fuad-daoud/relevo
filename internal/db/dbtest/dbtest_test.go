package dbtest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestInstallSetsAndClears pins the contract a TestMain depends on: a fresh
// Open is a copy of the template, and cleanup removes the template directory.
func TestInstallSetsAndClears(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	cleanup, err := Install()
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Cleanup(cleanup)

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Install left %d entries in the temp dir, want 1", len(entries))
	}
	dir := filepath.Join(tmp, entries[0].Name())
	tplBytes, err := os.ReadFile(filepath.Join(dir, "template.db"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	fresh := filepath.Join(tmp, "fresh.db")
	d, err := db.Open(fresh)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close fresh: %v", err)
	}
	freshBytes, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatalf("read fresh: %v", err)
	}
	if !bytes.Equal(freshBytes, tplBytes) {
		t.Errorf("a fresh Open is not a byte copy of the template")
	}

	cleanup()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("template directory still present after cleanup: %v", err)
	}
}

// TestRawOpenUsesTheSelectedEngine pins the raw-driver seam: RawOpen hands back
// a working pool on the caller's path, without migrating it.
func TestRawOpenUsesTheSelectedEngine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.db")
	pool := RawOpen(t, path)
	if _, err := pool.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pool.Exec(`INSERT INTO t (n) VALUES (1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

// TestOwnerModeRoutesOpenThroughASocket pins the on mode: with the variable set
// OwnerMode installs the hop, and the first Open of a path is reached through
// an owner listening on a short /tmp socket.
func TestOwnerModeRoutesOpenThroughASocket(t *testing.T) {
	t.Setenv(ownerEnv, "1")
	reg, cleanup, err := ownerMode()
	if err != nil {
		t.Fatalf("ownerMode: %v", err)
	}
	t.Cleanup(cleanup)
	if reg == nil {
		t.Fatal("ownerMode returned no registry with the variable set")
	}

	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	sock, ok := reg.socks[path]
	if !ok {
		t.Fatalf("OwnerMode did not route %s through a socket", path)
	}
	if !strings.HasPrefix(sock, "/tmp/") {
		t.Errorf("owner socket %q is not directly under /tmp", sock)
	}
	if len(sock) >= 104 {
		t.Errorf("owner socket path is %d bytes, over the 104-byte sun_path", len(sock))
	}
}

// TestInstallOwnerRoutesWithoutTheEnvGate pins the ungated entry point the e2e
// round calls: with the variable unset InstallOwner still installs the hop, and
// the first Open of a path is reached through an owner on a short /tmp socket.
func TestInstallOwnerRoutesWithoutTheEnvGate(t *testing.T) {
	t.Setenv(ownerEnv, "")
	cleanup, err := InstallOwner()
	if err != nil {
		t.Fatalf("InstallOwner: %v", err)
	}
	t.Cleanup(cleanup)

	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if got := d.Route(); !strings.HasPrefix(got, "owner /tmp/") {
		t.Errorf("route = %q, want an owner socket directly under /tmp", got)
	}
}

// TestOwnerModeOffOpensDirectly pins the off mode: an unset variable installs
// nothing, and Open creates the file itself.
func TestOwnerModeOffOpensDirectly(t *testing.T) {
	t.Setenv(ownerEnv, "")
	reg, cleanup, err := ownerMode()
	if err != nil {
		t.Fatalf("ownerMode: %v", err)
	}
	t.Cleanup(cleanup)
	if reg != nil {
		t.Fatal("ownerMode installed a registry with the variable unset")
	}

	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := os.Stat(path); err != nil {
		t.Errorf("a direct Open did not create %s: %v", path, err)
	}
}
