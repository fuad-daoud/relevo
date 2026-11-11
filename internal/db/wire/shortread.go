package wire

import "strings"

// shortReadWAL is the storage engine's text for a WAL frame read past the end
// of the file: a pool whose WAL index names frames a reset truncated away
// fails every statement with it until the pool is rebuilt.
const shortReadWAL = "short read on WAL frame"

// IsShortReadWAL reports whether err carries the engine's short WAL-frame
// text. A wrap keeps the text, so a daemon path that wraps the driver error
// and a wire Error rebuilt from its message both still match.
func IsShortReadWAL(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), shortReadWAL)
}
