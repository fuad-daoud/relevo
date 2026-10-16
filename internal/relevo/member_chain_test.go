package relevo

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// newMemberChainRow is one chain row named name in the status given, holding
// <name>-plan as its planner, running on server when one is set, with that
// planner bound beside it. It is the store shape the notice reads: raw records
// plus the owning chain.
func newMemberChainRow(t *testing.T, rt Runtime, name, status, server string) store.Binding {
	t.Helper()
	now := rt.Now().UTC()
	row := db.ChainRow{
		ID:           db.NewID(),
		Name:         name,
		Status:       status,
		Phase:        "build",
		Step:         "reviewing",
		Plan:         1,
		Plans:        1,
		SettingsJSON: []byte(`{"max_corrections":1}`),
		Builder:      name,
		Planner:      name + "-plan",
		Server:       server,
		Worktree:     rt.Store.Dir(name),
		MasterMindID: testMasterMindID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	member := store.Binding{
		Name: name + "-plan", CWD: rt.Store.Dir(name + "-plan"), Round: 2,
		State: store.StateNeedsYou, MasterMindID: testMasterMindID,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		return tx.Save(member)
	}); err != nil {
		t.Fatalf("seed chain %q: %v", name, err)
	}
	return member
}

// TestMemberChainPicksTheOwningChain pins the resolver's whole surface: every
// chain a member can sit in -- done, running, halted, and one running on a
// server -- resolves to the chain that names it, while a non-member and a read
// failure miss, never faulting.
func TestMemberChainPicksTheOwningChain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		chain  string
		status string
		server string
	}{
		{"xdone", string(chain.StatusDone), ""},
		{"xrun", string(chain.StatusRunning), ""},
		{"xhalt", string(chain.StatusHalted), ""},
		{"xserved", string(chain.StatusRunning), "zen"},
	} {
		t.Run(tc.chain, func(t *testing.T) {
			t.Parallel()

			rt := newRuntime(t)
			member := newMemberChainRow(t, rt, tc.chain, tc.status, tc.server)

			c, ok := MemberChain(rt.Store.ChainByMember, member.Name)
			if !ok {
				t.Fatalf("MemberChain(%q) misses, want the owning chain", member.Name)
			}
			if c.Name != tc.chain || c.Status != tc.status || c.Server != tc.server {
				t.Errorf("MemberChain(%q) = %s/%s/%s, want %s/%s/%s",
					member.Name, c.Name, c.Status, c.Server, tc.chain, tc.status, tc.server)
			}
		})
	}

	t.Run("non-member and missing miss", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name     string
			byMember func(string) (db.ChainRow, error)
		}{
			{"a binding in no chain", func(string) (db.ChainRow, error) {
				return db.ChainRow{}, store.ErrNotFound
			}},
			{"a read failure", func(string) (db.ChainRow, error) {
				return db.ChainRow{}, errors.New("database locked")
			}},
		} {
			if _, ok := MemberChain(tc.byMember, "plain"); ok {
				t.Errorf("%s: MemberChain ok, want a miss so the ordinary hint stands", tc.name)
			}
		}
	})
}

// TestMemberHintPicksTheWord pins the hint table the waiting notice is built
// from: a member whose chain is done or stopped names the clear a person can
// actually run, while a running or halted chain still owning its rounds -- and
// everyone in no chain -- keeps the status-only hint. No `unbind --done` form
// appears anywhere: that one refuses every name.
func TestMemberHintPicksTheWord(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		hint     string
		wantBoth bool
	}{
		{"done chain: both commands",
			MemberHint("api", db.ChainRow{Status: string(chain.StatusDone)}, true), true},
		{"stopped chain: both commands",
			MemberHint("api", db.ChainRow{Status: string(chain.StatusStopped)}, true), true},
		{"running chain: status only",
			MemberHint("api", db.ChainRow{Status: string(chain.StatusRunning)}, true), false},
		{"halted chain: status only",
			MemberHint("api", db.ChainRow{Status: string(chain.StatusHalted)}, true), false},
		{"served chain: the same words",
			MemberHint("api", db.ChainRow{Status: string(chain.StatusDone), Server: "zen"}, true), true},
		{"non-member: the ordinary hint",
			MemberHint("api", db.ChainRow{Status: string(chain.StatusDone)}, false), false},
	} {
		if !strings.Contains(tc.hint, "relevo status --name api") {
			t.Errorf("%s: hint = %q, want it to name the status", tc.name, tc.hint)
		}
		if strings.Contains(tc.hint, "relevo unbind") {
			t.Errorf("%s: hint = %q, want no unbind in it", tc.name, tc.hint)
		}
		if both := strings.Contains(tc.hint, "; relevo done api clears it"); both != tc.wantBoth {
			t.Errorf("%s: hint = %q, want the both-commands form = %v", tc.name, tc.hint, tc.wantBoth)
		}
	}
}
