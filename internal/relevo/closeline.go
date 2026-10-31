package relevo

import (
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
)

// closeClause returns the artifact clause for b's round close payload.
func closeClause(rt Runtime, b store.Binding, round int) string {
	return text.ArtifactClause(b.Shape == store.ShapeReader, readerOutputLabel(rt, b), b.Name, round)
}

// artifactNoun is the noun a stored sentence uses for b's artifact: the
// reader's resolved output label, or "report" for a writer.
func artifactNoun(rt Runtime, b store.Binding) string {
	if b.Shape == store.ShapeReader {
		return readerOutputLabel(rt, b)
	}
	return "report"
}
