package remote

// The server keeps the uncommitted work and prior tokens of the round it last
// closed on the binding view, so an entry for that round is completed from the
// view; every other round's entry is the whole record and is returned untouched.
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
