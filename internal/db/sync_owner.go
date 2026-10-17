package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ownerStep is one step from a shared row toward the installation that owns it:
// read column from parent, where parentKey holds the value fed in.
type ownerStep struct {
	// column is the column read from the parent table.
	column string
	// parent is the table to read it from.
	parent string
	// key is the parent column the incoming value is compared against.
	key string
}

// ownerRule is how one shared table's rows resolve to the installation that owns
// them. A table with no hops carries the owner itself and owner names that
// column; a child walks hops outward, and the first hop is fed by the child
// column the table holds. A table whose parent depends on a discriminator
// carries one rule per kind, in the order its trigger lists them.
type ownerRule struct {
	// child is the column on the row that feeds the first hop. It is empty for
	// a table that resolves without a hop.
	child string
	// owner is the column the resolution ends at when there are no hops: the
	// installation table ends at its own id, every other root at origin or writer.
	owner string
	// hops are the steps outward, innermost first.
	hops []ownerStep
	// kinds resolves the row per owner_kind for the one table that carries a
	// discriminator, and is empty for every other table.
	kinds []ownerKind
	// oldOnUpdate indicates that the update trigger attributes to OLD rather than NEW.
	oldOnUpdate bool
}

// ownerKind is one discriminator value's route to an owner.
type ownerKind struct {
	// kind is the owner_kind value this rule applies to.
	kind string
	// rule is the route a row of that kind takes.
	rule ownerRule
}

