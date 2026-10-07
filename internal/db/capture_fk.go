//go:build !modernc

package db

// The order a change-set rewrite has to be recorded in, and the grouping that
// order needs.
//
// A rewrite is recorded as the engine records it: the row is deleted and
// inserted again. The remote applies those changes in the change set's own
// order, one statement at a time, with its foreign keys enforced -- so the order
// the backfill records in is the order the remote has to be able to apply:
//
//   - the delete of a row the remote still has children for is refused, so every
//     delete must come after the deletes of the tables that reference the row;
//   - the insert of a row whose parent the remote does not hold is refused, so
//     every insert must come after the inserts of the tables the row references.
//
// One table order cannot serve both: the deletes want the children first and the
// inserts want the parents first. So a group is recorded as one transaction that
// writes every delete of the group before any insert of it, and a group is
// closed over the references between its tables -- a table whose rows reference
// another table's is in the same group as that table -- so the rows the remote
// holds outside the group are never the ones that refuse.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// CaptureGroup is one foreign-key-closed set of tables, in the order a rewrite
// must record its inserts.
type CaptureGroup struct {
	// Key names the group for a caller that records its own progress. It is the
	// group's tables joined by a comma, which is stable because Tables is.
	Key string
	// Tables are the group's tables, every one of them after every table it
	// references.
	Tables []string
}

// CaptureGroups is the file's application tables as foreign-key-closed groups,
// each ordered parents first. A table that references itself is one table: the
// reference is between rows of it, which the rewrite orders as it goes.
//
// The order within a group is a topological sort of the references, with the
// alphabetically first ready table taken first so the answer is the same answer
// on every run. A cycle -- which a table set with mutual references would make
// -- is broken at the alphabetically first table on it, because a walk that
// refuses to order is a walk that never runs.
func CaptureGroups(ctx context.Context, conn *sql.Conn) ([]CaptureGroup, error) {
	tables, err := CaptureTables(ctx, conn)
	if err != nil {
		return nil, err
	}
	parents, err := captureParents(ctx, conn, tables)
	if err != nil {
		return nil, err
	}
	return groupTables(tables, parents), nil
}

// captureParents is the reference graph over the tables themselves: for each
// table, the tables its foreign keys name, with the driver's own tables and the
// table itself left out. A table the walk does not cover cannot refuse anything,
// so a reference to one is not an edge.
func captureParents(ctx context.Context, conn *sql.Conn, tables []string) (map[string][]string, error) {
	known := make(map[string]bool, len(tables))
	for _, table := range tables {
		known[table] = true
	}
	parents := make(map[string][]string, len(tables))
	for _, table := range tables {
		// pragma_foreign_key_list names the parent table "table", the child
		// column "from" and the parent column "to", which is the shape every
		// other reader of this pragma in the tree uses.
		rows, err := conn.QueryContext(ctx,
			`SELECT "table" FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			return nil, fmt.Errorf("db: %s: read the references: %w", table, mapBusy(err))
		}
		seen := map[string]bool{}
		for rows.Next() {
			var parent sql.NullString
			if err := rows.Scan(&parent); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("db: %s: read the references: %w", table, err)
			}
			name := parent.String
			if name == table || !known[name] || seen[name] {
				continue
			}
			seen[name] = true
			parents[table] = append(parents[table], name)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("db: %s: read the references: %w", table, mapBusy(err))
		}
		sort.Strings(parents[table])
	}
	return parents, nil
}

// groupTables is the connected components of the reference graph taken in both
// directions -- two tables that reference each other are one group, because a
// delete of either is refused while the other exists -- each sorted parents
// first.
func groupTables(tables []string, parents map[string][]string) []CaptureGroup {
	// The undirected adjacency: a reference joins its two tables whichever way
	// round it points.
	joined := make(map[string]bool)
	adjacent := make(map[string][]string, len(tables))
	for _, table := range tables {
		for _, parent := range parents[table] {
			edge := table + "\x00" + parent
			if joined[edge] || edge == parent+"\x00"+table {
				continue
			}
			joined[edge] = true
			adjacent[table] = append(adjacent[table], parent)
			adjacent[parent] = append(adjacent[parent], table)
		}
	}

	seen := make(map[string]bool, len(tables))
	var groups []CaptureGroup
	for _, table := range tables {
		if seen[table] {
			continue
		}
		// The component, walked from this table. Tables come out of the walk in
		// no order, so the sort below is what makes the group the same group on
		// every run.
		var members []string
		queue := []string{table}
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			if seen[next] {
				continue
			}
			seen[next] = true
			members = append(members, next)
			queue = append(queue, adjacent[next]...)
		}
		sort.Strings(members)
		groups = append(groups, CaptureGroup{
			Key:    strings.Join(members, ","),
			Tables: parentsFirst(members, parents),
		})
	}
	return groups
}

// parentsFirst is members ordered so that no table comes before a table it
// references. Kahn's algorithm over the references within members, taking the
// alphabetically first ready table each round, with a table on a cycle taken as
// ready so the sort terminates.
func parentsFirst(members []string, parents map[string][]string) []string {
	within := make(map[string]bool, len(members))
	for _, table := range members {
		within[table] = true
	}
	waiting := make(map[string][]string, len(members)) // table -> the members it waits for
	for _, table := range members {
		for _, parent := range parents[table] {
			if within[parent] {
				waiting[table] = append(waiting[table], parent)
			}
		}
		sort.Strings(waiting[table])
	}

	ordered := make([]string, 0, len(members))
	placed := make(map[string]bool, len(members))
	for len(ordered) < len(members) {
		ready := ""
		for _, table := range members {
			if placed[table] {
				continue
			}
			if allPlaced(waiting[table], placed) {
				ready = table
				break
			}
		}
		if ready == "" {
			// Every table left is waiting on another that is left, so the
			// references run in a cycle. The alphabetically first of them is
			// taken, which keeps the walk running and the order reproducible.
			for _, table := range members {
				if !placed[table] {
					ready = table
					break
				}
			}
		}
		ordered = append(ordered, ready)
		placed[ready] = true
	}
	return ordered
}

// allPlaced reports whether every table in wait has been ordered already.
func allPlaced(wait []string, placed map[string]bool) bool {
	for _, table := range wait {
		if !placed[table] {
			return false
		}
	}
	return true
}
