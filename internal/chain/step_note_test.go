package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestStepNoteNamesTheMembersPart pins the prompt note a member round runs
// under: the chain word plus the part the member fills, and the plain chain
// word for a member the chain row names in no part and for a binding in no
// chain at all.
func TestStepNoteNamesTheMembersPart(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	newMemberChainRow(t, s, "c", string(StatusRunning), "")
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.ChainMembersPut("c", []db.ChainMemberRow{
			{Binding: "c-side", Actor: "sidekick", Seq: 2},
		})
	}); err != nil {
		t.Fatalf("seed partless member: %v", err)
	}

	for _, tc := range []struct {
		name string
		want string
	}{
		{"c", "chain builder"},
		{"c-plan", "chain planner"},
		{"c-side", "chain"},
		{"plain", "chain"},
	} {
		if err := s.WithLock(func(tx *store.Tx) error {
			if got := StepNote(tx, tc.name); got != tc.want {
				t.Errorf("StepNote(%q) = %q, want %q", tc.name, got, tc.want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
