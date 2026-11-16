package sync

// The pull half of a machine that reads none of the log's history: the labels
// the directory shows and the mirrored documents, applied through the doc
// pre-pass. A none machine never runs Import, so it moves no mark; the
// importer's applied state is what makes a redelivery a no-op.

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// PrepassMirrored applies the mirrored documents over the caller's log, for a
// machine that reads none of the history. The labels go first because the
// pre-pass names each entry's origin from the directory, then the importer is
// built exactly as PrepassSecrets builds it and runs over the mirrored names.
// Unlike PrepassSecrets there is no stored-credentials early return: labels
// and documents are this machine's whole view of the fleet, so it runs every
// tick.
func PrepassMirrored(shared *db.DB, l Local, t synclog.LogTransport) (int, error) {
	if err := RecordMachineLabels(shared, t); err != nil {
		return 0, err
	}
	imp := synclog.NewImporter(shared, t)
	if err := BindVerifier(imp, l); err != nil {
		return 0, err
	}
	BindDocs(imp, shared)
	applied, err := imp.PrepassDocs(MirrorDocNames()...)
	if err != nil {
		return applied, fmt.Errorf("sync: mirror the documents: %w", err)
	}
	return applied, nil
}

// ReadHistoryNone reports whether this machine reads none of the log's
// history, as its own section says. An absent section reads as full, which is
// exactly what it is.
func ReadHistoryNone(l Local) (bool, error) {
	settings, err := ReadSettings(l)
	if err != nil {
		return false, err
	}
	return settings.HistoryNone(), nil
}

// MirroredResult is one pre-pass count as the ImportResult the callers expect.
// It carries no applied count: the pre-pass counts handled entries, and a
// redelivery of an unchanged document is handled without changing anything, so
// the count cannot tell a real change from one. Reporting it would mark every
// quiet tick as moving, and a drainImport loop fed with it would spin to its
// deadline on the first redelivery.
func MirroredResult(_ int) synclog.ImportResult {
	return synclog.ImportResult{}
}
