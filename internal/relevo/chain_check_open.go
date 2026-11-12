package relevo

import (
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/view"
)

// openCheckStore is anything that lists chains and their open check rows:
// both *store.Store and *store.Tx qualify, so a caller passes whichever
// handle it already holds.
type openCheckStore interface {
	Chains() ([]db.ChainRow, error)
	ChainCheckOpen() ([]db.ChainCheckRow, error)
}

// OpenCheckMembers maps each chain with a running check to the member the
// check runs against and the check's process id. Only rows with no result
// and a recorded process count: settled rows and rows whose process was
// never recorded are not running.
func OpenCheckMembers(r openCheckStore) (map[string]int, error) {
	chains, err := r.Chains()
	if err != nil {
		return nil, err
	}
	rows, err := r.ChainCheckOpen()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	member := make(map[string]string, len(chains))
	for _, c := range chains {
		member[c.ID] = chainCheckMember(c)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		name, ok := member[row.ChainID]
		if !ok || name == "" {
			continue
		}
		out[name] = row.PID
	}
	return out, nil
}

// chainCheckMember is the member a chain's check runs against: the chain's
// builder, or its first member when no writer was recorded.
func chainCheckMember(c db.ChainRow) string {
	if c.Builder != "" {
		return c.Builder
	}
	if members := view.MembersOf(c); len(members) > 0 {
		return members[0]
	}
	return ""
}
