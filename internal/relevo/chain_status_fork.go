package relevo

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// chainChildRows is a chain's child chains in key order, with how many of them
// have ended. The rows whose Parent names this chain are its children; the key
// order is the order the engine named them in ("1".."N"), so the listing reads
// in fork order rather than in whatever order the query returned.
func chainChildRows(s *store.Store, c db.ChainRow) (children []db.ChainRow, done int) {
	if s == nil {
		return nil, 0
	}
	rows, err := s.Chains()
	if err != nil {
		return nil, 0
	}
	for _, r := range rows {
		if r.Parent == c.Name && r.Parent != "" {
			children = append(children, r)
		}
	}
	sortChainRowsByKey(children)
	for _, r := range children {
		if chain.Status(r.Status) == chain.StatusDone {
			done++
		}
	}
	return children, done
}

// sortChainRowsByKey orders child rows by their fork key, the suffix after the
// parent name. Keys are the short digit runs the engine assigned in order, so
// the order is the declaration order -- and that order is numeric, not textual:
// with ten or more children a plain string comparison reads "10" before "2".
// A key that does not parse as a number -- which the engine never writes --
// falls back to the name comparison, so the sort still orders the rows.
func sortChainRowsByKey(rows []db.ChainRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, aok := chainRowForkKey(rows[i])
		b, bok := chainRowForkKey(rows[j])
		if aok && bok && a != b {
			return a < b
		}
		return rows[i].Name < rows[j].Name
	})
}

// chainRowForkKey is a row's own fork key -- its name with its own
// "<parent>." prefix trimmed -- as the integer the engine declared it with. ok
// is false for a row whose key is not a number.
func chainRowForkKey(c db.ChainRow) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(c.Name, c.Parent+"."))
	if err != nil {
		return 0, false
	}
	return n, true
}

// chainChildFacts fills a chain's fork facts: the child chain names in key order
// and how many of them are done.
func chainChildFacts(s *store.Store, f *view.ChainFacts, c db.ChainRow) {
	children, done := chainChildRows(s, c)
	for _, ch := range children {
		f.Children = append(f.Children, ch.Name)
	}
	f.ChildrenDone = done
}

// chainChildRowOf is the row a child chain prints under its parent: the child's
// own chain row, unchanged in every other way. The nesting itself is read from
// the facts the parent the row names, so the status and the statusline cannot
// disagree about which rows are nested. Its halt reason is already the row's
// detail, exactly as any halted chain's is.
func chainChildRowOf(s *store.Store, c db.ChainRow) view.BindingStatus {
	return viewChainRow(s, c)
}
