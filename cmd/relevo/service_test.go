package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
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

// stubLookPath answers lookPath from a map, and restores the real lookup when
// the test ends: the rendered unit's PATH must not depend on what this shell
// happens to have. A binary absent from the map is not found.
func stubLookPath(t *testing.T, paths map[string]string) {
	t.Helper()
	old := lookPath
	lookPath = func(binary string) (string, error) {
		if p, ok := paths[binary]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = old })
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
	stubLookPath(t, nil)
	putUnit(t, u, oldUnit)
	r := &fakeRunner{}
	act, err := refreshServiceUnit(u, r, isOld)
	if err != nil || act != serviceunit.ActionStale {
		t.Fatalf("refresh = %q, %v; want stale", act, err)
	}
	got, _ := os.ReadFile(u.path)
	if !bytes.Equal(got, u.rendered()) {
		t.Error("stale unit was not rewritten to this machine's rendered unit")
	}
	if bytes.Contains(got, []byte("@BIN@")) || bytes.Contains(got, []byte("@PATH@")) {
		t.Error("rewritten unit kept a placeholder")
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
	stubLookPath(t, nil)
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
	if got, _ := os.ReadFile(u.path); !bytes.Equal(got, u.rendered()) {
		t.Error("unit not written to the rendered bytes")
	}
}

func TestServiceInstallRefusesAnEditedUnitWithoutForce(t *testing.T) {
	u := linuxSpec(t)
	stubLookPath(t, nil)
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
	if got, _ := os.ReadFile(u.path); !bytes.Equal(got, u.rendered()) {
		t.Error("--force did not replace the edited unit with the rendered bytes")
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

// TestServicePATHAssemblyOrderAndDedup pins the assembled PATH's shape: the
// fixed list stays first, harness homes follow in harness.All() order, a
// home already covered is not appended, and one home already in the fixed
// list is not appended either.
func TestServicePATHAssemblyOrderAndDedup(t *testing.T) {
	home := t.TempDir()
	fixed := "%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin"
	agyDir := filepath.Join(home, "gemini-home", "bin")
	claudeDir := filepath.Join(home, "npm-global", "bin")
	stubLookPath(t, map[string]string{
		"agy":      filepath.Join(agyDir, "agy"),
		"claude":   filepath.Join(claudeDir, "claude"),
		"codex":    filepath.Join(claudeDir, "codex"),
		"opencode": filepath.Join(home, ".local", "bin", "opencode"),
	})
	want := fixed + ":" + agyDir + ":" + claudeDir
	if got := servicePath(home); got != want {
		t.Errorf("servicePath =\n%s\nwant\n%s", got, want)
	}
}

// TestServiceInstallRendersCurrentBinary pins that an install writes a unit
// carrying this machine's binary and every harness home, and that a re-read
// of the written bytes decides current, so installs and later refreshes
// agree on what the unit is.
func TestServiceInstallRendersCurrentBinary(t *testing.T) {
	home := t.TempDir()
	u := newUnitSpec("linux", filepath.Join(home, "config"), home, filepath.Join(home, ".local", "bin", "relevo"))
	npmDir := filepath.Join(home, "npm-global", "bin")
	bunDir := filepath.Join(home, ".bun", "bin")
	stubLookPath(t, map[string]string{
		"agy":      filepath.Join(home, ".local", "bin", "agy"),
		"claude":   filepath.Join(npmDir, "claude"),
		"opencode": filepath.Join(npmDir, "opencode"),
		"codex":    filepath.Join(bunDir, "codex"),
	})
	r := &fakeRunner{}
	if _, err := installServiceUnit(u, r, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(u.path)
	for _, want := range []string{
		"ExecStart=" + u.bin,
		"Environment=PATH=%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin:",
		npmDir,
		bunDir,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("written unit lacks %q:\n%s", want, got)
		}
	}
	// The installed binary home is inside the fixed list already, so the agy
	// lookup must have added nothing for it: exactly one .local/bin mention,
	// the ExecStart.
	if n := strings.Count(string(got), filepath.Join(home, ".local", "bin")); n != 1 {
		t.Errorf("the binary home appeared %d times, want once", n)
	}
	// claude and opencode share the npm home; dedup keeps it once.
	if n := strings.Count(string(got), npmDir); n != 1 {
		t.Errorf("the npm home appeared %d times, want once", n)
	}
	act, err := u.decide(isOld)
	if err != nil || act != serviceunit.ActionCurrent {
		t.Errorf("re-read of the installed unit = %q, %v; want current", act, err)
	}
}
