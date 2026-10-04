package db

import (
	"context"
	"fmt"
)

// historyTables is every table whose rows are shared history -- what the file
// that leaves this machine is for. The enable seed decision turns on whether
// there is anything here worth seeding a remote with, so the list is the tables
// a push would carry and nothing else.
//
// installation is deliberately absent even though it is a shared table: every
// machine has one from the moment it is installed, and counting it would make
// every machine look like it holds history and send a new machine down the
// upload path instead of the bootstrap one.
var historyTables = []string{
	"artifact", "binding", "binding_event", "binding_record",
	"chain_check", "chain_event", "chain_member", "chains",
	"event", "repo", "round", "round_file",
}

// HasSharedHistory reports whether this database holds a row a push would carry.
// It is the one fact the enable seed decision needs about this machine, and it
// is a count of rows rather than a file size: a database holding only a mirror
// row or an empty chain has nothing to seed a remote with, and one holding a
// single sealed round has a great deal.
//
// It refuses rather than answering false for a table it cannot read, because a
// false answer here is the answer that pushes this machine's history over a
// remote that already had some.
func HasSharedHistory(d *DB) (bool, error) {
	ctx := context.Background()
	for _, table := range historyTables {
		var n int64
		query := `SELECT EXISTS(SELECT 1 FROM ` + table + `)`
		if err := d.sqlDB.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return false, fmt.Errorf("db: shared history: %s: %w", table, mapBusy(err))
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}
