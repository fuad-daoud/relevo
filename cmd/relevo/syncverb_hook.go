package main

import (
	"context"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncpipe"
)

// installSyncVerbHook makes the owner's socket the way a client asks for a sync
// verb, and it is the whole of what removes the stop dance. The daemon already
// holds both files under the lock, so a client asking for a verb never opens the
// file and never competes for it. Nothing here re-decides what a verb means: the
// hook is a verb.Runner, the same value the cockpit's sync actions drive.
// Serializing waits for the Daemon's guard, so installSyncVerbSerializing rebinds
// the same hook once the daemon exists; until then a verb runs unserialized
// rather than not at all. A nil server, or a handle with no local file, leaves
// the hook uninstalled and the owner refuses the verb.
func installSyncVerbHook(srv *owner.Server, d *db.DB) {
	if srv == nil || d == nil {
		return
	}
	runner := buildVerbRunner(d)
	if runner == nil {
		return
	}
	srv.OnSyncVerb = runner.OwnerVerb
}

// buildVerbRunner resolves the machine-local handle and builds the runner the
// hook and the cockpit share. This stays in cmd because resolving the handles
// means the sync pipe, which lives below the verb package. A handle with no
// local file yields nil; installSyncVerbSerializing binds the guards once the
// daemon exists.
func buildVerbRunner(d *db.DB) *verb.Runner {
	if d == nil {
		return nil
	}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		// Leaving the hook off makes the owner refuse rather than answer from the
		// shared file, which would report a configured machine as unconfigured.
		slog.Warn("relevo daemon: sync verbs are not served", "err", err)
		return nil
	}
	// Narrow the sync files before any worker can open the replica, so no driver
	// file is read through a wider mode than the owner's.
	if err := syncpipe.SecureSyncFiles(d.Path()); err != nil {
		slog.Warn("relevo daemon: could not narrow the sync files to owner-only", "err", err)
	}
	// The client starts no process until a call drives it, and the opener below
	// replaces it with one pointed at the remote and token an enable stored.
	inner := syncpipe.NewSyncRunner(syncpipe.Config{}, local)
	inner.Blobs = func() blobstore.BlobStore { return relevosync.StoreFromLocal(local) }
	replicaPath := syncpipe.ReplicaPath(d.Path())
	inner.ReplicaPath = replicaPath
	return verb.NewRunner(verb.RunnerDeps{
		Shared:      d,
		Local:       local,
		Path:        d.Path(),
		ReplicaPath: replicaPath,
		StagingDir:  syncpipe.BlobStagingDir(d.Path()),
		Inner:       inner,
		ClientName:  dbSyncHandleName,
		Blobs:       relevosync.NewR2Store,
		// The opener reads the machine-local rows at the moment an enable runs, so the
		// worker is pointed at the remote and token the enable just stored.
		Open: func(context.Context) (synclog.LogTransport, error) {
			return syncpipe.OpenSupervisor(d, local)
		},
		// A join's rows hold no bucket until the fleet's secrets document has been
		// applied: the join's first worker starts with none.
		OpenNoBucket: func(context.Context) (synclog.LogTransport, bool, error) {
			return syncpipe.OpenNoBucketSupervisor(d, local)
		},
	})
}

// installSyncVerbSerializing rebinds the owner's verb hook with the daemon's
// in-flight guard, so a verb and a tick cannot interleave: both record the same
// markers, and two at once would write each other's outcomes. A server whose hook
// the startup did not install stays without one. It runs once the Daemon exists,
// so the guard is the one the seal hook and the idle tick already take.
func installSyncVerbSerializing(ctx context.Context, d *relevo.Daemon, srv *owner.Server, handle *db.DB) {
	if srv == nil || handle == nil || srv.OnSyncVerb == nil {
		return
	}
	runner := buildVerbRunner(handle)
	if runner == nil {
		return
	}
	runner.Serialize = d.WaitSyncSlot
	runner.Trigger = func() { d.SyncNow(ctx) }
	d.SetSyncVerbs(runner)
	srv.OnSyncVerb = runner.OwnerVerb
	// A reader's hint runs on the daemon's own context: the connection that carried
	// it is gone by the time the attempt it queued is running.
	srv.OnSyncFreshen = func() { d.Freshen(ctx) }
}
