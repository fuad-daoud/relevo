package remote

// ChainRoundFacts is one closed-round entry of a chain member view, completed
// from the member's binding view for the round that view itself describes: the
// server keeps the uncommitted work and the prior tokens of the round it last
// closed on the binding view and, on a server older than both fields, nowhere
// else, so a client that read the entry alone would install that round as
// clean and bill it for nothing. Every other round's entry is the whole record
// -- the server ships no older figures to complete it from -- and is returned
// untouched, which is what keeps the newest round's values off the older ones.
func ChainRoundFacts(cr ClosedRoundView, view BindingView) ClosedRoundView {
	if cr.Round != view.ClosedRound {
		return cr
	}
	if cr.DirtyCommit == "" {
		cr.DirtyCommit = view.DirtyCommit
	}
	if cr.PriorTokens == nil {
		cr.PriorTokens = view.PriorTokens
	}
	// The binding view's stamps are the newest closed round's on a server that
	// keeps them, so an entry that carries none is completed from the view.
	// An older round has no view-level figure to complete it from and keeps
	// whatever the server shipped for that round alone.
	if cr.RoundStartedAt.IsZero() {
		cr.RoundStartedAt = view.RoundStartedAt
	}
	return cr
}
