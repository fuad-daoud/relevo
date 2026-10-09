package syncpipe

// The daemon-side wiring an enable drives: a worker transport built from the
// machine-local rows the enable stored, and the replica beside the shared
// database the worker owns.

import (
	"bytes"
	"fmt"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// OpenSupervisor builds the transport an enable drives from the rows the
// preflight has just stored: where the remote is, which token reaches it, and
// where the worker's replica belongs.
//
// It starts no process. The first call starts the worker, which is also what
// bootstraps the replica, so a preflight that refuses never leaves a child
// behind. The replica's path travels on the worker's command line because the
// worker owns the only handle on that file, and a path it derived itself could
// name a file the daemon did not mean.
func OpenSupervisor(shared *db.DB, local relevosync.Local) (*Supervisor, error) {
	if shared == nil || local == nil {
		return nil, fmt.Errorf("syncpipe: no database to sync: %w", db.ErrInvalid)
	}
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return nil, err
	}
	if settings.RemoteURL == "" {
		return nil, relevosync.ErrNoRemote
	}
	token, ok, err := relevosync.ReadToken(local)
	if err != nil {
		return nil, err
	}
	if !ok || len(bytes.TrimSpace(token)) == 0 {
		return nil, relevosync.ErrNoToken
	}
	cfg := Config{
		Args:   []string{"sync-worker", "--replica", ReplicaPath(shared.Path())},
		Origin: shared.Origin(),
		URL:    settings.RemoteURL,
		Token:  string(token),
	}
	return NewSupervisor(cfg, relevosync.NewBreaker(local)), nil
}

// ReplicaPath is the replica beside a shared database: the one file the sync
// driver owns, named from the shared path so the daemon and the worker cannot
// disagree about which state directory it is in.
func ReplicaPath(sharedPath string) string {
	return filepath.Join(filepath.Dir(sharedPath), "relevo-sync.db")
}
