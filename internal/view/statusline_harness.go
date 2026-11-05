package view

// statusLineHarnessOn builds the row's two builder names: Harness from the
// candidate's harness segment, On from the candidate's short name, each with
// the server and burst-node suffixes. The node rides the server suffix with
// the literal arrow, so the OpenCode plugin renders the Go string unchanged.
func statusLineHarnessOn(b BindingStatus) (harness, on string) {
	harness = harnessSegment(b.BuilderCandidate)
	if b.Server != "" {
		harness += "@" + b.Server
	}
	if b.Node != "" {
		harness += "→" + b.Node
	}
	if b.BuilderCandidate != "" {
		on = b.BuilderName
		if on == "" {
			on = harnessSegment(b.BuilderCandidate)
		}
		if b.Server != "" {
			on += "@" + b.Server
		}
		if b.Node != "" {
			on += "→" + b.Node
		}
	}
	return harness, on
}
