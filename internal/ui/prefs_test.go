package ui

import (
	"context"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// testPrefsStore is a real t.TempDir() database, so a preference round trip
// exercises the kv row (P3b plan §4.4, §7).
func testPrefsStore(t *testing.T) PrefsStore {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return PrefsStore{KV: d, Key: "ui"}
}

func TestPrefsRoundTrip(t *testing.T) {
	ps := testPrefsStore(t)
	if got := loadPrefs(ps); got != (prefs{}) {
		t.Errorf("missing record must load zero prefs, got %+v", got)
	}
	// sort, dashboard and dashboard_sort survive (R2.10); compact,
	// rail_cols and scope are gone (X1–X3).
	want := prefs{Sort: "name", Dashboard: "harness:agy", DashboardSort: "cost"}
	if msg := savePrefs(ps, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(ps); got != want {
		t.Errorf("round trip: %+v", got)
	}

	// A zero PrefsStore (KV nil) is unscoped: nothing loads.
	if got := loadPrefs(PrefsStore{}); got != (prefs{}) {
		t.Errorf("zero PrefsStore must load zero prefs, got %+v", got)
	}
}

// TestPrefsOldDocumentStillLoads pins §4.6: an old prefs JSON carrying the
// dropped keys (compact, rail_cols, scope) still decodes, because
// encoding/json ignores unknown fields.
func TestPrefsOldDocumentStillLoads(t *testing.T) {
	ps := testPrefsStore(t)
	old := []byte(`{"sort":"name","compact":true,"rail_cols":42,"scope":"all","dashboard":"x","dashboard_sort":"cost"}`)
	if err := ps.KV.KVPut(ps.Key, old); err != nil {
		t.Fatal(err)
	}
	got := loadPrefs(ps)
	want := prefs{Sort: "name", Dashboard: "x", DashboardSort: "cost"}
	if got != want {
		t.Errorf("old document: %+v, want %+v", got, want)
	}
}

func TestApplyPrefs(t *testing.T) {
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{}}, Options{})
	m = m.applyPrefs(prefs{Sort: "name"})
	fv, ok := m.stack[0].(fleetView)
	if !ok {
		t.Fatal("the root view must be the fleet")
	}
	if fv.attention {
		t.Error("sort name must turn attention off")
	}
	m = m.applyPrefs(prefs{})
	fv = m.stack[0].(fleetView)
	if !fv.attention {
		t.Error("zero prefs restore attention")
	}
}

func TestPrefsDashboardRoundTrip(t *testing.T) {
	ps := testPrefsStore(t)
	want := prefs{Sort: "attention", Dashboard: "harness:agy since:30d", DashboardSort: "cost"}
	if msg := savePrefs(ps, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(ps); got != want {
		t.Errorf("round trip: %+v, want %+v", got, want)
	}

	m := newModel(context.Background(), mastermindSource{relevo.Runtime{}}, Options{})
	m = m.applyPrefs(want)
	if m.prefs.Dashboard != "harness:agy since:30d" {
		t.Errorf("applyPrefs: dashboard = %q", m.prefs.Dashboard)
	}
	if m.prefs.DashboardSort != "cost" {
		t.Errorf("applyPrefs: dashboard_sort = %q", m.prefs.DashboardSort)
	}
}

// TestChangesSaveWhenAStoreIsSet pins the shell's save path: the `a` sort
// key returns a save command when a store is set, and the prefMsg path
// saves through the same store.
func TestChangesSaveWhenAStoreIsSet(t *testing.T) {
	ps := testPrefsStore(t)
	m := splitModel(t, 140, 40, threeRows()...)
	m.opts.Prefs = ps
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = drain(t, m, cmd)
	if got := loadPrefs(ps).Sort; got != "name" {
		t.Errorf("a must persist the sort pref, got %q", got)
	}

	// No store: the change applies for the run but nothing is saved (and
	// nothing panics).
	m.opts.Prefs = PrefsStore{}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = drain(t, m, cmd)
	if !fleet(m).attention {
		t.Error("no store: the sort must still have toggled back to attention")
	}
}
