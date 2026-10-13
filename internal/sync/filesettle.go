package sync

import (
	"github.com/fuad-daoud/relevo/internal/db"
)

// The work that follows a batch of arriving items once it has committed. An
// arriving agents item lands its definition files inside the importer's
// transaction, but the render that goes with it reads the config the apply
// wrote, so it cannot run there: a render inside the transaction would render
// the agent as it was before the entry arrived. The pipeline therefore drains
// what the apply queued, here, after the commit.
//
// It is a seam with one caller, the same way the documents themselves arrive
// through SetDocsBuilder and the capture through SetFileCapture: internal/sync
// cannot import internal/relevo, so the package that owns both asks for it.

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
