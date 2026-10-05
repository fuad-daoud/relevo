package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The twin rule: what the pass does with a stale row whose stamp would collide
// with a row this installation already stamped.
//
// Stamping an unstamped row moves it into this installation's half of its
// natural key, and every live unique index over the tables the pass stamps is
// keyed by origin plus that key -- migration 014 re-keys the four it already had
// and migration 016 adds the fifth. So the stamp collides exactly when a row
// already stamped with this origin holds the same key, which is what a rename or
// a re-import leaves behind: one checkout recorded twice, once before the origin
// column existed and once after it. NULL never collides, so a repo row with no
// origin_url or no common_dir is not in either of its table's index sets and
// cannot be a twin at all.
//
// The mirror tables are read models the ingest rebuilds from a log, so there the
// stale '' row loses to the stamped live row and is dropped: repo is the shape
// the field log line named, and binding, mastermind and chains are the same
// shape on their own keys. binding_record is the system of record and is never
// deleted, so a twin there halts the table for a person to decide rather than
// resolving it in code.
//
// The drop is not unconditional on a mirror either, but the common case is not a
// halt. round and event point at binding, binding points at repo and mastermind,
// with no cascade, so the database refuses to let a row history depends on
// disappear -- and on a database whose stale rows are referenced that refusal is
// every row, which is why a pass over one reported itself skipped having settled
// nothing. The twin is the same checkout recorded twice, so a pointer at the stale
// id is a pointer at the same checkout the live id names: the pass repoints it
// first (see origin_repoint.go), which changes no attribution and leaves the drop
// with nothing to refuse on.
//
// Where a reference genuinely cannot move -- the live row already holds what the
// pointer would bring -- the pass does not decide. It leaves the row unstamped and
// halts it for the same reason a record does, naming the table and how many rows
// are left rather than what any of them holds, and carries on to the next row.

// twinRule is how one table resolves a stale row its stamp would collide on.
type twinRule int

const (
	// twinDropStale drops the stale '' row in favour of the stamped live row.
	twinDropStale twinRule = iota
	// twinHaltForHuman leaves the row unstamped and names the table, because
	// deciding which record survives is not the pass's call to make.
	twinHaltForHuman
)

// backfillTwinRule is that decision per table. It has one entry for every table
// backfillTables names: a table the pass stamps with no rule here is a table
// whose collision still aborts the whole pass, and a rule for a table the pass
// does not stamp is a case no row can reach.
var backfillTwinRule = map[string]twinRule{
	"binding_record": twinHaltForHuman,
	"binding":        twinDropStale,
	"repo":           twinDropStale,
	"mastermind":     twinDropStale,
	"chains":         twinDropStale,
}

// backfillTwinKey is the live unique index over one table that a stamp can
// collide on: the index's own name, the table it covers, the columns it keys on
// after origin, and its WHERE predicate. The predicate is spelled out because it
// is why some rows cannot collide at all.
type backfillTwinKey struct {
	name    string
	table   string
	cols    []string
	partial string
}

// backfillTwinKeys is every index a stamp can collide on, read off the schema:
// the four migration 014 re-keys by origin and the one migration 016 creates with
// origin already part of it. Every table backfillTables names appears here
// exactly as the schema has it, and a table's own index set is what its rule is
// read against.
var backfillTwinKeys = []backfillTwinKey{
	{
		name: "binding_record_origin_owner_name_uidx", table: "binding_record",
		cols: []string{"owner", "name"}, partial: "archived_at IS NULL",
	},
	{
		name: "binding_name_created_at_uidx", table: "binding",
		cols: []string{"name", "created_at"},
	},
	{
		name: "repo_origin_origin_url_uidx", table: "repo",
		cols: []string{"origin_url"}, partial: "origin_url IS NOT NULL",
	},
	{
		name: "repo_origin_common_dir_uidx", table: "repo",
		cols: []string{"common_dir"}, partial: "common_dir IS NOT NULL",
	},
	{
		name: "mastermind_origin_harness_session_uidx", table: "mastermind",
		cols: []string{"harness_kind", "session_id"},
	},
	{
		name: "chains_origin_owner_name_uidx", table: "chains",
		cols: []string{"owner", "name"},
	},
}

// twinKeysFor is every index the pass can collide on inside one table, in schema
// order, so a report of the same database twice is the same list twice.
func twinKeysFor(table string) []backfillTwinKey {
	var keys []backfillTwinKey
	for _, k := range backfillTwinKeys {
		if k.table == table {
			keys = append(keys, k)
		}
	}
	return keys
}

// collidingRowPair is one stale row and the stamped live row it loses to: the
// row to remove, and the id everything pointing at it is moved to.
type collidingRowPair struct {
	stale string
	live  string
}

