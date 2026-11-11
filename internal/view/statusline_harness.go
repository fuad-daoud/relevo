package view

// statusLineHarnessOn builds the row's two builder names: Harness from the
// candidate's harness segment, On from the candidate's short name, each with
// the server suffix when the round runs there.
func statusLineHarnessOn(b BindingStatus) (harness, on string) {
	harness = harnessSegment(b.BuilderCandidate)
	if b.Server != "" {
		harness += "@" + b.Server
	}
	if b.BuilderCandidate != "" {
		on = b.BuilderName
		if on == "" {
			on = harnessSegment(b.BuilderCandidate)
		}
		if b.Server != "" {
			on += "@" + b.Server
		}
	}
	return harness, on
}
