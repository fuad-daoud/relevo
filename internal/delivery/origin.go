package delivery

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// OriginLine produces the one fixed first line for a typed payload; pure, no
// trailing newline.
func OriginLine(name string, round int, dir store.Direction, kind store.Kind) string {
	if dir == store.DirToBuilder {
		return fmt.Sprintf("relevo: round %d · to runner %q · from the MasterMind (not the human)", round, name)
	}
	if kind == store.KindFindings {
		return fmt.Sprintf("relevo: consult · to MasterMind · about runner %q (not the human)", name)
	}
	// A halt gets its own line because the read-backs key on the origin string
	// alone: a shared line lets a halt confirm the round's report as "already
	// present" without ever sending it. Neither line may contain the other, or
	// the substring match still collides.
	if kind == store.KindHalt {
		return fmt.Sprintf("relevo: round %d · halt · to MasterMind · about runner %q (not the human)", round, name)
	}
	return fmt.Sprintf("relevo: round %d · to MasterMind · about runner %q (not the human)", round, name)
}

// WithOrigin prepends the origin line, unless the payload already opens with one.
func WithOrigin(payload, origin string) string {
	trimmed := strings.TrimLeft(payload, " \t\r\n")
	if strings.HasPrefix(trimmed, "relevo: ") {
		return payload
	}
	return origin + "\n\n" + payload
}
