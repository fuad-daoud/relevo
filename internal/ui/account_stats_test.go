package ui

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/stats"
)

// TestCandidatesTabAccountSection pins cockpit stat surface: the account
// breakdown appears under the candidate table only when the report carries
// accounts, so a host with none shows the tab it always did.
func TestCandidatesTabAccountSection(t *testing.T) {
	env := statsTestEnv(t, 132, 34)
	v := statsView{window: "30d", loaded: true, rep: statsFixture()}

	lines, _ := v.candidatesTabLines(env, 132)
	if body := stripANSI(strings.Join(lines, "\n")); strings.Contains(body, "ACCOUNT") {
		t.Errorf("candidate tab printed an account section with no accounts:\n%s", body)
	}

	v.rep.Accounts = []stats.ScoreRow{{Token: "cp1", Rounds: 3, Closed: 2, Reported: 2, DonePct: 100}}
	lines, _ = v.candidatesTabLines(env, 132)
	body := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(body, "ACCOUNT") || !strings.Contains(body, "cp1") {
		t.Errorf("candidate tab missing the account section:\n%s", body)
	}
}
