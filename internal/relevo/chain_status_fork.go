package relevo

import (
	"sort"

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
// the order is the declaration order. Sorting is on the whole name because the
// parent prefix is identical across the rows, which leaves the key as the only
// thing that varies.
func sortChainRowsByKey(rows []db.ChainRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
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
