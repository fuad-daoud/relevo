package text

import (
	"fmt"
	"strings"
)

// ShowCommand renders the `relevo show` command printing one round's artifact, the
// durable way to name a closed round whose files may be sealed into the database.
func ShowCommand(name string, round int, section string) string {
	return fmt.Sprintf("relevo show %s --round %d --%s", name, round, section)
}

// Brief reduces an error to one line, for a reason or note field where a
// multi-line git message would break the line's shape.
func Brief(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if idx := strings.Index(s, "\n"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

// TitleFirst upper-cases the first byte of s; the empty string stays empty. Actor
// names from agentsrc match [a-z][a-z0-9-]{0,23}, so ASCII is enough.
func TitleFirst(s string) string {
	if s == "" {
		return ""
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}

// DefaultOutput words a reader's prompt when its agent names no output label.
const DefaultOutput = "notes"

// OutputWord is the bare noun a sentence uses for the artifact a round wrote:
// "output" for a reader, "report" for a writer.
func OutputWord(isReader bool) string {
	if isReader {
		return "output"
	}
	return "report"
}

// WithoutArtifact is the clause a sentence uses for the artifact that was not
// written: "without an output" for a reader, "without a report" for a writer. The
// reader's word stays neutral since its output label does not agree with the clause.
func WithoutArtifact(isReader bool) string {
	if isReader {
		return "without an output"
	}
	return "without a report"
}

// ArtifactClause returns the artifact line of a round's close payload: "<Label>:
// relevo show <name> --round <n> --output" for a reader, "Report: relevo show
// <name> --round <n> --report" for a writer. The writer's word stays the literal
// "Report", never its output label, so the bytes cannot drift under a custom
// writer's name.
func ArtifactClause(isReader bool, output, name string, round int) string {
	if isReader {
		label := output
		if label == "" {
			label = DefaultOutput
		}
		return fmt.Sprintf("%s: %s", TitleFirst(label), ShowCommand(name, round, "output"))
	}
	return "Report: " + ShowCommand(name, round, "report")
}
