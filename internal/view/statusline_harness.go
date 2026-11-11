package view

import (
	"strings"
	"unicode/utf8"
)

// dropServerToFit shortens on's "name@server→node" to "name→node" inside mid
// when mid is wider than w. The cut runs from the right, so without this the
// node goes first, and the node is what tells two rounds on one server apart.
func dropServerToFit(mid, on string, w int) string {
	if utf8.RuneCountInString(mid) <= w {
		return mid
	}
	at, arrow := strings.LastIndex(on, "@"), strings.LastIndex(on, "→")
	if at < 0 || arrow < at {
		return mid
	}
	return strings.Replace(mid, on, on[:at]+on[arrow:], 1)
}

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
