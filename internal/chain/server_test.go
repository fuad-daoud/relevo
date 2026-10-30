package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestGoneMirrorReadsOnlyTheGoneWording(t *testing.T) {
	t.Parallel()

	gone := GoneReason("c", "zen")
	if gone != "chain c is gone from zen" {
		t.Fatalf("GoneReason = %q", gone)
	}
	for _, tc := range []struct {
		row  db.ChainRow
		want bool
	}{
		{db.ChainRow{Name: "c", Server: "zen", Status: string(StatusHalted), Reason: gone}, true},
		{db.ChainRow{Name: "c", Server: "zen", Status: string(StatusHalted), Reason: "gate red"}, false},
		{db.ChainRow{Name: "c", Server: "zen", Status: string(StatusDone), Reason: gone}, false},
		{db.ChainRow{Name: "c", Server: "other", Status: string(StatusHalted), Reason: gone}, false},
	} {
		if got := GoneMirror(tc.row); got != tc.want {
			t.Errorf("GoneMirror(%+v) = %v, want %v", tc.row, got, tc.want)
		}
	}
}

func TestServerMemberNeedsAServerChain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, status, server string
		member, active       bool
	}{
		{"local", string(StatusRunning), "", false, false},
		{"served", string(StatusRunning), "zen", true, true},
		{"served-done", string(StatusDone), "zen", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := store.New(t.TempDir())
			member := newMemberChainRow(t, s, tc.name, tc.status, tc.server)
			if got := ServerMemberStore(s, member.Name); got != tc.member {
				t.Errorf("ServerMemberStore = %v, want %v", got, tc.member)
			}
			if got := ServerMemberActive(s, member.Name); got != tc.active {
				t.Errorf("ServerMemberActive = %v, want %v", got, tc.active)
			}
			if err := s.WithLock(func(tx *store.Tx) error {
				if got := ServerMember(tx, member.Name); got != tc.member {
					t.Errorf("ServerMember = %v, want %v", got, tc.member)
				}
				if ServerMember(tx, "plain") {
					t.Error("ServerMember = true for a binding in no chain")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if ServerMemberStore(s, "plain") || ServerMemberActive(s, "plain") {
				t.Error("a binding in no chain reads as a server member")
			}
		})
	}
}
