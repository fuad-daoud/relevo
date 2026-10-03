package relevo

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// scopeFixture is the two-mastermind store the scope tests read: A owns a1, a2,
// a finished binding and a chain of its own; B owns b1 and a chain of its own.
// One chain per mastermind is what makes the two paths disagree loudly if the
// chain narrowing stops following the row narrowing.
func scopeFixture(t *testing.T) Runtime {
	t.Helper()
	rt := newRuntime(t)
	root := t.TempDir()
	for _, b := range []store.Binding{
		{Name: "a1", CWD: filepath.Join(root, "a1"), Round: 1, State: store.StateActive, MasterMindID: testClaimMasterMind},
		{Name: "a2", CWD: filepath.Join(root, "a2"), Round: 1, State: store.StateActive, MasterMindID: testClaimMasterMind},
		{Name: "a-done", CWD: filepath.Join(root, "a-done"), Round: 1, State: store.StateDone, MasterMindID: testClaimMasterMind},
		{Name: "b1", CWD: filepath.Join(root, "b1"), Round: 1, State: store.StateActive, MasterMindID: otherClaimMasterMind},
	} {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", b.Name, err)
		}
	}
	now := baseTime
	for _, c := range []struct {
		name  string
		id    string
		parts []string
	}{
		{"cha", testClaimMasterMind, []string{"ca1", "ca2"}},
		{"chb", otherClaimMasterMind, []string{"cb1", "cb2"}},
	} {
		members := make([]store.Binding, 0, len(c.parts))
		for i, p := range c.parts {
			b := store.Binding{
				Name: p, CWD: filepath.Join(root, p), Round: 1, State: store.StateActive, MasterMindID: c.id,
			}
			if i == 1 {
				b.Role, b.Shape = "reviewer", store.ShapeReader
			}
			members = append(members, b)
		}
		row := db.ChainRow{
			ID: db.NewID(), Name: c.name, Status: "running", Phase: "build", Step: "building",
			Plan: 1, Plans: 2, Builder: c.parts[0], Reviewer: c.parts[1],
			Worktree:     filepath.Join(root, c.name),
			MasterMindID: c.id, CreatedAt: now, UpdatedAt: now,
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(row, members) }); err != nil {
			t.Fatalf("CreateChain(%s): %v", c.name, err)
		}
	}
	return rt
}

