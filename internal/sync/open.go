package sync

// The shape one open describes, and the seam it is built through. This build
// carries no sync engine, so nothing opens a remote; the shape stays because it
// is where the settings, the path and the token meet, and because a caller that
// wants to say what a remote should be reached with has to be able to say it in
// one type rather than in whatever the driver happens to take.

import (
	"context"
)

// OpenConfig is what an open asks a remote for. It is this package's own shape
// rather than the driver's own struct, so a test can read the decision without
// the driver in reach and so there is one place that decides the token never
// reaches a log.
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
}

// Opener builds the handle one open config describes. It is a function rather
// than an interface because there is exactly one thing to do with it -- hand it
// to a client -- and a fake closure is the whole of what a test needs.
type Opener func(context.Context, OpenConfig) (SyncClient, error)
