package db

import (
	"fmt"
	"strings"
)

// Why the pass repoints before it drops.
//
// The stale '' row and the stamped live row it collides with are the same
// checkout recorded twice, so they share common_dir (that sharing is the
// collision the twin rule reads). A row that points at the stale one therefore
// points at the same checkout the live one is, and moving that pointer changes no
// attribution: same directory, same machine, same history. Only the id it holds
// changes, to the id of the row that survives.
//
// That is the whole repair, and without it the drop cannot happen at all: the
// engine refuses to remove a row its children still name, and the refusal
// arrived on a database whose stale rows are referenced -- so the pass had
// nothing it could settle and reported itself skipped having done nothing.

// twinReference is one foreign key that names a twin-ruled table's id: the child
// table, the column holding the parent id, and whether the schema deletes the
// child along with the parent.
//
// The cascade flag is not decoration. A CASCADE child is a reference the drop
// resolves by itself, by deleting the child -- which loses history. Repointing it
// instead keeps it, and can only fail the repoint where the live parent already
// holds a row under that child's primary key, which is a case for a person rather
// than a merge.
type twinReference struct {
	child   string
	column  string
	cascade bool
}

// backfillTwinReferences is every foreign key the schema holds into a
// twin-ruled table, read off the schema: the tables and columns that point at
// binding_record, binding, repo, mastermind and chains.
//
// It is spelled out here rather than discovered at run time so the rule reads as
// the schema does, and a test pins it against the schema -- the same shape as
// backfillTwinKeys, and for the same reason. A key the schema has and this list
// omits is a stale row the pass still cannot drop; a key here the schema does not
// have is a repoint of a table that does not exist.
//
// A table the pass only ever halts on -- binding_record, the system of record --
// carries no entry: nothing drops it, so nothing has to be repointed out of the
// way. Its children cascade, and dropping a record row would delete them, which is
// the reason the rule declines to drop one at all.
var backfillTwinReferences = map[string][]twinReference{
	// binding.repo_id is the one the field named: nine bindings sharing a repo_id,
	// all stamped, all pointing at an unstamped repo row.
	"repo": {{child: "binding", column: "repo_id"}},
	"binding": {
		// A binding names three things, and each is a reference the drop refuses on.
		{child: "binding", column: "forked_from_binding_id"},
		{child: "event", column: "binding_id"},
		{child: "round", column: "binding_id"},
	},
	"mastermind": {{child: "binding", column: "mastermind_id"}},
	"chains":     {{child: "chain_event", column: "chain_id", cascade: true}},
}

// twinReferencesFor is every foreign key into one table, in schema order, so a
// report of the same database twice is the same list twice.
func twinReferencesFor(table string) []twinReference {
	return backfillTwinReferences[table]
}

// repointReferences moves every row that names staleID to liveID instead, so the
// stale row the twin rule drops has nothing left pointing at it.
//
// Each statement is guarded by the column's own value, so this only ever touches
// rows that actually name the stale row. The statements run inside the caller's
// transaction with the drop, which is what makes the pair atomic: a reference that
// cannot move rolls the repoint back with the drop, leaving the stale row and
// every pointer to it exactly as they were.
//
// A unique violation is the refusal that matters here, and it is not hypothetical:
// round and event are keyed (binding_id, number) and (binding_id, seq), so moving
// a round onto the live binding collides whenever that binding already holds a
// round with the same number. Two histories of one binding cannot be merged by
// moving a pointer, so the caller halts the row for a person rather than dropping
// one side of it.
//
// It writes through the caller's transaction rather than the shared read seam,
// because a repoint is a write and must commit or roll back with the drop beside
// it rather than on its own.
func repointReferences(t *Tx, table, staleID, liveID string) error {
	for _, ref := range twinReferencesFor(table) {
		query := `UPDATE ` + ref.child + ` SET ` + ref.column + ` = ? WHERE ` + ref.column + ` = ?`
		if _, err := t.exec(query, liveID, staleID); err != nil {
			if isUniqueViolation(err) {
				return &repointRefusal{table: ref.child, column: ref.column}
			}
			return fmt.Errorf("origin backfill: repoint %s.%s: %w", ref.child, ref.column, mapBusy(err))
		}
	}
	return nil
}

// repointRefusal is a reference that cannot move: the live row the pointer would
// join is already taken under that child's own key. It is the pass declining to
// decide, not a database failure, so it carries no engine error to wrap.
type repointRefusal struct {
	table  string
	column string
}

func (e *repointRefusal) Error() string {
	return fmt.Sprintf("origin backfill: %s.%s already holds this row under the live id", e.table, e.column)
}

// isUniqueViolation reports whether an error is the engine refusing a write that
// would break a unique index. It matches the extended code where one is exposed
// and the message where it is not: a Turso constraint carries the primary code in
// both slots, so only the message says which index was hit.
func isUniqueViolation(err error) bool {
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return true
	}
	code, ext, ok := errCode(err)
	return ok && code&0xff == sqliteConstraint && ext == sqliteConstraintUnique
}
