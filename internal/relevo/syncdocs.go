package relevo

import (
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/consent"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The config package imports internal/sync, so the sync pipeline gets the
// documents it answers for from here, where both can be imported.
func init() { relevosync.SetDocsBuilder(sharedDocs) }

// sharedDocs is the documents of one machine: its config, bound to the local
// file the way config.Open binds it, its gates, bound to the same file the
// ledger is in, and the two consent documents, each bound to the file its own
// store is in -- the repository answers with the shared rows, the session
// answers with the machine-local ones.
//
// It is the one place a document is bound. Every pipeline stage reaches the
// documents through the BindDocs sites rather than naming one, so a document
// added here is picked up by the steady pipeline, the join, the unlock and the
// verbs at once, and a construction site that forgot it fails rather than
// quietly syncing nothing.
func sharedDocs(shared *db.DB) []synclog.SharedDoc {
	local := gatesLocal(shared)
	return []synclog.SharedDoc{
		config.Open(shared).Doc(),
		availability.OpenGates(local, nil),
		consent.OpenRepo(shared),
		consent.OpenSessions(local),
	}
}

// gatesLocal is the file the gates document belongs to: the machine-local file
// beside the shared handle, where the ledger row, the write stamps and the
// export signal all live. A handle with no local file answers as itself, which
// is the same rule every other machine-local surface binds through.
func gatesLocal(shared *db.DB) *db.DB {
	if shared == nil {
		return nil
	}
	return shared.LocalOrSelf()
}
