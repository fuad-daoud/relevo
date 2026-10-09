package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// sandboxStateRoot is the state root TestMain's temp HOME implies, so the
// marker a test writes lands under the package's own isolated root and never
// under the developer's real state.
func sandboxStateRoot(t *testing.T) string {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", root, err)
	}
	return root
}

// writeSandboxMarker writes the marker scripts/relevo-dev-user.sh writes: a
// name and a port, one key per line.
func writeSandboxMarker(t *testing.T, stateRoot, name string, port int) {
	t.Helper()
	body := "name=" + name + "\n"
	if port > 0 {
		body += "port=" + strconv.Itoa(port) + "\n"
	}
	if err := os.WriteFile(filepath.Join(stateRoot, sandboxMarkerName), []byte(body), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(stateRoot, sandboxMarkerName)) })
}

// sandboxTestDB opens a machine database under the test's own temp dir, so the
// serve pointer a test writes never touches the package's real one.
func sandboxTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// findSandboxRow pulls the named global row out of a check slice.
func findSandboxRow(t *testing.T, checks []doctor.Check, name string) (doctor.Check, bool) {
	t.Helper()
	for _, c := range checks {
		if c.Group == "" && c.Name == name {
			return c, true
		}
	}
	return doctor.Check{}, false
}

// A state root with no marker yields the xdg row alone: the linger and port
// rows are sandbox-only, so an ordinary production machine gains no row it
// has to learn about.
func TestSandboxChecksNoMarkerShowsOnlyXDG(t *testing.T) {
	root := sandboxStateRoot(t)
	_ = os.Remove(filepath.Join(root, sandboxMarkerName))

	out := sandboxChecks(&stubDoctorEnv{statErr: os.ErrNotExist}, sandboxTestDB(t), root, nil)

	if _, ok := findSandboxRow(t, out, "xdg"); !ok {
		t.Error("no xdg row, want one for every user")
	}
	for _, absent := range []string{"linger", "port"} {
		if c, ok := findSandboxRow(t, out, absent); ok {
			t.Errorf("row %q present (%s) without a marker, want it absent", absent, c.Detail)
		}
	}
}

// A marker adds the linger row. The stub env reports every Stat as failing, so
// the row lands on its warn branch and names the loginctl line -- the state a
// freshly created sandbox is in until lingering is enabled.
func TestSandboxChecksMarkerAddsLingerRow(t *testing.T) {
	root := sandboxStateRoot(t)
	writeSandboxMarker(t, root, "demo", 7801)

	out := sandboxChecks(&stubDoctorEnv{statErr: os.ErrNotExist}, sandboxTestDB(t), root, nil)

	c, ok := findSandboxRow(t, out, "linger")
	if !ok {
		t.Fatal("no linger row, want one where the marker exists")
	}
	if c.Severity != doctor.SevWarn {
		t.Errorf("linger severity = %v, want warn", c.Severity)
	}
	if c.Fix == "" {
		t.Error("linger fix is empty, want the loginctl enable-linger line")
	}
}

// A marker plus a serve pointer on the default port adds the port row, warning
// that 7777 collides with the production serve on the same host.
func TestSandboxChecksMarkerAndPointerAddsPortRow(t *testing.T) {
	root := sandboxStateRoot(t)
	writeSandboxMarker(t, root, "demo", 7801)

	d := sandboxTestDB(t)
	if err := serve.WriteDaemonPointer(d, serve.DaemonPointer{
		Root:      filepath.Join(root, "serve"),
		PID:       1234,
		Listen:    ":7777",
		StartedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("WriteDaemonPointer: %v", err)
	}

	out := sandboxChecks(&stubDoctorEnv{statErr: os.ErrNotExist}, d, root, nil)

	c, ok := findSandboxRow(t, out, "port")
	if !ok {
		t.Fatal("no port row, want one where the marker and the pointer both exist")
	}
	if c.Severity != doctor.SevWarn {
		t.Errorf("port severity = %v, want warn", c.Severity)
	}
	if c.Fix != "relevo serve --listen :7801" {
		t.Errorf("port fix = %q, want the marker's port", c.Fix)
	}
}

// A marker with no serve pointer has no port row: there is no port to report.
func TestSandboxChecksMarkerWithoutPointerHasNoPortRow(t *testing.T) {
	root := sandboxStateRoot(t)
	writeSandboxMarker(t, root, "demo", 7801)

	out := sandboxChecks(&stubDoctorEnv{statErr: os.ErrNotExist}, sandboxTestDB(t), root, nil)

	if c, ok := findSandboxRow(t, out, "port"); ok {
		t.Errorf("port row present (%s) without a serve pointer, want it absent", c.Detail)
	}
	if _, ok := findSandboxRow(t, out, "linger"); !ok {
		t.Error("no linger row, want one where the marker exists")
	}
}

// A marker naming no sandbox reads as no sandbox at all, so a truncated file
// turns the extra rows off rather than describing a sandbox it cannot name.
func TestSandboxChecksNamelessMarkerIsNoSandbox(t *testing.T) {
	root := sandboxStateRoot(t)
	marker := filepath.Join(root, sandboxMarkerName)
	if err := os.WriteFile(marker, []byte("port=7801\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(marker) })

	out := sandboxChecks(&stubDoctorEnv{statErr: os.ErrNotExist}, sandboxTestDB(t), root, nil)

	if _, ok := findSandboxRow(t, out, "xdg"); !ok {
		t.Error("no xdg row, want one for every user")
	}
	for _, absent := range []string{"linger", "port"} {
		if c, ok := findSandboxRow(t, out, absent); ok {
			t.Errorf("row %q present (%s) for a nameless marker, want it absent", absent, c.Detail)
		}
	}
}

// The rows land among the global rows, before the per-kind blocks, so render
// order is unchanged for everything that was already there.
func TestSandboxChecksKeepsGlobalRenderOrder(t *testing.T) {
	root := sandboxStateRoot(t)
	writeSandboxMarker(t, root, "demo", 7801)

	in := []doctor.Check{
		{Name: "release"},
		{Group: "claude", Name: "binary"},
		{Name: "daemon"},
	}
	out := sandboxChecks(&stubDoctorEnv{statErr: os.ErrNotExist}, sandboxTestDB(t), root, in)

	firstGrouped := -1
	for i, c := range out {
		if c.Group != "" {
			firstGrouped = i
			break
		}
	}
	if firstGrouped < 0 {
		t.Fatalf("no grouped row survived: %+v", out)
	}
	for i, c := range out[:firstGrouped] {
		if c.Group != "" {
			t.Errorf("grouped row %q at index %d, want every sandbox row among the globals", c.Name, i)
		}
	}
	if out[0].Name != "release" || out[firstGrouped-1].Name == "" {
		t.Errorf("globals = %+v, want the existing rows preserved in order", out[:firstGrouped])
	}
}

// sandboxMarker reads what create wrote and refuses everything it cannot name.
func TestSandboxMarkerParsing(t *testing.T) {
	root := t.TempDir()

	if _, ok := sandboxMarker(root); ok {
		t.Error("a root with no marker reads as a sandbox")
	}
	if _, ok := sandboxMarker(""); ok {
		t.Error("an empty state root reads as a sandbox")
	}

	writeSandboxMarker(t, root, "demo", 7801)
	m, ok := sandboxMarker(root)
	if !ok {
		t.Fatal("a written marker did not parse")
	}
	if m.Name != "demo" || m.Port != 7801 {
		t.Errorf("marker = %+v, want name demo port 7801", m)
	}
}
