package chatlabel

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/usage"
)

// Resolver builds a Label for a mastermind record. A zero Resolver answers with empty labels.
type Resolver struct {
	Exec       usage.Exec // nil -> opencode labels are empty
	OpencodeDB string     // "" -> opencode labels are empty
	TailBytes  int64      // 0 -> DefaultTailBytes
}

// Resolve returns the harness's own name for one session. Every failure ends
// in the empty Label; it never returns an error.
func (r Resolver) Resolve(ctx context.Context, kind, sessionID, locator string) Label {
	switch kind {
	case "claude":
		if locator == "" {
			return Label{}
		}
		tail, err := ReadTail(locator, r.TailBytes)
		if err != nil {
			return Label{}
		}
		return Claude(tail)
	case "opencode":
		if r.Exec == nil || r.OpencodeDB == "" || !opencodeSessionID.MatchString(sessionID) {
			return Label{}
		}
		ctx, cancel := context.WithTimeout(ctx, OpencodeTimeout)
		defer cancel()
		out, err := r.Exec.Run(ctx, "sqlite3", "-readonly", r.OpencodeDB, OpencodeQuery(sessionID))
		if err != nil {
			// A pre-2.0 database has no session_v2; the legacy table has the title.
			out, err = r.Exec.Run(ctx, "sqlite3", "-readonly", r.OpencodeDB, OpencodeLegacyQuery(sessionID))
			if err != nil {
				return Label{}
			}
		}
		return Opencode(out)
	default:
		return Label{}
	}
}
