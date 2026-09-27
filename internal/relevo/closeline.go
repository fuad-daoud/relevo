package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/store"
)

// artifactClause returns the artifact line of a round's close payload.
// For a reader it is "<Label>: relevo show <name> --round <n> --summary".
// For a writer it is "Report: relevo show <name> --round <n> --report".
// The literal word "Report" is used for the writer, not the writer agent's
// output label, so the bytes cannot drift if a custom writer names it
// differently.
func artifactClause(shape, output, name string, round int) string {
	if shape == store.ShapeReader {
		label := output
		if label == "" {
			label = defaultOutput
		}
		return fmt.Sprintf("%s: %s", titleFirst(label), showCommand(name, round, "summary"))
	}
	return "Report: " + showCommand(name, round, "report")
}

// closeClause returns the artifact clause for b's round close payload.
func closeClause(rt Runtime, b store.Binding, round int) string {
	return artifactClause(b.Shape, readerOutputLabel(rt, b), b.Name, round)
}

// titleFirst upper-cases the first byte of s. The empty string stays empty.
// Actor names from agentsrc match [a-z][a-z0-9-]{0,23}, so ASCII is enough.
func titleFirst(s string) string {
	if s == "" {
		return ""
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}
