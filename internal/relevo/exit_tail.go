package relevo

import (
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
)

// builderCandidateOf is the configured candidate b's builder was started from,
// or the zero candidate when it is unparseable or no longer configured.
func builderCandidateOf(rt Runtime, b store.Binding) candidate.Candidate {
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil || rt.Candidates == nil {
		return candidate.Candidate{}
	}
	cand, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return candidate.Candidate{}
	}
	return cand
}

// denialSuffix is the exit entry's permission-blocked suffix, empty when the
// tail held no denial.
func denialSuffix(line string, isDenial bool) string {
	if !isDenial {
		return ""
	}
	return "; permission-blocked: " + line
}
