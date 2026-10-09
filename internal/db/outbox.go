package db

import (
	"fmt"
)

// TruncateOutbox empties the change log the shared tables' triggers write.
//
// It is the half of the outbox's life a machine that is not syncing needs: the
// triggers record every shared-table write whether or not anything drains them,
// so a machine whose sync is off collects one entry per write for as long as it
// is installed. An entry is an identity and an operation rather than a payload
// and the row it names is still in the shared file, so dropping the entries
// loses nothing a reconcile cannot re-derive.
//
// Only the rows go. The delete is the one statement, and no vacuum, checkpoint
// or log truncation follows it: compacting the file belongs to whoever compacts
// it, and a truncate that also rewrote the file would put a large write on a
// path that runs on a tick. An empty table and a file whose schema predates the
// outbox are both nothing to do.
func (d *DB) TruncateOutbox() error {
	return d.Tx(func(t *Tx) error { return t.TruncateOutbox() })
}

func (t *Tx) TruncateOutbox() error {
	if _, err := t.exec(`DELETE FROM sync_outbox`); err != nil {
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("db: truncate outbox: %w", mapBusy(err))
	}
	return nil
}
