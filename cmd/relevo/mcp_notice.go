package main

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// mcpNoticeTTL is how long relevo mcp keeps the daemon's record before reading it
// again: long enough to skip a stat per tool call, short enough to notice an
// upgrade within a turn or two.
const mcpNoticeTTL = 30 * time.Second

// mcpNotice is the line relevo mcp appends to every tool result when the daemon
// has moved on to a newer relevo. It returns "" unless the daemon runs a
// different version and has refused nothing: a difference is transient during a
// re-exec, and a refused binary is `relevo doctor`'s story.
func mcpNotice(own string, info store.DaemonInfo, ok bool) string {
	if !ok || info.Version == own || info.ReexecFailed != nil {
		return ""
	}
	return fmt.Sprintf("note: relevo was upgraded to %s; this session's relevo MCP server is still %s. Reconnect it (/mcp) or restart the session to load the new version.", info.Version, own)
}

// cachedString returns f's value, recomputing it at most once per ttl.
func cachedString(ttl time.Duration, now func() time.Time, f func() string) func() string {
	var (
		at   time.Time
		val  string
		have bool
	)
	return func() string {
		t := now()
		if have && t.Before(at.Add(ttl)) {
			return val
		}
		val = f()
		at = t
		have = true
		return val
	}
}
