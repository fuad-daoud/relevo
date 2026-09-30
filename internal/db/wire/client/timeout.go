//go:build unix

package client

import "time"

// handshakeBudget is the process-wide bound openConn applies to a new pooled
// connection. It mirrors handshakeTimeout until a caller raises it: a test that
// deliberately starves the owner (the re-exec-under-load test) raises its own
// client budget, and no production caller does. It affects connections opened
// after the call only.
var handshakeBudget = handshakeTimeout

// SetHandshakeTimeout sets the handshake bound openConn gives a connection it
// opens after the call. A non-positive d restores handshakeTimeout.
func SetHandshakeTimeout(d time.Duration) {
	if d <= 0 {
		d = handshakeTimeout
	}
	handshakeBudget = d
}
