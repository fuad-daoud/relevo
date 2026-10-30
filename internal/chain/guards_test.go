package chain

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestRefuseRunningMemberRefusesOnlyARunningChain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status  Status
		refused bool
	}{
		{StatusRunning, true},
		{StatusHalted, false},
		{StatusStopped, false},
		{StatusDone, false},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()

			s := store.New(t.TempDir())
			member := newMemberChainRow(t, s, "c", string(tc.status), "")

			storeErr := RefuseRunningMemberStore(s, member.Name)
			var txErr error
			var owns bool
			if err := s.WithLock(func(tx *store.Tx) error {
				txErr = RefuseRunningMember(tx, member.Name)
				owns = OwnsMember(tx, member.Name)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for label, err := range map[string]error{"store": storeErr, "tx": txErr} {
				if got := errors.Is(err, ErrRunningMember); got != tc.refused {
					t.Errorf("%s: refused = %v (%v), want %v", label, got, err, tc.refused)
				}
				if tc.refused && !strings.Contains(err.Error(), `binding "c-plan" belongs to running chain c; relevo stop c first`) {
					t.Errorf("%s: message = %q", label, err)
				}
			}
			if !owns {
				t.Error("OwnsMember = false for a chain member")
			}
		})
	}
}

func TestRefuseRunningMemberPassesANonMember(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	newMemberChainRow(t, s, "c", string(StatusRunning), "")
	if err := RefuseRunningMemberStore(s, "plain"); err != nil {
		t.Errorf("store: %v, want nil", err)
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		if err := RefuseRunningMember(tx, "plain"); err != nil {
			t.Errorf("tx: %v, want nil", err)
		}
		if OwnsMember(tx, "plain") {
			t.Error("OwnsMember = true for a binding in no chain")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
