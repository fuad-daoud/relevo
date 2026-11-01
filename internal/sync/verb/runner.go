package verb

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// RunnerDeps carries the pieces NewRunner assembles beside the daemon's handles:
// the open pair and paths, the worker, and the openers pointing it at the stored
// remote. The caller resolves opener and store: they live with the daemon's sync
// pipe, so the constructor takes them as values and only assembles. A Deps with
// no shared handle, local or worker builds nothing: there is no row sync could own.
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

// NewRunner is the one constructor for a verb runner, shared by the owner's hook
// and the cockpit's actions. The opener reads machine-local rows at enable time,
// so the worker points at the just-stored remote; a join's first worker starts
// bucketless, since the secrets document is only on the remote it opens to read.
// Serialize and Trigger stay unset: the owning daemon binds its guard later.
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
