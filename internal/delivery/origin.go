package delivery

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// OriginLine produces the one fixed first line for a typed payload.
// Pure; no trailing newline.
func OriginLine(name string, round int, dir store.Direction, kind store.Kind) string {
	if dir == store.DirToBuilder {
		return fmt.Sprintf("relevo: round %d · to runner %q · from the MasterMind (not the human)", round, name)
	}
	if kind == store.KindFindings {
		return fmt.Sprintf("relevo: consult · to MasterMind · about runner %q (not the human)", name)
	}
	// A halt gets its own line because the read-backs key on the origin string
	// alone: opencode's `LIKE '%origin%'` and agy's first-line equality both
	// match a halt row and a report row of the same round when the two share a
	// line, so a halt queued after a report's queuedAt confirms that report as
	// "already present" without ever POSTing it. Findings above is the same
	// kind-specific split, and needs no read-back change: differing first lines
	// stop matching on their own.
	//
	// Neither line may contain the other, or the substring match still
	// collides -- hence "halt" inserted mid-line rather than appended.
	if kind == store.KindHalt {
		return fmt.Sprintf("relevo: round %d · halt · to MasterMind · about runner %q (not the human)", round, name)
	}
	return fmt.Sprintf("relevo: round %d · to MasterMind · about runner %q (not the human)", round, name)
}

// WithOrigin prepends the origin line separated by a blank line, unless payload
// already begins with "relevo: " (after trimming leading whitespace), in which
// case payload is returned unchanged.
func WithOrigin(payload, origin string) string {
	trimmed := strings.TrimLeft(payload, " \t\r\n")
	if strings.HasPrefix(trimmed, "relevo: ") {
		return payload
	}
	return origin + "\n\n" + payload
}
