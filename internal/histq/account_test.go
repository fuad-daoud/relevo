package histq

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestGroupByAccount pins histq axis: `by account` buckets rounds by the
// login they drew from, and a round that drew none reads as the unrecorded
// key.
func TestGroupByAccount(t *testing.T) {
	acct := func(s string) *string { return &s }
	rows := []db.RoundRow{
		{Number: 1, BindingName: "a", StartedAt: fxDay(19, 9), Account: acct("cp1")},
		{Number: 2, BindingName: "b", StartedAt: fxDay(19, 10), Account: acct("cp2")},
		{Number: 3, BindingName: "c", StartedAt: fxDay(19, 11), Account: acct("cp1")},
		{Number: 4, BindingName: "d", StartedAt: fxDay(20, 9)},
	}

	if _, ok := ParseAxis("account"); !ok {
		t.Fatal("ParseAxis(account) is not a known axis")
	}

	counts := map[string]int{}
	for _, g := range Group(rows, AxisAccount, fxLoc) {
		counts[g.Key] = g.Rounds
	}
	if counts["cp1"] != 2 || counts["cp2"] != 1 || counts["-"] != 1 {
		t.Errorf("account buckets = %v, want cp1:2 cp2:1 -:1", counts)
	}
}
