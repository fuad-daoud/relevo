package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// twoMastermindScope seeds one store with two registered masterminds: A owns
// a1, a2, a finished binding and a chain of its own; B owns b1 and a chain of
// its own. Nothing here runs a harness and nothing dials out: the whole point is
// that the four status surfaces can be compared against one fixture.
type twoMastermindScope struct {
	root string
	a, b string
}

func seedTwoMastermindScope(t *testing.T) twoMastermindScope {
	t.Helper()
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	reg := mastermindRegistryAt(t, stateHome)
	a, err := reg.Create(mastermind.Record{
		ID: "pl_aaaaaaaaaaaa", Name: "architect-a", HarnessKind: "claude", SessionID: "sess-a",
		CWD: filepath.Join(root, "mastermind-a"),
	})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	b, err := reg.Create(mastermind.Record{
		ID: "pl_bbbbbbbbbbbb", Name: "architect-b", HarnessKind: "claude", SessionID: "sess-b",
		CWD: filepath.Join(root, "mastermind-b"),
	})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	for _, bind := range []store.Binding{
		{Name: "a1", CWD: filepath.Join(root, "work", "a1"), Round: 1, State: store.StateActive, MasterMindID: a.ID},
		{Name: "a2", CWD: filepath.Join(root, "work", "a2"), Round: 1, State: store.StateActive, MasterMindID: a.ID},
		{Name: "a-done", CWD: filepath.Join(root, "work", "a-done"), Round: 1, State: store.StateDone, MasterMindID: a.ID},
		{Name: "b1", CWD: filepath.Join(root, "work", "b1"), Round: 1, State: store.StateActive, MasterMindID: b.ID},
	} {
		if err := s.Save(bind); err != nil {
			t.Fatalf("Save(%s): %v", bind.Name, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, c := range []struct {
		name  string
		id    string
		parts []string
	}{{"cha", a.ID, []string{"ca1", "ca2"}}, {"chb", b.ID, []string{"cb1", "cb2"}}} {
		members := []store.Binding{
			{Name: c.parts[0], CWD: filepath.Join(root, "work", c.parts[0]), Round: 1, State: store.StateActive, MasterMindID: c.id},
			{Name: c.parts[1], CWD: filepath.Join(root, "work", c.parts[1]), Round: 1, State: store.StateActive,
				Role: "reviewer", Shape: store.ShapeReader, MasterMindID: c.id},
		}
		row := db.ChainRow{
			ID: db.NewID(), Name: c.name, Status: "running", Phase: "build", Step: "building",
			Plan: 1, Plans: 2, Builder: c.parts[0], Reviewer: c.parts[1],
			Worktree: filepath.Join(root, "work", c.name), MasterMindID: c.id,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.WithLock(func(tx *store.Tx) error { return tx.CreateChain(row, members) }); err != nil {
			t.Fatalf("CreateChain(%s): %v", c.name, err)
		}
	}
	return twoMastermindScope{root: root, a: a.Name, b: b.Name}
}

// reportNames is the binding names a `status --json` document carries, in order.
func reportNames(t *testing.T, out []byte) []string {
	t.Helper()
	var doc struct {
		Bindings []struct {
			Name string `json:"name"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal status document %q: %v", out, err)
	}
	names := make([]string, 0, len(doc.Bindings))
	for _, b := range doc.Bindings {
		names = append(names, b.Name)
	}
	return names
}

// lineDocNames is the binding names a `status --line --json` document carries.
func lineDocNames(t *testing.T, out []byte) []string {
	t.Helper()
	var doc view.StatusLineDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal statusline document %q: %v", out, err)
	}
	names := make([]string, 0, len(doc.Rows))
	for _, r := range doc.Rows {
		names = append(names, r.Name)
	}
	return names
}

func equalNameSets(got, want []string) bool {
	g, w := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	return strings.Join(g, ",") == strings.Join(w, ",")
}

// TestStatusSurfacesAgreeOnOneScope pins the plan's headline: on one fixture,
// with one resolved identity, the human listing, `status --json`,
// `status --line` and `status --line --json` all name the same rows. Only the
// renderer differs between them.
func TestStatusSurfacesAgreeOnOneScope(t *testing.T) {
	for _, c := range []struct {
		mastermind string
		want       []string
	}{
		{"architect-a", []string{"a1", "a2", "cha"}},
		{"architect-b", []string{"b1", "chb"}},
	} {
		t.Run(c.mastermind, func(t *testing.T) {
			seedTwoMastermindScope(t)
			t.Setenv("RELEVO_MASTERMIND", c.mastermind)

			for _, args := range [][]string{{"status"}, {"status", "--json"}, {"status", "--line"}, {"status", "--line", "--json"}} {
				out, stderr, err := captureOutput(t, func() error { return run(args) })
				if err != nil {
					t.Fatalf("run(%v): %v (stderr: %s)", args, err, stderr)
				}
				names := textRowNames(t, string(out))
				if args[len(args)-1] == "--json" {
					if args[1] == "--line" {
						names = lineDocNames(t, out)
					} else {
						names = reportNames(t, out)
					}
				}
				if !equalNameSets(names, c.want) {
					t.Errorf("run(%v) rows = %v, want %v", args, names, c.want)
				}
			}
		})
	}
}

// textRowNames is the binding names a human `status` or `status --line` prints.
func textRowNames(t *testing.T, out string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "relevo:" {
			continue
		}
		// A statusline row leads with the state word; a status row leads with
		// the binding name. Either way the binding name is a known fixture name.
		for _, f := range fields {
			if f == "a1" || f == "a2" || f == "a-done" || f == "b1" || f == "cha" || f == "chb" {
				names = append(names, f)
				break
			}
		}
	}
	return names
}

// TestStatusExplicitScopeFlagsOverrideResolution pins that the two scope flags
// answer for the session, that they are exclusive, and that --all-masterminds
// is the only way to see every mastermind at once.
func TestStatusExplicitScopeFlagsOverrideResolution(t *testing.T) {
	fx := seedTwoMastermindScope(t)
	// The session resolves to B; --mastermind A must win over it.
	t.Setenv("RELEVO_MASTERMIND", fx.b)

	out, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--mastermind", "architect-a", "--json"})
	})
	if err != nil {
		t.Fatalf("status --mastermind: %v (stderr: %s)", err, stderr)
	}
	if names := reportNames(t, out); !equalNameSets(names, []string{"a1", "a2", "cha"}) {
		t.Errorf("--mastermind A rows = %v, want [a1 a2 cha] over the session's B", names)
	}

	out, stderr, err = captureOutput(t, func() error {
		return run([]string{"status", "--all-masterminds", "--all", "--json"})
	})
	if err != nil {
		t.Fatalf("status --all-masterminds: %v (stderr: %s)", err, stderr)
	}
	if names := reportNames(t, out); !equalNameSets(names, []string{"a1", "a2", "a-done", "b1", "cha", "chb"}) {
		t.Errorf("--all-masterminds --all rows = %v, want every row", names)
	}

	_, _, err = captureOutput(t, func() error {
		return run([]string{"status", "--mastermind", "architect-a", "--all-masterminds"})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Errorf("both scope flags: exit = %v, want 2", err)
	}
}

// TestStatusRefusesAnUnresolvableIdentity pins the refusal every status surface
// gives when no mastermind resolves: a usage code, exit 2, the next step, and
// nothing on stdout. An unresolvable identity is never internal and never a
// silent listing of everything or of nothing.
func TestStatusRefusesAnUnresolvableIdentity(t *testing.T) {
	for _, c := range []struct {
		name string
		env  [2]string
		args []string
	}{
		{name: "nothing detected", args: []string{"status"}},
		{name: "nothing detected json", args: []string{"status", "--json"}},
		{name: "nothing detected line", args: []string{"status", "--line"}},
		{name: "nothing detected line json", args: []string{"status", "--line", "--json"}},
		{name: "nothing detected chains", args: []string{"status", "--chains"}},
		{name: "stale env ref", env: [2]string{"RELEVO_MASTERMIND", "ghost"}, args: []string{"status"}},
		{name: "stale env ref json", env: [2]string{"RELEVO_MASTERMIND", "ghost"}, args: []string{"status", "--json"}},
		{name: "stale env ref line json", env: [2]string{"RELEVO_MASTERMIND", "ghost"}, args: []string{"status", "--line", "--json"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			seedTwoMastermindScope(t)
			if c.env[0] == "" {
				t.Setenv("CLAUDECODE", "")
				t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")
				t.Setenv("RELEVO_HARNESS", "")
			} else {
				t.Setenv(c.env[0], c.env[1])
			}

			stdout, _, err := captureOutput(t, func() error { return run(c.args) })
			if err == nil {
				t.Fatalf("run(%v) = nil, want a refusal", c.args)
			}
			ce := requireCLIError(t, err, codeUsage, "relevo mastermind list")
			var ec exitCodeErr
			if !errors.As(err, &ec) || ec.code != 2 {
				t.Errorf("exit = %v, want 2, not %d", err, catalogExit(codeInternal))
			}
			if ce.code == codeInternal {
				t.Errorf("code = %q: an unresolvable identity is never internal", ce.code)
			}
			if !strings.Contains(ce.message, "--mastermind") {
				t.Errorf("message = %q, want it to name --mastermind", ce.message)
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want nothing before the refusal", stdout)
			}
		})
	}
}

// TestStatusChainsFollowTheSameScope pins case 7: the chain listing takes the
// same scope as the binding listing, and what it names agrees with the named
// view of the same chain.
func TestStatusChainsFollowTheSameScope(t *testing.T) {
	seedTwoMastermindScope(t)
	t.Setenv("RELEVO_MASTERMIND", "architect-a")

	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"status", "--chains", "--json"}, []string{"cha"}},
		{[]string{"status", "--all-masterminds", "--chains", "--json"}, []string{"cha", "chb"}},
	} {
		out, stderr, err := captureOutput(t, func() error { return run(c.args) })
		if err != nil {
			t.Fatalf("run(%v): %v (stderr: %s)", c.args, err, stderr)
		}
		var doc struct {
			Chains []struct {
				Name string `json:"name"`
			} `json:"chains"`
		}
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("unmarshal chains %q: %v", out, err)
		}
		names := make([]string, 0, len(doc.Chains))
		for _, ch := range doc.Chains {
			names = append(names, ch.Name)
		}
		if !equalNameSets(names, c.want) {
			t.Errorf("run(%v) chains = %v, want %v", c.args, names, c.want)
		}
	}

	// A chain the scope left out is still answerable by name: a named view is
	// not a scoped view.
	out, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--name", "chb", "--json"})
	})
	if err != nil {
		t.Fatalf("status --name chb: %v (stderr: %s)", err, stderr)
	}
	if names := reportNames(t, out); !equalNameSets(names, []string{"chb", "cb1", "cb2"}) {
		t.Errorf("status --name chb rows = %v, want [chb cb1 cb2]", names)
	}
}

// TestStatusLineRefusesAllMasterminds pins the one documented exemption on the
// statusline: it names one mastermind, on its first line and in its document,
// so --all-masterminds has no honest rendering there.
func TestStatusLineRefusesAllMasterminds(t *testing.T) {
	seedTwoMastermindScope(t)
	_, _, err := captureOutput(t, func() error {
		return run([]string{"status", "--line", "--all-masterminds"})
	})
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestStatuslineNamesItsMastermind pins that the document's mastermind is
// filled in on the path that succeeds: `mastermind: null` belongs to no
// document this verb can print.
func TestStatuslineNamesItsMastermind(t *testing.T) {
	fx := seedTwoMastermindScope(t)
	t.Setenv("RELEVO_MASTERMIND", fx.a)

	out, stderr, err := captureOutput(t, func() error { return run([]string{"status", "--line", "--json"}) })
	if err != nil {
		t.Fatalf("status --line --json: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(out), `"mastermind":{"id":"pl_aaaaaaaaaaaa","name":"architect-a"}`) {
		t.Errorf("statusline document = %s, want the resolved mastermind", out)
	}
}

// TestStatusAllShowsTheScopedDoneRow pins that the DONE rule the four surfaces
// share is the one that honours --all.
func TestStatusAllShowsTheScopedDoneRow(t *testing.T) {
	fx := seedTwoMastermindScope(t)
	t.Setenv("RELEVO_MASTERMIND", fx.a)

	out, stderr, err := captureOutput(t, func() error { return run([]string{"status", "--all", "--json"}) })
	if err != nil {
		t.Fatalf("status --all --json: %v (stderr: %s)", err, stderr)
	}
	if names := reportNames(t, out); !equalNameSets(names, []string{"a1", "a2", "a-done", "cha"}) {
		t.Errorf("--all rows = %v, want the DONE row of this mastermind", names)
	}

	out, stderr, err = captureOutput(t, func() error { return run([]string{"status", "--name", "a-done", "--json"}) })
	if err != nil {
		t.Fatalf("status --name a-done: %v (stderr: %s)", err, stderr)
	}
	if names := reportNames(t, out); !equalNameSets(names, []string{"a-done"}) {
		t.Errorf("--name a-done rows = %v, want it by name", names)
	}
}
