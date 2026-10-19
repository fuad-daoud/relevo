package relevo

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestChainHaltedOverUncommittedWorkMatchesOnlyTheDirtyWordings(t *testing.T) {
	t.Parallel()
	rt, _ := chainRuntime(t)
	c := db.ChainRow{Name: "shop"}
	b := store.Binding{Name: "shop-builder", CWD: "/tmp/tree", Branch: "feat"}

	haltAtZeroBudgetRepair(t, rt)
	zeroBudget := flowChainRow(t, rt).Reason

	for _, tt := range []struct {
		name   string
		reason string
		want   bool
	}{
		{"readable tree", chainDirtyReason(rt, c, b, 2, "3 files changed (+1/-1)"), true},
		{"mirror tree", chainMirrorDirtyReason(rt, c, b, 2), true},
		{"unread tree", chainDirtyUnreadReason(c, b, 2, errors.New("boom")), false},
		{"empty", "", false},
		{"zero-budget halt", zeroBudget, false},
	} {
		if got := chainHaltedOverUncommittedWork(tt.reason); got != tt.want {
			t.Errorf("%s: chainHaltedOverUncommittedWork(%q) = %v, want %v", tt.name, tt.reason, got, tt.want)
		}
	}
}
