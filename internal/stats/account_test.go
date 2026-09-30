package stats

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestBuildAccountScorecard pins cockpit stat: rounds group by the login
// they drew from, and a report whose rows carry no account has no account
// rows.
func TestBuildAccountScorecard(t *testing.T) {
	acct := func(s string) *string { return &s }
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rows := []db.RoundRow{
		{Number: 1, StartedAt: now, Outcome: db.OutcomeReported, Account: acct("cp1")},
		{Number: 2, StartedAt: now, Outcome: db.OutcomeHalted, Account: acct("cp1")},
		{Number: 3, StartedAt: now, Outcome: db.OutcomeReported, Account: acct("cp2")},
		{Number: 4, StartedAt: now, Outcome: db.OutcomeOpen},
	}

	rep := Build(Inputs{Rows: rows, Until: now})
	if len(rep.Accounts) != 2 {
		t.Fatalf("Accounts = %d rows, want 2", len(rep.Accounts))
	}
	byName := map[string]ScoreRow{}
	for _, s := range rep.Accounts {
		byName[s.Token] = s
	}
	if byName["cp1"].Rounds != 2 || byName["cp1"].Halted != 1 {
		t.Errorf("cp1 row = %+v, want 2 rounds and 1 halted", byName["cp1"])
	}
	if byName["cp2"].Rounds != 1 {
		t.Errorf("cp2 row = %+v, want 1 round", byName["cp2"])
	}

	none := Build(Inputs{Rows: []db.RoundRow{{Number: 1, StartedAt: now, Outcome: db.OutcomeOpen}}, Until: now})
	if len(none.Accounts) != 0 {
		t.Errorf("account-less rows produced %d account rows, want 0", len(none.Accounts))
	}
}
