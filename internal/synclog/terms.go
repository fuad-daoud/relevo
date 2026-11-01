package synclog

// Term is a sync_binding_term row: (root, W, upto) means W stopped writing this
// root, valid only through upto in W's log.
type Term struct {
	RootID        string
	Writer        string
	Upto          int
	RegrantOrigin string
	RegrantSeq    int
}

func (t *Term) HasRegrant() bool {
	return t != nil && t.RegrantOrigin != "" && t.RegrantSeq > 0
}

// EntryValid reports whether entry e is valid under the term. Present means valid
// iff e.Seq <= upto, or a regrant covers it: e.After names the regrant, or the
// regrant's origin wrote past its seq. Anything above upto is refused as a
// "writer moved" drop.
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
