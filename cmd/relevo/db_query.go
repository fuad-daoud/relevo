package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// dbUsage is what a bare `relevo db` prints: the verb only dispatches
// subcommands, and query is the one there is.
const dbUsage = "usage: relevo db query '<SQL>' [--json]\n"

// dbFlagSet declares no flags: `relevo db` is a dispatcher, and every flag
// lives on its subcommands.
func dbFlagSet(*flag.FlagSet) {}

// dbQueryFlagValues holds the pointer db query parses into.
type dbQueryFlagValues struct {
	asJSON *bool
}

// dbQueryFlagSet defines --json on fs and returns what it parses into.
func dbQueryFlagSet(fs *flag.FlagSet) *dbQueryFlagValues {
	v := &dbQueryFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the rows as a JSON document")
	return v
}

// cmdDB dispatches `relevo db`'s subcommands. A bare `relevo db` prints the
// usage line, because the verb itself has nothing to run.
func cmdDB(args []string) error {
	switch {
	case len(args) > 0 && args[0] == "query":
		return cmdDBQuery(args[1:])
	}
	fmt.Fprint(os.Stderr, dbUsage)
	return errUsagePrinted
}

// cmdDBQuery runs one read-only SQL statement against relevo.db and prints its
// rows: a tabwriter table by default, and the columns/rows document under
// --json. It is the supported replacement for `sqlite3 relevo.db`, which the
// daemon's lock keeps out while the daemon runs.
func cmdDBQuery(args []string) error {
	fs := flag.NewFlagSet("db query", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbQueryFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	positional := fs.Args()
	if len(positional) != 1 {
		return fail(codeUsage, "relevo db query wants exactly one SQL statement, got %d arguments", len(positional))
	}

	d, err := openDBQuery()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	var (
		columns []string
		rows    [][]any
	)
	err = d.QueryReadOnly(context.Background(), positional[0], func(cols []string, values []any) error {
		columns = cols
		rows = append(rows, values)
		return nil
	})
	if err != nil {
		return classifyDBQuery(err)
	}

	if *v.asJSON {
		return writeDBQueryJSON(os.Stdout, columns, rows)
	}
	return writeDBQueryTable(os.Stdout, columns, rows)
}

// classifyDBQuery maps the read-only seam's failure to the frame's codes: a
// query argument that is not one statement is a usage error, and anything the
// seam or the engine refused -- a disallowed keyword, a write, a syntax error
// -- is refused with the engine's own message.
func classifyDBQuery(err error) error {
	if errors.Is(err, db.ErrNotOneStatement) || errors.Is(err, db.ErrPragmaNotReadOnly) {
		return failNext(codeUsage, "relevo help", "%v", err)
	}
	return failWrap(codeRefused, err, "%v", err)
}

// openDBQuery reaches relevo.db read-only: the owner when its socket answers,
// the file itself when it does not, and one re-dial when the direct open finds
// the daemon holding the file. It never starts the owner.
func openDBQuery() (*db.DB, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "relevo.db")
	if !fileExists(path) {
		return nil, fail(codeRefused, "no relevo.db at %s", path)
	}

	if d, derr := dialOwner(root, verbDialBudget); derr == nil {
		return d, nil
	}
	d, err := openReadOnlyDB(path, db.Options{})
	if err == nil {
		return d, nil
	}
	if !errors.Is(err, db.ErrLocked) {
		return nil, failWrap(codeInternal, err, "open %s", path)
	}
	// The file is held, so the owner is the only reader. One more dial covers a
	// daemon that bound its socket between the first attempt and the open;
	// after that the file stays out of reach.
	if d, derr := dialOwner(root, verbDialBudget); derr == nil {
		return d, nil
	}
	return nil, failWrap(codeConflict, err, "relevo.db is held by the daemon and the owner did not answer")
}

// dbQueryDoc is the --json shape: the statement's columns and one array of
// values per row, in the order the engine returned them.
type dbQueryDoc struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// writeDBQueryJSON prints the document, with empty arrays rather than nulls for
// a statement that returned no rows.
func writeDBQueryJSON(w io.Writer, columns []string, rows [][]any) error {
	if columns == nil {
		columns = []string{}
	}
	if rows == nil {
		rows = [][]any{}
	}
	return json.NewEncoder(w).Encode(dbQueryDoc{Columns: columns, Rows: rows})
}

// writeDBQueryTable prints one tabwriter row per record, the header first, so
// the output lines up in a terminal the way sqlite3's did.
func writeDBQueryTable(w io.Writer, columns []string, rows [][]any) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(columns) > 0 {
		fmt.Fprintln(tw, strings.Join(columns, "\t"))
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, value := range row {
			cells[i] = formatDBQueryValue(value)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

// formatDBQueryValue renders one table cell: NULL for a nil, a byte count for a
// blob, and the value's own text otherwise.
func formatDBQueryValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case []byte:
		return fmt.Sprintf("<blob %d bytes>", len(v))
	default:
		return fmt.Sprint(v)
	}
}
