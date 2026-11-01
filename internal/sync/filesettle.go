package sync

import (
	"github.com/fuad-daoud/relevo/internal/db"
)

// The work that follows a batch of arriving items once it has committed: the
// render reads the config the apply wrote, so it cannot run inside the
// importer's transaction, where the agent still reads as it was. internal/sync
// cannot import internal/relevo, so the package that owns both installs it, the
// way the documents arrive through SetDocsBuilder.

var fileSettle func(shared *db.DB)

// SetFileSettle installs the work the pipeline runs once an import has
// committed. It is nil until installed, and a pipeline with none simply leaves
// the queue where it is.
func SetFileSettle(fn func(shared *db.DB)) { fileSettle = fn }

// SettleFiles runs the installed settle over shared, if there is one. It never
// fails the attempt: whatever could not be rendered is a warning, and the
// entries that named it are applied either way.
func SettleFiles(shared *db.DB) {
	if fileSettle == nil || shared == nil {
		return
	}
	fileSettle(shared)
}