// OwnerRules is how each shared table's rows resolve to the installation that
// owns them, in SharedTables order.
//
// This is the Go half of the resolution the outbox triggers already perform in
// SQL. The triggers are immutable once applied, so a second hand-written copy of
// their logic is where the two would drift; TestOwnerResolutionMatchesTriggers
// compares each rule against the live trigger body, which is the only copy that
// cannot go stale.
var OwnerRules = map[string]ownerRule{
	"repo":               {owner: "origin"},
	"mastermind":         {owner: "origin"},
	"binding_record":     {owner: "writer", oldOnUpdate: true},
	"installation":       {owner: "id"},
	"binding":            {owner: "writer", oldOnUpdate: true},
	"chains":             {owner: "origin"},
	"availability_event": {owner: "origin"},
	"latency_sample":     {owner: "origin"},
	"binding_event":      {child: "record_id", hops: []ownerStep{{column: "writer", parent: "binding_record", key: "id"}}},
	"round_file":         {child: "record_id", hops: []ownerStep{{column: "writer", parent: "binding_record", key: "id"}}},
	"chain_event":        {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"chain_member":       {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"chain_check":        {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"round":              {child: "binding_id", hops: []ownerStep{{column: "writer", parent: "binding", key: "id"}}},
	"event":              {child: "binding_id", hops: []ownerStep{{column: "writer", parent: "binding", key: "id"}}},
	"artifact": {child: "round_id", hops: []ownerStep{
		{column: "binding_id", parent: "round", key: "id"},
		{column: "writer", parent: "binding", key: "id"},
	}},
	"transcript": {kinds: []ownerKind{
		{kind: "round", rule: ownerRule{child: "owner_id", hops: []ownerStep{
			{column: "binding_id", parent: "round", key: "id"},
			{column: "writer", parent: "binding", key: "id"},
		}}},
		{kind: "mastermind", rule: ownerRule{child: "owner_id", hops: []ownerStep{
			{column: "origin", parent: "mastermind", key: "id"},
		}}},
	}},
}

// creatorColumn returns "origin" for binding_record and binding, and empty string
// for every other table.
func creatorColumn(tbl string) string {
	if tbl == "binding_record" || tbl == "binding" {
		return "origin"
	}
	return ""
}

// CreatorColumn returns "origin" for binding_record and binding, and empty string
// for every other table.
func CreatorColumn(tbl string) string {
	return creatorColumn(tbl)
}

// ownerExpression renders the rule's SQL for one row alias, in the shape the
// outbox triggers write: a root's owner column, a child's nested parent
// subqueries, or the discriminator's CASE for a table that has one.
//
// The same rendering serves the trigger comparison and the row lookup, so the
// text a test pins against a trigger body is the text the query runs.
func ownerExpression(rule ownerRule, alias string) string {
	if len(rule.kinds) > 0 {
		return ownerKindExpression(rule, alias)
	}
	if len(rule.hops) == 0 {
		return alias + "." + rule.owner
	}
	value := alias + "." + rule.child
	for i, hop := range rule.hops {
		inner := "SELECT " + hop.column + " FROM " + hop.parent + " WHERE " + hop.key + " = " + value
		// Every step but the outermost is a subquery in a value position and
		// needs its own parentheses; the outermost one is wrapped by the return.
		if i < len(rule.hops)-1 {
			inner = "(" + inner + ")"
		}
		value = inner
	}
	return "(" + value + ")"
}

// ownerKindExpression renders the CASE a discriminator table's trigger writes.
// The kinds are rendered in the rule's order, so the text a trigger is compared
// against is ordered the way the trigger orders its WHEN arms.
func ownerKindExpression(rule ownerRule, alias string) string {
	var b strings.Builder
	b.WriteString("CASE " + alias + ".owner_kind")
	for _, k := range rule.kinds {
		b.WriteString(" WHEN '" + k.kind + "' THEN " + ownerExpression(k.rule, alias))
	}
	b.WriteString(" ELSE NULL END")
	return b.String()
}

// ResolveOwner returns the installation that owns one shared row, resolving it
// the way the outbox triggers do. found is false when the row has no owner to
// resolve to: the row is absent, or its parent is already gone, which is the
// case a child removed by a cascade lands in. A caller that skips such an entry
// is right to, because the parent's own delete entry carries the removal to every
// importer.
func (d *DB) ResolveOwner(tbl, pk string) (string, bool, error) {
	return resolveOwner(context.Background(), d.sqlDB, tbl, pk)
}

func (t *Tx) ResolveOwner(tbl, pk string) (string, bool, error) {
	return resolveOwner(t.ctx, t.conn, tbl, pk)
}

func resolveOwner(ctx context.Context, q queryer, tbl, pk string) (string, bool, error) {
	shared := sharedTable(tbl)
	if shared == nil {
		return "", false, fmt.Errorf("db: resolve owner: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	rule, ok := OwnerRules[tbl]
	if !ok {
		return "", false, fmt.Errorf("db: resolve owner: %s has no owner rule: %w", tbl, ErrInvalid)
	}
	keys, err := exchangeKeyValues(pk, len(shared.PrimaryKey))
	if err != nil {
		return "", false, err
	}
	query, args := ownerQuery(*shared, rule, keys)
	var owner sql.NullString
	err = q.QueryRowContext(ctx, query, args...).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: resolve owner %s %s: %w", tbl, pk, mapBusy(err))
	}
	if !owner.Valid {
		return "", false, nil
	}
	return owner.String, true, nil
}

// ownerQuery builds the lookup for one row's owner: the rule's expression over
// the row's own table, restricted by the primary key with every key value bound.
// The table and column names come from SharedTables and OwnerRules, never from
// the pk text, which is parsed into parameters.
func ownerQuery(shared SharedTable, rule ownerRule, keys []any) (string, []any) {
	where := make([]string, len(shared.PrimaryKey))
	args := make([]any, 0, len(keys))
	for i, col := range shared.PrimaryKey {
		where[i] = quoteIdent(col) + " = ?"
		args = append(args, keys[i])
	}
	query := "SELECT " + ownerExpression(rule, quoteIdent(shared.Name)) +
		" FROM " + quoteIdent(shared.Name) + " WHERE " + strings.Join(where, " AND ")
	return query, args
}

// ResolveRoot returns the root ID (the binding_record or binding primary key id,
// or chains/mastermind/repo id) for one shared row, resolving it through its parent chain.
// found is false when the row has no root: the row is absent, or its parent is already gone.
func (d *DB) ResolveRoot(tbl, pk string) (string, bool, error) {
	return resolveRoot(context.Background(), d.sqlDB, tbl, pk)
}

func (t *Tx) ResolveRoot(tbl, pk string) (string, bool, error) {
	return resolveRoot(t.ctx, t.conn, tbl, pk)
}

func resolveRoot(ctx context.Context, q queryer, tbl, pk string) (string, bool, error) {
	shared := sharedTable(tbl)
	if shared == nil {
		return "", false, fmt.Errorf("db: resolve root: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	rule, ok := OwnerRules[tbl]
	if !ok {
		return "", false, fmt.Errorf("db: resolve root: %s has no owner rule: %w", tbl, ErrInvalid)
	}
	keys, err := exchangeKeyValues(pk, len(shared.PrimaryKey))
	if err != nil {
		return "", false, err
	}
	query, args := rootQuery(*shared, rule, keys)
	var rootID sql.NullString
	err = q.QueryRowContext(ctx, query, args...).Scan(&rootID)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: resolve root %s %s: %w", tbl, pk, mapBusy(err))
	}
	if !rootID.Valid {
		return "", false, nil
	}
	return rootID.String, true, nil
}

func rootQuery(shared SharedTable, rule ownerRule, keys []any) (string, []any) {
	where := make([]string, len(shared.PrimaryKey))
	args := make([]any, 0, len(keys))
	for i, col := range shared.PrimaryKey {
		where[i] = quoteIdent(col) + " = ?"
		args = append(args, keys[i])
	}
	query := "SELECT " + rootExpression(rule, quoteIdent(shared.Name)) +
		" FROM " + quoteIdent(shared.Name) + " WHERE " + strings.Join(where, " AND ")
	return query, args
}

func rootExpression(rule ownerRule, alias string) string {
	if len(rule.kinds) > 0 {
		var b strings.Builder
		b.WriteString("CASE " + alias + ".owner_kind")
		for _, k := range rule.kinds {
			b.WriteString(" WHEN '" + k.kind + "' THEN " + rootExpression(k.rule, alias))
		}
		b.WriteString(" ELSE NULL END")
		return b.String()
	}
	if len(rule.hops) == 0 {
		return alias + ".id"
	}
	value := alias + "." + rule.child
	for i, hop := range rule.hops {
		targetCol := hop.column
		if i == len(rule.hops)-1 {
			targetCol = hop.key
		}
		inner := "SELECT " + targetCol + " FROM " + hop.parent + " WHERE " + hop.key + " = " + value
		if i < len(rule.hops)-1 {
			inner = "(" + inner + ")"
		}
		value = inner
	}
	return "(" + value + ")"
}
