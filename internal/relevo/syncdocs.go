package relevo

import (
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The config package imports internal/sync, so the sync pipeline gets the
// documents it answers for from here, where both can be imported.
func init() { relevosync.SetDocsBuilder(sharedDocs) }

// sharedDocs is the documents of one machine: its config, bound to the local
// file the way config.Open binds it.
func sharedDocs(shared *db.DB) []synclog.SharedDoc {
	return []synclog.SharedDoc{config.Open(shared).Doc()}
}
