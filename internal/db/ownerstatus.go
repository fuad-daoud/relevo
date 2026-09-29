//go:build unix

package db

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// ProbeOwner dials sock and performs the handshake, returning the owner's own
// answer. It is bounded like a dial, so a caller that must answer quickly
// cannot hang on a wedged owner.
func ProbeOwner(sock string) (OwnerStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	in, err := client.Info(ctx, sock)
	if err != nil {
		return OwnerStatus{Socket: sock}, err
	}
	return OwnerStatus{Socket: sock, PID: in.PID, Version: wire.Version, Conns: in.Conns}, nil
}
