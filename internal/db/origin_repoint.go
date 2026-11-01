package db

import (
	"fmt"
	"strings"
)

// Why the pass repoints before it drops: the stale '' row and the stamped live
// row it collides with are the same checkout recorded twice, so they share
// common_dir. A row that points at the stale one points at the same checkout
// the live one is, so moving that pointer changes no attribution: only the id
// it holds changes, to the id of the row that survives. Without it the drop
// cannot happen: the engine refuses to remove a row its children still name.

// twinReference is one foreign key that names a twin-ruled table's id: the child
// table, the column holding the parent id, and whether the schema deletes the
// child along with the parent. A CASCADE child is a reference the drop would
// resolve by deleting the child -- which loses history -- so it is repointed
// instead, which can only fail where the live parent already holds a row under
// that child's primary key, a case for a person rather than a merge.
type twinReference struct {
	child   string
	column  string
	cascade bool
}

// backfillTwinReferences is every foreign key the schema holds into a
// twin-ruled table, read off the schema. It is spelled out here rather than
// discovered at run time, and a test pins it against the schema: a key the
// schema has and this list omits is a stale row the pass still cannot drop.
// binding_record carries no entry: nothing drops it, so nothing has to be
// repointed out of the way, and dropping a record row would cascade-delete its
// children, which is why the rule declines to drop one at all.
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
// stale row the twin rule drops has nothing left pointing at it. Each statement
// is guarded by the column's own value, and the statements run inside the
// caller's transaction with the drop, which is what makes the pair atomic. A
// unique violation is the refusal that matters: round and event are keyed
// (binding_id, number) and (binding_id, seq), so moving a round onto the live
// binding collides whenever that binding already holds a round with the same
// number, and the caller halts the row for a person rather than dropping one
// side of it. It writes through the caller's transaction rather than the shared
// read seam, because a repoint is a write and must commit or roll back with
// the drop beside it.
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
