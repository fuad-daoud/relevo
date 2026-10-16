package relevo

import (
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// keyedExporter is an exporter that signs with the machine's stored key, read
// now so an unlock takes effect on the next call.
func keyedExporter(shared *db.DB, t synclog.LogTransport) (*synclog.Exporter, error) {
	key, err := relevosync.SigningKey(shared.Local())
	if err != nil {
		return nil, err
	}
	e := synclog.NewExporter(shared, t)
	e.SetKey(key)
	relevosync.BindDocs(e, shared)
	return e, nil
}

// keyedImporter is an importer that verifies with the machine's stored
// keyring -- the whole chain, not only its newest key.
func keyedImporter(shared *db.DB, t synclog.LogTransport) (*synclog.Importer, error) {
	i := synclog.NewImporter(shared, t)
	if err := relevosync.BindVerifier(i, shared.Local()); err != nil {
		return nil, err
	}
	relevosync.BindDocs(i, shared)
	return i, nil
}

// exportOnce exports with the machine's stored key.
func exportOnce(shared *db.DB, t synclog.LogTransport) (synclog.ExportResult, error) {
	e, err := keyedExporter(shared, t)
	if err != nil {
		return synclog.ExportResult{}, err
	}
	return e.Export()
}

// importOnce imports with the machine's stored key.
func importOnce(shared *db.DB, t synclog.LogTransport) (synclog.ImportResult, error) {
	i, err := keyedImporter(shared, t)
	if err != nil {
		return synclog.ImportResult{}, err
	}
	return i.Import()
}
