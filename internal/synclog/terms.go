package synclog

// Term is the in-memory representation of a sync_binding_term row.
// A row (root, W, upto) means "W stopped being the writer of this root;
// its entries are valid only through upto in W's log".
type Term struct {
	RootID        string
	Writer        string
	Upto          int
	RegrantOrigin string
	RegrantSeq    int
}

// HasRegrant reports whether the term carries a regrant entry.
func (t *Term) HasRegrant() bool {
	return t != nil && t.RegrantOrigin != "" && t.RegrantSeq > 0
}

// EntryValid reports whether entry e is valid under the given term (rule 2).
// Term (R, e.Origin) present: valid iff e.Seq <= upto, or regrant is set and
// (e.After == regrant, or regrant.origin == e.Origin and e.Seq > regrant.seq).
// This is the honest late write (a child from the old writer's own snapshot
// or from a log applied after the root flipped). Anything above upto is refused
// (post-hand-over write: a bug or a forgery) and is reported as a drop with
// reason "writer moved".
func EntryValid(term *Term, e Entry) bool {
	if term == nil {
		return true
	}
	if e.Seq <= term.Upto {
		return true
	}
	if term.HasRegrant() {
		if e.After != nil && e.After.Origin == term.RegrantOrigin && e.After.Seq == term.RegrantSeq {
			return true
		}
		if term.RegrantOrigin == e.Origin && e.Seq > term.RegrantSeq {
			return true
		}
	}
	return false
}