func sortedNames(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

func sameNames(got, want []string) bool {
	return equalStrings(sortedNames(got), sortedNames(want))
}

func statuslineNames(rep view.Report, now time.Time) []string {
	rows := view.StatusLineRows(rep, now)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// TestScopeReportGivesEveryStatusSurfaceTheSameRows is the agreement the whole
// scope rule exists for: on the two-mastermind fixture the human listing, the
// --json document and the --line projection all name the same rows, and the
// statusline's own scoped builder names them too.
func TestScopeReportGivesEveryStatusSurfaceTheSameRows(t *testing.T) {
	rt := scopeFixture(t)
	ctx := context.Background()

	full, err := Status(ctx, rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	sc := Scope{MasterMindID: testClaimMasterMind}

	human := ScopeReport(full, sc)
	line, err := MasterMindStatus(ctx, rt, testClaimMasterMind)
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}

	want := []string{"a1", "a2", "cha"}
	for _, surface := range []struct {
		name string
		got  []string
	}{
		{"human", rowNames(human)},
		{"json", rowNames(human)},
		{"line projection", statuslineNames(human, rt.Now())},
		{"statusline builder", statuslineNames(line, rt.Now())},
	} {
		if !sameNames(surface.got, want) {
			t.Errorf("%s rows = %v, want %v", surface.name, surface.got, want)
		}
	}
	if human.DoneHidden != 1 {
		t.Errorf("DoneHidden = %d, want 1", human.DoneHidden)
	}

	// The other mastermind sees its own rows and nobody else's.
	other := ScopeReport(full, Scope{MasterMindID: otherClaimMasterMind})
	if got := rowNames(other); !sameNames(got, []string{"b1", "chb"}) {
		t.Errorf("B rows = %v, want [b1 chb]", got)
	}
}

// TestScopeReportKeepsDoneOnlyWhenAsked pins the DONE rule the human listing,
// the --json document and the --line projection share.
func TestScopeReportKeepsDoneOnlyWhenAsked(t *testing.T) {
	rt := scopeFixture(t)
	ctx := context.Background()
	full, err := Status(ctx, rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	plain := ScopeReport(full, Scope{MasterMindID: testClaimMasterMind})
	if names := rowNames(plain); contains(names, "a-done") {
		t.Errorf("plain rows = %v, want no DONE row", names)
	}

	all := ScopeReport(full, Scope{MasterMindID: testClaimMasterMind, All: true})
	if names := rowNames(all); !contains(names, "a-done") {
		t.Errorf("--all rows = %v, want the DONE row", names)
	}
	if all.DoneHidden != 0 {
		t.Errorf("--all DoneHidden = %d, want 0", all.DoneHidden)
	}

	// Naming something is a request for that specific thing, DONE or not, and
	// a named view takes no mastermind filter at all.
	named := ScopeReport(full, Scope{MasterMindID: testClaimMasterMind, Named: true})
	if got := rowNames(named); !contains(got, "a-done") {
		t.Errorf("named rows = %v, want the DONE row", got)
	}
	if got := rowNames(ScopeReport(full, Scope{Named: true})); !sameNames(got, rowNames(full)) {
		t.Errorf("named rows = %v, want every row: a name takes no scope filter", got)
	}
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestResolveScopeRefusesAnUnresolvedIdentity pins that no unresolvable
// identity produces a scope at all: each of the four refusals carries the next
// step that settles it, and none of them falls back to every mastermind.
func TestResolveScopeRefusesAnUnresolvedIdentity(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		next string
	}{
		{
			name: "two live sessions",
			err: mastermind.ErrAmbiguousOpencodeSession{
				Dir: "/repo", Titles: []string{"one", "two"},
			},
			next: "relevo status --mastermind <name|id>",
		},
		{"stale ref", mastermind.ErrUnknownMasterMind{Ref: "ghost"}, "relevo mastermind list"},
		{"unregistered session", mastermind.ErrUnregisteredSession{Kind: "opencode", SessionID: "s1"}, "relevo mastermind list"},
		{"nothing detected", mastermind.ErrNoMasterMind, "relevo mastermind list"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sc, rec, err := ResolveScope("", false, false, false,
				func(string) (mastermind.Record, error) { return mastermind.Record{}, c.err })
			if err == nil {
				t.Fatal("ResolveScope: want a refusal, got nil")
			}
			var refusal ScopeRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("ResolveScope returned %T, want a ScopeRefusal", err)
			}
			if refusal.Next != c.next {
				t.Errorf("next = %q, want %q", refusal.Next, c.next)
			}
			if !errors.Is(err, c.err) {
				t.Errorf("refusal does not unwrap to %v", c.err)
			}
			if sc != (Scope{}) || rec != nil {
				t.Errorf("ResolveScope = %+v, %v; want the zero scope on a refusal", sc, rec)
			}
		})
	}
}

// TestResolveScopeReadsTheFlags pins the flag surface: the two scope flags are
// exclusive, --all-masterminds never resolves, a named view needs no identity,
// and any other combination resolves exactly once and lands on that record.
func TestResolveScopeReadsTheFlags(t *testing.T) {
	resolve := func(ref string) (mastermind.Record, error) {
		if ref != "architect-2" {
			t.Fatalf("resolve called with %q, want architect-2", ref)
		}
		return mastermind.Record{ID: "pl_bbb", Name: "architect-2"}, nil
	}
	sc, rec, err := ResolveScope("architect-2", false, false, false, resolve)
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if sc.MasterMindID != "pl_bbb" || rec == nil || rec.Name != "architect-2" {
		t.Errorf("ResolveScope = %+v, %+v, want the resolved record's scope", sc, rec)
	}

	t.Run("both flags are exclusive", func(t *testing.T) {
		_, _, err := ResolveScope("x", true, false, false,
			func(string) (mastermind.Record, error) {
				t.Fatal("resolve must not run")
				return mastermind.Record{}, nil
			})
		var refusal ScopeRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("ResolveScope = %v, want a refusal", err)
		}
	})

	t.Run("all-masterminds resolves nothing", func(t *testing.T) {
		sc, rec, err := ResolveScope("", true, true, false,
			func(string) (mastermind.Record, error) {
				t.Fatal("resolve must not run")
				return mastermind.Record{}, nil
			})
		if err != nil {
			t.Fatalf("ResolveScope: %v", err)
		}
		if sc != (Scope{All: true}) || rec != nil {
			t.Errorf("ResolveScope = %+v, %v, want every mastermind and no record", sc, rec)
		}
	})

	t.Run("a named view needs no identity", func(t *testing.T) {
		sc, rec, err := ResolveScope("", false, false, true,
			func(string) (mastermind.Record, error) {
				t.Fatal("resolve must not run")
				return mastermind.Record{}, nil
			})
		if err != nil {
			t.Fatalf("ResolveScope: %v", err)
		}
		if sc != (Scope{Named: true}) || rec != nil {
			t.Errorf("ResolveScope = %+v, %v, want the named scope", sc, rec)
		}
	})
}
