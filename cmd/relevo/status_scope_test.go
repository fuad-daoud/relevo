package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// The status scope contract: `relevo status`, `status --json` and
// `status --line` resolve one scope, and none of them can show a fleet view
// another one denies.

// seedStatusScopeFixture seeds a store whose live fleet spans the MasterMinds
// named by the fixture: one binding each, none DONE, so no DONE rule can
// account for a row a scope keeps or drops. The two records carry no host pid
// and no session is marked, so a status read that resolves identity resolves
// it only through RELEVO_MASTERMIND -- which is what these tests vary. Every
// path lives under the store's own root, no builder endpoint is configured and
// no server is registered, so a read reaches no harness process and no
// network.
func seedStatusScopeFixture(t *testing.T, owners ...string) (ids []string) {
	t.Helper()
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	// A session with no identity: no env var, no harness marker.
	t.Setenv("RELEVO_MASTERMIND", "")
	t.Setenv("RELEVO_PLANNER", "")
	t.Setenv("CLAUDECODE", "")
	t.Setenv("RELEVO_HARNESS", "")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	reg := mastermindRegistryAt(t, stateHome)

	for i, owner := range owners {
		var id string
		switch i {
		case 0:
			id = "pl_aaaaaaaabbbb"
		case 1:
			id = "pl_bbbbbbbbbbbb"
		default:
			t.Fatalf("seedStatusScopeFixture: no id for owner %d", i)
		}
		rec, err := reg.Create(mastermind.Record{
			ID: id, Name: owner, HarnessKind: "claude",
			SessionID: "sess-" + owner,
			CWD:       filepath.Join(root, "mastermind", owner),
		})
		if err != nil {
			t.Fatalf("mastermind Create(%s): %v", owner, err)
		}
		ids = append(ids, rec.ID)

		binding := owner
		if err := s.Save(store.Binding{
			Name: binding, CWD: filepath.Join(root, "work", binding),
			Round: 1, State: store.StateActive, MasterMindID: rec.ID,
		}); err != nil {
			t.Fatalf("Save %s: %v", binding, err)
		}
	}
	return ids
}

// statusReportBindingNames is the row set a `status --json` document carries.
func statusReportBindingNames(t *testing.T, out []byte) []string {
	t.Helper()
	var rep view.Report
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("json.Unmarshal status --json: %v\nstdout was:\n%s", err, out)
	}
	names := make([]string, 0, len(rep.Bindings))
	for _, b := range rep.Bindings {
		names = append(names, b.Name)
	}
	return names
}

// statusLineBindingNames is the row set a `status --line --json` document
// carries.
func statusLineBindingNames(t *testing.T, out []byte) []string {
	t.Helper()
	var doc view.StatusLineDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("json.Unmarshal status --line --json: %v\nstdout was:\n%s", err, out)
	}
	names := make([]string, 0, len(doc.Rows))
	for _, r := range doc.Rows {
		names = append(names, r.Name)
	}
	return names
}

// runStatusFormat runs one status invocation and fails the test if it refuses.
func runStatusFormat(t *testing.T, args ...string) []byte {
	t.Helper()
	stdout, stderr, err := captureOutput(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
	}
	return stdout
}

// TestStatusRefusesAnUnresolvableMasterMindInEveryFormat pins the ambiguous
// case: this session names no MasterMind and two own live bindings here, so
// the three formats refuse the same way. Before the fix the human listing
// showed the whole fleet while `status --line` showed nothing at all -- one
// format naming a scope the other denied.
func TestStatusRefusesAnUnresolvableMasterMindInEveryFormat(t *testing.T) {
	seedStatusScopeFixture(t, "architect-1", "architect-2")

	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		stdout, _, err := captureOutput(t, func() error { return run(args) })
		if err == nil {
			t.Errorf("%v = nil, want the ambiguous-scope refusal\nstdout was:\n%s", args, stdout)
			continue
		}
		requireCLIError(t, err, codeRefused, "")
		if len(stdout) != 0 {
			t.Errorf("%v stdout = %q, want it clean", args, stdout)
		}
	}

	// The statusline never fails a prompt, so it refuses on stderr and names no
	// row -- the same decision as the two formats above, never a fleet view.
	line, stderr, err := captureOutput(t, func() error { return run([]string{"status", "--line"}) })
	if err != nil {
		t.Fatalf("status --line: %v", err)
	}
	for _, name := range []string{"architect-1", "architect-2"} {
		if strings.Contains(string(line), name) {
			t.Errorf("status --line named %q, want no row:\n%s", name, line)
		}
	}
	if !strings.Contains(string(stderr), "MasterMind") {
		t.Errorf("status --line stderr = %q, want it to name the ambiguity", stderr)
	}
}

