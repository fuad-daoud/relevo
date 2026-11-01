package db

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSeedsAFreshPathFromTheTemplate(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "template.db")

	td, err := Open(tpl)
	if err != nil {
		t.Fatalf("Open template: %v", err)
	}
	tplVersion, err := td.Version()
	if err != nil {
		t.Fatalf("template Version: %v", err)
	}
	if err := td.Close(); err != nil {
		t.Fatalf("Close template: %v", err)
	}
	tplBytes, err := os.ReadFile(tpl)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	swapFreshTemplate(t, tpl)

	fresh := filepath.Join(dir, "fresh.db")
	d, err := Open(fresh)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	gotVersion, err := d.Version()
	if err != nil {
		t.Fatalf("fresh Version: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close fresh: %v", err)
	}

	if gotVersion != tplVersion {
		t.Errorf("fresh Version = %d, want the template's %d", gotVersion, tplVersion)
	}

	freshBytes, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatalf("read fresh: %v", err)
	}
	if !bytes.Equal(freshBytes, tplBytes) {
		t.Errorf("a fresh Open is not a byte copy of the template")
	}

	fi, err := os.Stat(fresh)
	if err != nil {
		t.Fatalf("stat fresh: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("fresh mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestOpenLeavesAnExistingFileAlone(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.db")

	// An existing database, built the way production would when no template is
	// set, that the template must not replace.
	d, err := Open(existing)
	if err != nil {
		t.Fatalf("Open existing: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close existing: %v", err)
	}
	before, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read existing: %v", err)
	}

	// A template that differs from the existing file, so replacing it would be
	// visible in the bytes.
	tpl := filepath.Join(dir, "template.db")
	td, err := Open(tpl)
	if err != nil {
		t.Fatalf("Open template: %v", err)
	}
	if _, err := td.sqlDB.Exec(`CREATE TABLE template_marker (x INTEGER)`); err != nil {
		t.Fatalf("create template marker: %v", err)
	}
	if err := td.Close(); err != nil {
		t.Fatalf("Close template: %v", err)
	}
	tplBytes, err := os.ReadFile(tpl)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	if bytes.Equal(before, tplBytes) {
		t.Fatal("fixture is broken: the existing file and the template must differ")
	}

	swapFreshTemplate(t, tpl)

	reopened, err := Open(existing)
	if err != nil {
		t.Fatalf("reopen existing: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close reopened: %v", err)
	}

	after, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("reread existing: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("an existing file was rewritten; its bytes changed")
	}
}

func TestOpenWithoutATemplateMigrates(t *testing.T) {
	swapFreshTemplate(t, "")

	d, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	got, err := d.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if want := embeddedVersion(t); got != want {
		t.Errorf("Version = %d, want the embedded %d", got, want)
	}
}

func TestSeedFailureFallsBackToMigrating(t *testing.T) {
	dir := t.TempDir()
	swapFreshTemplate(t, filepath.Join(dir, "missing.db"))

	d, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	got, err := d.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if want := embeddedVersion(t); got != want {
		t.Errorf("Version = %d, want the embedded %d after the seed failed", got, want)
	}
}

// swapFreshTemplate points Open at path for one test and puts back the template
// that was installed before, so the tests after it keep seeding from the
// package's template instead of migrating every fresh file.
func swapFreshTemplate(t *testing.T, path string) {
	t.Helper()
	prev := freshTemplate
	SetFreshTemplate(path)
	t.Cleanup(func() { SetFreshTemplate(prev) })
}

// TestPackageSeedsFreshFilesFromATemplate pins TestMain's install: without it
// every fresh Open in this package runs every migration again, and the suite
// slows until load pushes it past the test timeout.
func TestPackageSeedsFreshFilesFromATemplate(t *testing.T) {
	if freshTemplate == "" {
		t.Fatal("no template is installed: TestMain must seed fresh files from one")
	}
}
