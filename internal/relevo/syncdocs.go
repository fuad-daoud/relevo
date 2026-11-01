package relevo

import (
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/consent"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The config package imports internal/sync, so the documents the pipeline
// answers for come from here, where both can be imported. The same seam
// carries the capture of files a person edited on disk, a config edit the
// pipeline has to make before it exports.
func init() {
	relevosync.SetDocsBuilder(sharedDocs)
	relevosync.SetFileCapture(captureEditedFiles)
}

// sharedDocs is the documents of one machine: its config, bound to the local
// file and the config root the applier needs; its gates, bound to the same
// file the ledger is in; the two consent documents, each bound to the file its
// own store is in, the repository answering with the shared rows and the
// session with the machine-local ones; and its secrets, bound to the same store
// the config is, because both read and write the one secret table. It is the
// one place a document is bound: every pipeline stage reaches the documents
// through the BindDocs sites, so a document added here is picked up by the
// steady pipeline, the join, the unlock and the verbs at once, and a
// construction site that forgot it fails rather than quietly syncing nothing.
func sharedDocs(shared *db.DB) []synclog.SharedDoc {
	local := gatesLocal(shared)
	return []synclog.SharedDoc{
		machineConfig(shared).Doc(),
		availability.OpenGates(local, nil),
		consent.OpenRepo(shared),
		consent.OpenSessions(local),
		machineConfig(shared).SecretsDoc(),
	}
}

// captureEditedFiles is the pipeline's file capture: the hook scripts under
// this machine's config root and the harness files of every native agent, both
// read off the disk and stored where the config document carries them. It runs
// before every export and before a join or an unlock drains what was parked. A
// store that cannot resolve its config root reports no change rather than an
// error, so the export still describes the config as the store holds it.
func captureEditedFiles(shared *db.DB) {
	store := machineConfig(shared)
	store.CaptureHookFiles()
	store.CaptureAgentFiles()
}

// machineConfig is the config store of one machine with the applier bound, so
// the config document can write the files it carries, and with the re-render
// bound, so an arriving agents item is rendered once its transaction commits.
func machineConfig(shared *db.DB) *config.Store {
	root, err := ConfigRoot()
	if err != nil {
		return config.Open(shared).WithAgentRenders(queueRender(shared))
	}
	return config.Open(shared).WithFiles(HookFileApplier(root), root).
		WithAgentRenders(queueRender(shared))
}

// gatesLocal is the file the gates document belongs to: the machine-local file
// beside the shared handle, where the ledger row, the write stamps and the
// export signal all live. A handle with no local file answers as itself, the
// same rule every other machine-local surface binds through.
func gatesLocal(shared *db.DB) *db.DB {
	if shared == nil {
		return nil
	}
	return shared.LocalOrSelf()
}