// collidingRowPairs names the stale rows in table whose stamp would collide with a
// row already stamped as origin under one index, each with that live row's id. It
// is the join that index would have to make: the two rows key alike, both inside
// the index's predicate, and they share an origin only because the stale side is
// the one about to move.
//
// Both ids come back because resolving the pair needs both: the stale row is what
// the pass drops, and the live row's id is where every reference to the stale row
// moves first. Returning only the stale id is what left the pass no way to settle
// a row anything pointed at.
//
// Reading the pairs before moving anything is what makes the pass incremental.
// The rows it drops are exactly the rows that would otherwise raise the unique
// violation, so the stamp that follows completes instead of aborting the pass on
// the first one and taking every clean row with it.
func collidingRowPairs(q queryer, table, origin string, key backfillTwinKey) ([]collidingRowPair, error) {
	var on strings.Builder
	for i, col := range key.cols {
		if i > 0 {
			on.WriteString(" AND ")
		}
		fmt.Fprintf(&on, "s.%s = l.%s", col, col)
	}
	inside := ""
	if key.partial != "" {
		inside = " AND (s." + key.partial + ") AND (l." + key.partial + ")"
	}
	query := `SELECT s.id, l.id FROM ` + table + ` AS s JOIN ` + table + ` AS l ON ` + on.String() +
		inside + ` WHERE s.origin = '' AND l.origin = ?`

	rows, err := q.QueryContext(context.Background(), query, origin)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var pairs []collidingRowPair
	for rows.Next() {
		var pair collidingRowPair
		if err := rows.Scan(&pair.stale, &pair.live); err != nil {
			return nil, err
		}
		pairs = append(pairs, pair)
	}
	return pairs, rows.Err()
}

// collidingRowIDs is collidingRowPairs read for the stale side only, which is all
// a caller that previews the rows without settling them needs.
func collidingRowIDs(q queryer, table, origin string, key backfillTwinKey) ([]string, error) {
	pairs, err := collidingRowPairs(q, table, origin, key)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		ids = append(ids, pair.stale)
	}
	return ids, nil
}

// sqliteConstraintForeignKey is SQLITE_CONSTRAINT_FOREIGNKEY, the extended code
// a parent row's children refuse the removal of. It is the one constraint
// failure the twin rule expects to meet, because it means history points at the
// stale row rather than that the rule was wrong.
const sqliteConstraintForeignKey = 787

// settleStaleRow resolves one stale row in its own transaction: every row that
// names it is repointed at the live twin's id, and then the stale row is removed.
// Both halves are one transaction, so a pointer that cannot move leaves the stale
// row standing with every reference to it intact rather than half-migrated.
//
// The id comes from the unstamped side of the join, and the drop's WHERE repeats
// that, so this can only ever remove the stale half of a pair: the stamped live
// row the pair is resolved in favour of is not this statement's to touch. live is
// that row's id, and is only ever the twin the rule already paired this one with.
//
// A row whose references cannot all be moved -- or that the engine still refuses
// to remove -- is halted rather than dropped: the row stays, unstamped, for a
// person to decide, and the pass continues past it. That is the outcome a halt has
// always meant, so a halt is never a reason to lose the pass.
func settleStaleRow(d *DB, table, id, live string) (halted bool, err error) {
	derr := d.Tx(func(t *Tx) error {
		if rerr := repointReferences(t, table, id, live); rerr != nil {
			return rerr
		}
		_, derr := t.exec(`DELETE FROM `+table+` WHERE id = ? AND origin = ''`, id)
		return derr
	})
	if derr == nil {
		return false, nil
	}
	// A reference that cannot move, and an engine that still refuses the removal
	// once nothing names it, are both the pass declining to decide on this row.
	var refusal *repointRefusal
	if errors.As(derr, &refusal) || isForeignKeyRefusal(derr) {
		return true, nil
	}
	return false, fmt.Errorf("origin backfill: %s: drop stale row: %w", table, mapBusy(derr))
}

// isForeignKeyRefusal reports whether an error is the engine refusing a removal
// on the row's foreign keys.
//
// The message is checked first, and deliberately ahead of the extended code. A
// Turso constraint carries the primary SQLITE_CONSTRAINT in both code slots, so
// its extended code is 19 and never 787, and reading the code alone decided such
// an error was not a foreign key refusal at all -- which is how the halt this
// branch exists for went unreported and the pass aborted with nothing done. Only
// the message distinguishes the two refusals on that engine.
func isForeignKeyRefusal(err error) bool {
	if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		return true
	}
	code, ext, ok := errCode(err)
	return ok && code&0xff == sqliteConstraint && (ext == sqliteConstraintForeignKey || ext == 0)
}
