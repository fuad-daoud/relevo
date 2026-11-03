package relevo

import (
	"fmt"
	"strings"
)

// chainCandidateParts are the only parts a --candidate pin names: one pin
// per chain member, without creating one actor per model.
const chainCandidateParts = "builder, reviewer, planner, security"

// ParseChainCandidates reads the repeatable --candidate part=value flag into
// the per-part pins a start resolves. It is pure so the CLI pins the rule
// without a runtime: every refusal fires before anything is created.
func ParseChainCandidates(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		part, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(part) == "" || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("--candidate wants part=value, got %q", entry)
		}
		switch part {
		case "builder", "reviewer", "planner", "security":
		default:
			return nil, fmt.Errorf("--candidate part %q must be one of %s", part, chainCandidateParts)
		}
		if _, dup := out[part]; dup {
			return nil, fmt.Errorf("--candidate names %q twice: pin each of %s at most once", part, chainCandidateParts)
		}
		out[part] = value
	}
	return out, nil
}
