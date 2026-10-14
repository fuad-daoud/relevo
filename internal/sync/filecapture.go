package sync

import (
	"github.com/fuad-daoud/relevo/internal/db"
)

// The capture of files a person edited on disk. A person editing a hook script
// or a native agent file under the config root has made a config edit, and it
// exports like one; the capture is what turns that edit into a config write.
//
// internal/sync cannot import internal/config -- config imports internal/sync
// -- so the pipeline asks for it through a hook installed by the package that
// owns both, the same way the documents themselves arrive through
// SetDocsBuilder. Nothing here decides anything: it is a seam with one caller.

var fileCapture func(shared *db.DB)

// SetFileCapture installs the capture the pipeline runs before it exports. It
// is nil until installed, and a pipeline with no capture simply exports what
// the config store already holds.
func SetFileCapture(fn func(shared *db.DB)) { fileCapture = fn }

// CaptureFiles runs the installed capture over shared, if there is one. It
// never fails the attempt: a capture that cannot read the disk leaves the
// config as it is, which is the state the exporter already describes.
func CaptureFiles(shared *db.DB) {
	if fileCapture == nil || shared == nil {
		return
	}
	fileCapture(shared)
}
