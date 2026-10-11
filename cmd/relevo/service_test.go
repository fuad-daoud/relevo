package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/serviceunit"
)

// fakeRunner records the init-system calls instead of making them.
type fakeRunner struct {
	calls     []string
	unloadErr error
}

func (f *fakeRunner) rec(s string) error  { f.calls = append(f.calls, s); return nil }
func (f *fakeRunner) DaemonReload() error { return f.rec("daemon-reload") }
func (f *fakeRunner) Enable() error       { return f.rec("enable") }
func (f *fakeRunner) Restart() error      { return f.rec("restart") }
func (f *fakeRunner) Load(p string) error { return f.rec("load " + p) }
func (f *fakeRunner) Unload(p string) error {
	_ = f.rec("unload " + p)
	return f.unloadErr
}

const (
	oldUnit    = "old shipped unit\n"
	editedUnit = "edited by hand\n"
)

// isOld stands in for the shipped index: only oldUnit is a past release's.
func isOld(doc, sha string) bool {
	return sha == serviceunit.SHA256Hex([]byte(oldUnit))
}

func linuxSpec(t *testing.T) unitSpec {
	t.Helper()
	root := t.TempDir()
	return newUnitSpec("linux", filepath.Join(root, "config"), root, filepath.Join(root, "bin", "relevo"))
}

func putUnit(t *testing.T, u unitSpec, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(u.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestServiceUnitRefreshLeavesAnAbsentUnitAlone(t *testing.T) {
	u := linuxSpec(t)
	r := &fakeRunner{}
	act, err := refreshServiceUnit(u, r, isOld)
	if err != nil || act != serviceunit.ActionAbsent {
		t.Fatalf("refresh = %q, %v; want absent", act, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(filepath.Dir(filepath.Dir(u.path))))
	if len(entries) != 0 {
		t.Errorf("an absent unit created %d entries under the config root", len(entries))
	}
	if len(r.calls) != 0 {
		t.Errorf("init system touched: %v", r.calls)
	}
}

func TestServiceUnitRefreshRewritesAStaleUnitAndReloads(t *testing.T) {
	u := linuxSpec(t)
	putUnit(t, u, oldUnit)
	r := &fakeRunner{}
	act, err := refreshServiceUnit(u, r, isOld)
	if err != nil || act != serviceunit.ActionStale {
		t.Fatalf("refresh = %q, %v; want stale", act, err)
	}
	got, _ := os.ReadFile(u.path)
	if !bytes.Equal(got, serviceunit.ShippedService()) {
		t.Error("stale unit was not rewritten to the shipped bytes")
	}
	if strings.Join(r.calls, ",") != "daemon-reload" {
		t.Errorf("calls = %v, want only daemon-reload (never a restart)", r.calls)
	}
}

func TestServiceUnitRefreshKeepsAnEditedUnit(t *testing.T) {
	u := linuxSpec(t)
	putUnit(t, u, editedUnit)
	r := &fakeRunner{}
	act, err := refreshServiceUnit(u, r, isOld)
	if err != nil || act != serviceunit.ActionEdited {
		t.Fatalf("refresh = %q, %v; want edited", act, err)
	}
	if got, _ := os.ReadFile(u.path); string(got) != editedUnit {
		t.Errorf("edited unit was changed: %q", got)
	}
	if len(r.calls) != 0 {
		t.Errorf("init system touched: %v", r.calls)
	}
}

func TestServiceUnitRefreshLeavesACurrentUnitAlone(t *testing.T) {
	u := linuxSpec(t)
	putUnit(t, u, string(serviceunit.ShippedService()))
	r := &fakeRunner{}
	act, err := refreshServiceUnit(u, r, isOld)
	if err != nil || act != serviceunit.ActionCurrent || len(r.calls) != 0 {
		t.Fatalf("refresh = %q, %v, calls %v; want current and no calls", act, err, r.calls)
	}
}

func TestServiceUnitRefreshOnDarwinWritesWithoutReload(t *testing.T) {
	root := t.TempDir()
	u := newUnitSpec("darwin", filepath.Join(root, "config"), root, filepath.Join(root, "relevo"))
	if !strings.HasSuffix(u.path, filepath.Join("Library", "LaunchAgents", "com.github.fuad-daoud.relevo.plist")) {
		t.Fatalf("plist path = %s", u.path)
	}
	putUnit(t, u, strings.ReplaceAll(oldUnit, "old", root))
	before := func(doc, sha string) bool { return doc == serviceunit.PlistName }
	r := &fakeRunner{}
	act, err := refreshServiceUnit(u, r, before)
	if err != nil || act != serviceunit.ActionStale {
		t.Fatalf("refresh = %q, %v; want stale", act, err)
	}
	got, _ := os.ReadFile(u.path)
	if !bytes.Equal(got, u.rendered()) || bytes.Contains(got, []byte("@BIN@")) {
		t.Error("plist was not rendered with this machine's paths")
	}
	if len(r.calls) != 0 {
		t.Errorf("darwin refresh ran %v, want write-only", r.calls)
	}
}

func TestServiceInstallWritesEnablesAndRestarts(t *testing.T) {
	u := linuxSpec(t)
	r := &fakeRunner{}
	doc, err := installServiceUnit(u, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if doc != serviceDocOf("install", u.path, "wrote") {
		t.Errorf("doc = %+v", doc)
	}
	if got := strings.Join(r.calls, ","); got != "daemon-reload,enable,restart" {
		t.Errorf("calls = %s", got)
	}
	if got, _ := os.ReadFile(u.path); !bytes.Equal(got, serviceunit.ShippedService()) {
		t.Error("unit not written")
	}
}

func TestServiceInstallRefusesAnEditedUnitWithoutForce(t *testing.T) {
	u := linuxSpec(t)
	putUnit(t, u, editedUnit)
	r := &fakeRunner{}
	_, err := installServiceUnit(u, r, false)
	var ce *cliError
	if !errors.As(err, &ce) || ce.code != codeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if got, _ := os.ReadFile(u.path); string(got) != editedUnit || len(r.calls) != 0 {
		t.Error("edited unit touched without --force")
	}
	if _, err := installServiceUnit(u, r, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(u.path); !bytes.Equal(got, serviceunit.ShippedService()) {
		t.Error("--force did not replace the edited unit")
	}
}

func TestServiceInstallOnDarwinLoadsThePlist(t *testing.T) {
	root := t.TempDir()
	u := newUnitSpec("darwin", filepath.Join(root, "config"), root, filepath.Join(root, "relevo"))
	r := &fakeRunner{}
	if _, err := installServiceUnit(u, r, false); err != nil {
		t.Fatal(err)
	}
	want := "unload " + u.path + ",load " + u.path
	if got := strings.Join(r.calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
}

func TestServiceUninstallRemovesTheUnitOnly(t *testing.T) {
	u := linuxSpec(t)
	putUnit(t, u, editedUnit)
	bin := filepath.Join(filepath.Dir(u.path), "relevo-binary")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	doc, err := uninstallServiceUnit(u, r)
	if err != nil || doc.Outcome != "removed" {
		t.Fatalf("doc = %+v, err = %v", doc, err)
	}
	if _, err := os.Stat(u.path); !os.IsNotExist(err) {
		t.Error("unit still present")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Error("uninstall removed something other than the unit")
	}
	if got := strings.Join(r.calls, ","); got != "unload ,daemon-reload" {
		t.Errorf("calls = %s", got)
	}
	again, err := uninstallServiceUnit(u, r)
	if err != nil || again.Outcome != "absent" {
		t.Errorf("second uninstall = %+v, %v; want absent", again, err)
	}
}
