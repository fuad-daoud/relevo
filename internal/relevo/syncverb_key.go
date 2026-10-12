package relevo

import (
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// keyedExporter is an exporter that signs with the machine's stored key, read
// now so an unlock takes effect on the next call.
func keyedExporter(shared *db.DB, t synclog.LogTransport) *synclog.Exporter {
	e := synclog.NewExporter(shared, t)
	e.SetKey(relevosync.SigningKey(shared.Local()))
	return e
}

// keyedImporter is an importer that verifies with the machine's stored key.
func keyedImporter(shared *db.DB, t synclog.LogTransport) *synclog.Importer {
	i := synclog.NewImporter(shared, t)
	i.SetKey(relevosync.SigningKey(shared.Local()))
	return i
}
