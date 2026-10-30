package verb

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// RunnerDeps carries the pieces NewRunner assembles that live beside the
// daemon's handles: the pair already open under the lock, the paths beside
// them, the worker built for them, and the openers that point that worker at
// the stored remote.
//
// The caller resolves the opener and store pieces: they live with the daemon's
// sync pipe, which the verb package does not import, so the constructor takes
// them as values and only assembles. A Deps with no shared handle, no local
// handle or no worker builds nothing: there is no row sync could own, and a
// runner built without one would have to be handed a nil seam.
type RunnerDeps struct {
	Shared       *db.DB
	Local        relevosync.Local
	Path         string
	ReplicaPath  string
	StagingDir   string
	ClientName   string
	Inner        *relevosync.Runner
	Blobs        func(relevosync.R2Secrets) blobstore.BlobStore
	Open         func(context.Context) (synclog.LogTransport, error)
	OpenNoBucket func(context.Context) (synclog.LogTransport, bool, error)
}

// NewRunner is the one constructor for a verb runner, so the hook the owner
// serves and the cockpit's sync actions run are the same value over the same
// handles.
//
// The opener reads the machine-local rows at the moment an enable runs, so
// the worker it builds is pointed at the remote and token the enable just
// stored rather than the empty section the daemon started with. A join's rows
// hold no bucket until the fleet's secrets document has been applied, and that
// document is only on the remote the worker is opened to read, so the join's
// first worker starts with none where OpenNoBucket is installed.
//
// Serialize and Trigger stay unset: a runner runs inline until the daemon that
// owns the attempts binds its guard, which is the wiring's act, not the
// constructor's.
func NewRunner(deps RunnerDeps) *Runner {
	if deps.Shared == nil || deps.Local == nil || deps.Inner == nil {
		return nil
	}
	return &Runner{
		Shared:       deps.Shared,
		Local:        deps.Local,
		Path:         deps.Path,
		ReplicaPath:  deps.ReplicaPath,
		StagingDir:   deps.StagingDir,
		Runner:       deps.Inner,
		ClientName:   deps.ClientName,
		Blobs:        deps.Blobs,
		Open:         deps.Open,
		OpenNoBucket: deps.OpenNoBucket,
	}
}
