package sync

import (
	"github.com/fuad-daoud/relevo/internal/db"
)

// The capture of files a person edited on disk: an edit to a hook script or a
// native agent file under the config root exports as a config edit.
//
// internal/sync cannot import internal/config, so the pipeline asks for it
// through a hook installed by the package that owns both, the way the documents
// arrive through SetDocsBuilder.

var fileCapture func(shared *db.DB)

// SetFileCapture installs the capture the pipeline runs before it exports. It
// is nil until installed, and a pipeline with no capture simply exports what
// the config store already holds.
func SetFileCapture(fn func(shared *db.DB)) { fileCapture = fn }

// CaptureFiles runs the installed capture over shared, if there is one. It never
// fails the attempt: a capture that cannot read the disk leaves the config as
// the exporter already describes it.
func CaptureFiles(shared *db.DB) {
	if fileCapture == nil || shared == nil {
		return
	}
	fileCapture(shared)
}