// TestStatusScopesEveryFormatToTheSessionMasterMind pins the default view: the
// session names one MasterMind, so the human listing, `--json` and `--line`
// each show that Master's binding and no other's.
func TestStatusScopesEveryFormatToTheSessionMasterMind(t *testing.T) {
	ids := seedStatusScopeFixture(t, "architect-1", "architect-2")
	t.Setenv("RELEVO_MASTERMIND", ids[0])

	human := string(runStatusFormat(t, "status"))
	if !strings.Contains(human, "architect-1") {
		t.Errorf("status = %q, want the session MasterMind's row", human)
	}
	if strings.Contains(human, "architect-2") {
		t.Errorf("status = %q, want no other MasterMind's row", human)
	}
	if got := statusReportBindingNames(t, runStatusFormat(t, "status", "--json")); len(got) != 1 || got[0] != "architect-1" {
		t.Errorf("status --json rows = %v, want [architect-1]", got)
	}
	if got := statusLineBindingNames(t, runStatusFormat(t, "status", "--line", "--json")); len(got) != 1 || got[0] != "architect-1" {
		t.Errorf("status --line --json rows = %v, want [architect-1]", got)
	}
	line := string(runStatusFormat(t, "status", "--line"))
	if !strings.Contains(line, "architect-1") || strings.Contains(line, "architect-2") {
		t.Errorf("status --line = %q, want only architect-1's row", line)
	}
}

// TestStatusFooterCountsTheOtherMasterMindsBindings pins the footer: the
// default view hides one binding here, and the human listing says so with the
// exact count and the command that shows it.
func TestStatusFooterCountsTheOtherMasterMindsBindings(t *testing.T) {
	ids := seedStatusScopeFixture(t, "architect-1", "architect-2")
	t.Setenv("RELEVO_MASTERMIND", ids[0])

	human := string(runStatusFormat(t, "status"))
	want := "1 bindings belong to other MasterMinds: relevo status --all"
	if !strings.Contains(human, want) {
		t.Errorf("status = %q, want the footer %q", human, want)
	}

	// The JSON document stays a machine contract: the footer is a human line.
	if doc := string(runStatusFormat(t, "status", "--json")); strings.Contains(doc, "belong to other MasterMinds") {
		t.Errorf("status --json = %q, want no human footer in the document", doc)
	}
}

// TestStatusFooterAbsentWhenNoBindingIsHidden pins the single-MasterMind case:
// there is nothing another MasterMind owns, so the footer is absent rather
// than present with a zero count.
func TestStatusFooterAbsentWhenNoBindingIsHidden(t *testing.T) {
	ids := seedStatusScopeFixture(t, "architect-1")
	t.Setenv("RELEVO_MASTERMIND", ids[0])

	human := string(runStatusFormat(t, "status"))
	if strings.Contains(human, "belong to other MasterMinds") {
		t.Errorf("status = %q, want no footer with one MasterMind in the store", human)
	}
	if !strings.Contains(human, "architect-1") {
		t.Errorf("status = %q, want the one binding's row", human)
	}
}

// TestStatusAllShowsEveryMasterMindInEveryFormat pins the fleet view: --all
// resolves the scope every format uses to the whole store, so no format can
// show a MasterMind's private slice, and the footer has nothing left to say.
func TestStatusAllShowsEveryMasterMindInEveryFormat(t *testing.T) {
	ids := seedStatusScopeFixture(t, "architect-1", "architect-2")
	t.Setenv("RELEVO_MASTERMIND", ids[0])

	human := string(runStatusFormat(t, "status", "--all"))
	for _, name := range []string{"architect-1", "architect-2"} {
		if !strings.Contains(human, name) {
			t.Errorf("status --all = %q, want %q's row", human, name)
		}
	}
	if strings.Contains(human, "belong to other MasterMinds") {
		t.Errorf("status --all = %q, want no footer in the fleet view", human)
	}
	if got := statusReportBindingNames(t, runStatusFormat(t, "status", "--all", "--json")); len(got) != 2 {
		t.Errorf("status --all --json rows = %v, want both bindings", got)
	}
	if got := statusLineBindingNames(t, runStatusFormat(t, "status", "--line", "--all", "--json")); len(got) != 2 {
		t.Errorf("status --line --all --json rows = %v, want both bindings", got)
	}
	line := string(runStatusFormat(t, "status", "--line", "--all"))
	for _, name := range []string{"architect-1", "architect-2"} {
		if !strings.Contains(line, name) {
			t.Errorf("status --line --all = %q, want %q's row", line, name)
		}
	}
}

// TestStatusAllRescuesAnUnresolvableMasterMind pins that --all is the way out
// of a refusal: the fleet view needs no identity, so it answers where the
// scoped view refuses.
func TestStatusAllRescuesAnUnresolvableMasterMind(t *testing.T) {
	seedStatusScopeFixture(t, "architect-1", "architect-2")

	human := string(runStatusFormat(t, "status", "--all"))
	for _, name := range []string{"architect-1", "architect-2"} {
		if !strings.Contains(human, name) {
			t.Errorf("status --all = %q, want %q's row", human, name)
		}
	}
	if got := statusLineBindingNames(t, runStatusFormat(t, "status", "--line", "--all", "--json")); len(got) != 2 {
		t.Errorf("status --line --all --json rows = %v, want both bindings", got)
	}
}

// TestStatusLineAllIsNotAUsageError pins that --all reaches the statusline:
// the flag is the fleet view in every format, so combining it with --line is
// the fleet line rather than a usage refusal.
func TestStatusLineAllIsNotAUsageError(t *testing.T) {
	seedStatusScopeFixture(t, "architect-1")

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"status", "--line", "--all"}) })
	if err != nil {
		t.Fatalf("status --line --all: %v (stderr: %s); the fleet view is a view, not a usage error", err, stderr)
	}
	if !strings.Contains(string(stdout), "architect-1") {
		t.Errorf("status --line --all = %q, want the binding's row", stdout)
	}
}
