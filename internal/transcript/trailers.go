package transcript

import (
	"bytes"

	"github.com/fuad-daoud/relevo/internal/trail"
)

// relevo's supervisor appends two bookkeeping lines to a builder's stream once
// the builder exits: a relevo-exit: code and a relevo-rusage: payload. Neither
// is the builder's words, so Render drops them.
var trailerPrefixes = [...]string{
	trail.ExitTrailer,
	trail.RusageTrailerPrefix,
}

func isTrailerLine(trimmed []byte) bool {
	for _, p := range trailerPrefixes {
		if bytes.HasPrefix(trimmed, []byte(p)) {
			return true
		}
	}
	return false
}
