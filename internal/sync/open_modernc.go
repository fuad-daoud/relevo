//go:build modernc

package sync

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// errNoSyncDriver is what a build with no sync driver refuses with. It is a
// sentinel a caller can classify, and it wraps db.ErrInvalid so the refusal does
// not read as an internal failure the user has to report.
var errNoSyncDriver = fmt.Errorf("sync: this build carries no sync driver: %w", db.ErrInvalid)

// OpenRemote refuses on a build with no sync driver. The modernc build is the
// way back from Turso, so there is no client to open; a verb that needs one says
// so here rather than failing at the first push with something the user would
// have to decode.
func OpenRemote(context.Context, OpenConfig) (SyncClient, error) {
	return nil, errNoSyncDriver
}
