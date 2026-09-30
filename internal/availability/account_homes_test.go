package availability

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/account"
)

// accountHomeChecker answers the default check from defs and the per-account
// check from homes, each keyed by the definition list so a test can make one
// role's file missing in one home only.
type accountHomeChecker struct {
	defs  map[string][]string
	homes map[string][]string
}

func accountHomeKey(home, kind string, definitions []string) string {
	return home + "\x00" + kind + "\x00" + strings.Join(definitions, ",")
}

func (c accountHomeChecker) Missing(kind string, definitions []string) []string {
	return c.defs[kind+"\x00"+strings.Join(definitions, ",")]
}

func (c accountHomeChecker) MissingIn(home, kind string, definitions []string) []string {
	return c.homes[accountHomeKey(home, kind, definitions)]
}

// TestRolesMissingGatesPerAccountHome pins the per-account gate rule: when a
// candidate's provider is served by an account pool, the definitions are
// checked in each account's own home, and a home that lacks them gates only
// that account -- a2 is gated, a1 is not.
func TestRolesMissingGatesPerAccountHome(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	d.Accounts = account.Set{
		{Name: "a1", Harness: account.Claude, Groups: []string{"test"}, ConfigDir: "/homes/a1"},
		{Name: "a2", Harness: account.Claude, Groups: []string{"test"}, ConfigDir: "/homes/a2"},
	}
	d.Roles = accountHomeChecker{
		defs:  map[string][]string{},
		homes: map[string][]string{accountHomeKey("/homes/a2", "claude", []string{"reviewer"}): {".claude/agents/reviewer.md"}},
	}

	gates := rolesMissingGates(d)
	if len(gates) != 1 {
		t.Fatalf("gates = %+v, want exactly one for the broken account home", gates)
	}
	g := gates[0]
	if g.Token != "test@a2" || g.Kind != RolesMissing || g.Role != "reviewer" {
		t.Errorf("gate = %+v, want test@a2 roles_missing for reviewer", g)
	}
	if !strings.Contains(g.Note, "in account a2 (/homes/a2)") {
		t.Errorf("note = %q, want the account name and home", g.Note)
	}
	if !strings.Contains(g.Note, "run relevo config agents --kind claude") {
		t.Errorf("note = %q, want the shipped install fix", g.Note)
	}
}

// TestRolesMissingGatesNoAccountsUsesDefaultHome pins that an empty pool keeps
// the single default-home check, so a host with no accounts configured gates
// exactly as it did before.
func TestRolesMissingGatesNoAccountsUsesDefaultHome(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	d.Roles = accountHomeChecker{
		defs:  map[string][]string{"claude\x00plan-executor,researcher": {".claude/agents/plan-executor.md"}},
		homes: map[string][]string{},
	}

	gates := rolesMissingGates(d)
	if len(gates) != 1 || gates[0].Token != testClaudeRef {
		t.Fatalf("gates = %+v, want one default-home gate on %s", gates, testClaudeRef)
	}
	if strings.Contains(gates[0].Note, " in account ") {
		t.Errorf("note = %q, want no account wording for the default home", gates[0].Note)
	}
}
