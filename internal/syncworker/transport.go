package syncworker

import "strings"

// transportNeedles are the words the sync driver's text carries when the
// connection to the remote dropped before an answer came back. The driver
// reports the loss only as text, so a Go-layer io.EOF is deliberately not
// matched: a local file read ends the same way and is not a lost connection.
// The bare EOF needle includes the closing quote of the request URL the HTTP
// client names, so a wrapped io.EOF ("read replica: EOF") does not match.
var transportNeedles = []string{
	`": EOF`,
	"connection reset by peer",
	"broken pipe",
	"unexpected EOF",
}

// isTransportError reports whether err is a connection to the remote that
// dropped. An error carrying the remote's refusal marker is never one: a
// refused statement reached the remote and must keep its own class instead of
// reading as a fault a later attempt fixes.
func isTransportError(err error) bool {
	text := err.Error()
	if strings.Contains(text, remoteRefusalMarker) {
		return false
	}
	for _, needle := range transportNeedles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
