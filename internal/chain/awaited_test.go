package chain

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

type fakeMembers struct {
	rows []db.ChainMemberRow
	err  error
}

func (f fakeMembers) ChainMembers(string) ([]db.ChainMemberRow, error) { return f.rows, f.err }

func TestAwaitedReadsTheLegacyColumns(t *testing.T) {
	t.Parallel()

	row := db.ChainRow{Name: "c", Builder: "c", Reviewer: "c-rev", AwaitingMember: MemberReviewer, AwaitingRound: 3}
	name, round, ok := Awaited(fakeMembers{}, row)
	if !ok || name != "c-rev" || round != 3 {
		t.Errorf("Awaited = %q %d %v, want c-rev 3 true", name, round, ok)
	}
	row.AwaitingMember = MemberSecurity
	if _, _, ok := Awaited(fakeMembers{}, row); ok {
		t.Error("a part the row does not fill resolves")
	}
}

func TestAwaitedReadsTheStatesActorThroughChainMember(t *testing.T) {
	t.Parallel()

	state := func(member string) []byte {
		raw, err := json.Marshal(workflow.State{Awaiting: workflow.Awaiting{Member: member, Round: 4}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	members := fakeMembers{rows: []db.ChainMemberRow{{Binding: "c", Actor: ""}, {Binding: "c-rev", Actor: "reviewer"}}}

	for _, tc := range []struct {
		label   string
		members MemberLister
		row     db.ChainRow
		want    string
		ok      bool
	}{
		{"builder", members, db.ChainRow{Name: "c", StateJSON: state("builder")}, "c", true},
		{"reviewer", members, db.ChainRow{Name: "c", StateJSON: state("reviewer")}, "c-rev", true},
		{"a check run", members, db.ChainRow{Name: "c", StateJSON: state("")}, "", false},
		{"no member runs it", members, db.ChainRow{Name: "c", StateJSON: state("security")}, "", false},
		{"a read failure", fakeMembers{err: errors.New("locked")}, db.ChainRow{Name: "c", StateJSON: state("builder")}, "", false},
		{"bad state", members, db.ChainRow{Name: "c", StateJSON: []byte("{")}, "", false},
	} {
		name, round, ok := Awaited(tc.members, tc.row)
		if name != tc.want || ok != tc.ok || (ok && round != 4) {
			t.Errorf("%s: Awaited = %q %d %v, want %q %v", tc.label, name, round, ok, tc.want, tc.ok)
		}
	}
}
