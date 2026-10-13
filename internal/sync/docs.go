package sync

import (
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// DocsBuilder returns the shared documents of one machine, bound to that
// machine's own files.
type DocsBuilder func(shared *db.DB) []synclog.SharedDoc

// docsBuilder is set once at start-up by the package that owns the documents.
// The config document cannot be built here: the config package imports this
// one, so this package could not import it back.
var docsBuilder DocsBuilder

// SetDocsBuilder installs the function SharedDocs builds a machine's documents
// with. It is called before any exporter, importer or reconciler is built.
func SetDocsBuilder(b DocsBuilder) { docsBuilder = b }

// SharedDocs returns the documents of the machine whose shared file is shared.
// Each call builds new ones, because a document is bound to one store and
// several machines can run in one process.
func SharedDocs(shared *db.DB) []synclog.SharedDoc {
	if docsBuilder == nil || shared == nil {
		return nil
	}
	return docsBuilder(shared)
}

// docUser is an exporter, importer or reconciler.
type docUser interface {
	UseDocs(docs ...synclog.SharedDoc)
}

// BindDocs binds the machine's documents to a pipeline stage, so the stage
// answers for this machine's store and not for the process-wide registry.
func BindDocs(stage docUser, shared *db.DB) {
	if docs := SharedDocs(shared); len(docs) > 0 {
		stage.UseDocs(docs...)
	}
}
