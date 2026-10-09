package relevo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedTwoConsultFindings is the fixture the per-consult claim tests share: a
// live binding whose round 1 carries three queued findings entries -- consults
// abc and def each wrote a findings file, consult ghi wrote none -- the shape a
// binding reaches once all of its consults are done.
func seedTwoConsultFindings(t *testing.T, rt Runtime) {
	t.Helper()
	if err := rt.Store.Save(store.Binding{
		Name:         "webshop",
		CWD:          "/repo/webshop",
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: "opencode", SessionID: "sess"},
		MasterMindID: testClaimMasterMind,
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No Path: consult ghi wrote no findings file, so no read can print one.
	// It queues first, ahead of the entries that do point at a file, so a claim
	// that ignores the path takes this one on any read.
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindFindings,
		Payload: "Consult ghi (critic) wrote no findings: process exited (code 1) with no final message.",
	}); err != nil {
		t.Fatalf("AppendLog silent consult: %v", err)
	}
	for _, c := range []struct{ id, text string }{
		{"abc", "# abc findings\n"},
		{"def", "# def findings\n"},
	} {
		path := rt.Store.FindingsPath("webshop", 1, c.id)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir findings dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(c.text), 0o644); err != nil {
			t.Fatalf("write findings %s: %v", c.id, err)
		}
		if err := rt.Store.AppendLog("webshop", store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindFindings,
			Payload: "Findings from critic consult " + c.id + ": relevo show webshop --findings " + c.id,
			Path:    path,
		}); err != nil {
			t.Fatalf("AppendLog findings %s: %v", c.id, err)
		}
	}
}

// confirmedFindingsPaths returns the paths of the confirmed findings entries of
// name, in log order.
func confirmedFindingsPaths(t *testing.T, rt Runtime, name string) []string {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var paths []string
	for _, e := range entries {
		if e.Kind == store.KindFindings && e.Confirmed {
			paths = append(paths, e.Path)
		}
	}
	return paths
}

// TestShowFindingsClaimsOnlyTheConsultItPrinted pins the per-consult claim: with
// two consults' findings entries pending at once, `show --findings def` prints
// def's file, so it confirms def's entry alone. A match on the entry kind would
// take abc's entry instead -- an entry nobody read, lost to the wait and push
// routes -- and leave def's, which the next wait delivers a second time.
func TestShowFindingsClaimsOnlyTheConsultItPrinted(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedTwoConsultFindings(t, rt)

	res, err := Show(context.Background(), rt, ShowOptions{
		Name: "webshop", Section: ShowFindings, FindingsID: "def",
	})
	if err != nil {
		t.Fatalf("Show --findings def: %v", err)
	}
	if res.Missing || res.Text != "# def findings\n" {
		t.Fatalf("res = %+v, want def's findings text", res)
	}

	want := rt.Store.FindingsPath("webshop", 1, "def")
	if paths := confirmedFindingsPaths(t, rt, "webshop"); len(paths) != 1 || paths[0] != want {
		t.Errorf("confirmed findings entries = %v, want %q alone", paths, want)
	}
	// abc's entry is untouched, so the wait route still has it to deliver.
	if _, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "wait"); err != nil || !found {
		t.Errorf("Pull after show --findings def = found %v, err %v; want the older entry still pending", found, err)
	}
}

// TestShowFindingsDoesNotClaimASilentConsultsEntry pins the read that has no
// file behind it: a consult that wrote no findings queues an entry with no
// path, and no read can print a file for it. Reading another consult's findings
// must leave that entry pending, which the wait route still has to deliver.
func TestShowFindingsDoesNotClaimASilentConsultsEntry(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedTwoConsultFindings(t, rt)

	if _, err := Show(context.Background(), rt, ShowOptions{
		Name: "webshop", Section: ShowFindings, FindingsID: "abc",
	}); err != nil {
		t.Fatalf("Show --findings abc: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	silent := 0
	for _, e := range entries {
		if e.Kind == store.KindFindings && e.Path == "" {
			silent++
			if e.Confirmed {
				t.Error("a read of another consult's findings confirmed the entry with no file")
			}
		}
	}
	if silent != 1 {
		t.Fatalf("silent findings entries = %d, want 1", silent)
	}
}
