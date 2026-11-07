package sync

import "errors"

// ErrConnectionLost reports a connection to Turso that dropped before an answer
// came back. It is not a refusal: nothing says the remote declined anything, so
// it is transient and absent from IsPermanentRefusal, and its text never carries
// the sentence a refusal does.
var ErrConnectionLost = errors.New("sync: lost the connection to Turso")
