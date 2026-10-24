package doctor

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/view"
)

// DefaultGoCacheWarnBytes is the size past which the Go build cache is worth
// pruning: it grows without bound and nothing else evicts it.
const DefaultGoCacheWarnBytes int64 = 50 << 30

// GoCacheCheck builds the "go cache" row. An empty dir means no go binary
// answered, which is informational: the cache is simply not there to size.
func GoCacheCheck(dir string, bytes int64, truncated bool, warnBytes int64) Check {
	c := Check{Name: "go cache"}
	if dir == "" {
		c.Severity = SevInfo
		c.Detail = "go not available; build cache not sized"
		return c
	}
	c.Detail = fmt.Sprintf("%s in %s", view.HumanBytes(bytes), dir)
	if truncated {
		c.Detail += " (the walk ran out of budget: at least this much)"
	}
	if bytes > warnBytes {
		c.Severity = SevWarn
		c.Fix = "go clean -cache"
	}
	return c
}

// TrimpathCheck builds the "go trimpath" row, so a repository's opt-out is
// visible next to the build cache.
func TrimpathCheck(on bool) Check {
	c := Check{Name: "go trimpath", Severity: SevInfo, Detail: "-trimpath on"}
	if !on {
		c.Detail = "-trimpath off (opted out for this repository)"
	}
	return c
}
