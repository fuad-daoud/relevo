package sync

import (
	"context"
)

// OpenConfig is what an enable asks a remote to open. It is this package's own
// shape rather than the driver's own struct, for two reasons: the seed matrix
// decides BootstrapIfEmpty here and a test has to be able to read that decision
// without the driver in reach, and the token belongs in a field this package
// controls, so there is one place that decides the value never reaches a log.
//
// BootstrapIfEmpty is a plain bool because every seed decision is total: no
// case wants the driver's pointer left unset, so its default never decides for
// us, and the field cannot be forgotten because there is nothing to forget.
type OpenConfig struct {
	// Path is the file to sync.
	Path string
	// RemoteURL is the remote to sync it with.
	RemoteURL string
	// Namespace is the remote's namespace, empty when the remote does not name
	// one.
	Namespace string
	// ClientName is what the remote is told this client is called.
	ClientName string
	// AuthToken is the bearer token. It is carried into the open and into
	// nothing else: no message, no log line and no failure in this package ever
	// formats it.
	AuthToken []byte
	// BootstrapIfEmpty is whether the open may take the remote's initial state.
	// It is false only where the seed matrix already decided the first call is a
	// push, and every such decision pulls explicitly afterwards.
	BootstrapIfEmpty bool
}

// Opener builds the handle one open config describes. It is a function rather
// than an interface because there is exactly one thing to do with it -- hand it
// to a client -- and a fake closure is the whole of what a test needs.
type Opener func(context.Context, OpenConfig) (SyncClient, error)
