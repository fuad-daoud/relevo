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
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/store"
)

// dbUsage is what a bare `relevo db` prints: the verb only dispatches
// subcommands, and query is the one there is.
const dbUsage = "usage: relevo db query '<SQL>' [--json] [--limit N] [--timeout D] [--max-bytes N]\n\n" +
	"One read-only statement: SELECT, WITH or a read-only PRAGMA. RECURSIVE is\n" +
	"refused as a cheap first line, not as the guarantee: SQLite decides recursion\n" +
	"structurally, so a runaway statement is ended by the owner, which refuses\n" +
	"ad-hoc reads and reaps the daemon. --limit caps the rows printed (default\n" +
	"1000); --max-bytes caps the value bytes held (default 16777216, 16 MiB);\n" +
	"--timeout bounds dial, open and read (default 10s, at most 12s).\n"

// The db query defaults: a row cap and a byte cap that keep a broad SELECT
// from filling memory, and a hold budget that leaves the owner's own open-lock
// wait a margin.
const (
	dbQueryDefaultLimit    = 1000
	dbQueryDefaultTimeout  = 10 * time.Second
	dbQueryDefaultMaxBytes = 16 * 1024 * 1024
)

// errQueryLimitReached stops the read once the row cap or the byte cap is hit.
// cmdDBQuery treats it as a normal truncation, not a failure: it is the onRow
// stop signal, not an engine error.
var errQueryLimitReached = errors.New("db query: row limit reached")

// dbQueryRead is the read cmdDBQuery runs. It is a var so a test can replace it
// with a read that ignores ctx, proving the deadline ends the command even when
// the engine does not.
var dbQueryRead = func(ctx context.Context, d *db.DB, stmt string, onRow func([]string, []any) error) error {
	return d.QueryReadOnly(ctx, stmt, onRow)
}

// dbQueryOut is where db query prints its rows, resolved to os.Stdout at call
// time when nil. It is a seam so a test can capture the print, or stall it to
// prove the handle is already closed by then.
var dbQueryOut io.Writer

// dbFlagSet declares the flags `relevo db`'s subcommands take. The verb itself
// is a dispatcher and parses none, but the registry lists the flags a caller
// reaches through it, and the parity test requires the installer to declare
// exactly those.
func dbFlagSet(fs *flag.FlagSet) {
	dbQueryFlagSet(fs)
}

// dbQueryFlagValues holds the pointers db query parses into.
type dbQueryFlagValues struct {
	asJSON   *bool
	limit    *int
	timeout  *time.Duration
	maxBytes *int
}

// dbQueryFlagSet defines the flags on fs and returns what they parse into.
func dbQueryFlagSet(fs *flag.FlagSet) *dbQueryFlagValues {
	v := &dbQueryFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the rows as a JSON document")
	v.limit = fs.Int("limit", dbQueryDefaultLimit, "print at most this many rows")
	v.timeout = fs.Duration("timeout", dbQueryDefaultTimeout, "bound the dial, open and read")
	v.maxBytes = fs.Int("max-bytes", dbQueryDefaultMaxBytes, "print at most this many value bytes")
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
	if err := checkDBQueryBounds(*v.limit, *v.timeout, *v.maxBytes); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *v.timeout)
	defer cancel()
	if ctx.Err() != nil {
		// The deadline was already past before anything ran: refuse rather than
		// fall through to an open the caller's budget no longer covers.
		return dbQueryTimeout(*v.timeout)
	}

	d, err := openDBQuery(ctx)
	if err != nil {
		return err
	}
	// closed records that the handle is already released, so the deferred close
	// does not run a second time.
	closed := false
	defer func() {
		if !closed {
			_ = d.Close()
		}
	}()

	var (
		columns   []string
		rows      [][]any
		rowsCut   bool
		bytesCut  bool
		bytesHeld int
	)
	onRow := func(cols []string, values []any) error {
		columns = cols
		if len(rows) >= *v.limit {
			// One extra row proves another follows, so the cut is real rather
			// than the statement returning exactly --limit rows.
			rowsCut = true
			return errQueryLimitReached
		}
		add := dbQueryRowBytes(values)
		if bytesHeld+add > *v.maxBytes {
			// The row that would pass the budget is not appended at all, so
			// one value larger than the whole budget stops the read before it
			// is ever held.
			bytesCut = true
			return errQueryLimitReached
		}
		bytesHeld += add
		rows = append(rows, values)
		return nil
	}

	// The read runs on its own goroutine so the deadline ends the command even
	// when the engine ignores ctx inside a step. main's os.Exit then ends the
	// process, and with it the direct flock the abandoned read still holds.
	readErr := make(chan error, 1)
	go func() { readErr <- dbQueryRead(ctx, d, positional[0], onRow) }()

	select {
	case rerr := <-readErr:
		if rerr != nil && !errors.Is(rerr, errQueryLimitReached) {
			if ctx.Err() != nil {
				return dbQueryTimeout(*v.timeout)
			}
			return classifyDBQuery(rerr)
		}
	case <-ctx.Done():
		return dbQueryTimeout(*v.timeout)
	}

	// Release the file before the note and the rows go out: a stalled stdout --
	// a pipe nobody drains, `| less` then Ctrl-Z -- must not hold relevo.db.lock
	// through the print, or the read-vs-open budget would not cover this phase.
	_ = d.Close()
	closed = true

	if rowsCut {
		fmt.Fprintf(os.Stderr, "truncated at %d rows (raise --limit)\n", *v.limit)
	}
	if bytesCut {
		fmt.Fprintf(os.Stderr, "truncated at %d bytes (raise --max-bytes)\n", *v.maxBytes)
	}

	out := dbQueryOut
	if out == nil {
		out = os.Stdout
	}
	if *v.asJSON {
		return writeDBQueryJSON(out, columns, rows)
	}
	return writeDBQueryTable(out, columns, rows)
}

// checkDBQueryBounds refuses a limit or byte cap below one and a timeout that
// is not positive or exceeds ReadOnlyHoldBudget. The open-lock wait outlasts
// the budget, so a longer deadline could hold the lock past any fixed wait.
func checkDBQueryBounds(limit int, timeout time.Duration, maxBytes int) error {
	switch {
	case limit < 1:
		return fail(codeUsage, "relevo db query --limit must be at least 1, got %d", limit)
	case maxBytes < 1:
		return fail(codeUsage, "relevo db query --max-bytes must be at least 1, got %d", maxBytes)
	case timeout <= 0:
		return fail(codeUsage, "relevo db query --timeout must be positive, got %s", timeout)
	case timeout > db.ReadOnlyHoldBudget:
		return fail(codeUsage, "relevo db query --timeout must be at most %s, got %s", db.ReadOnlyHoldBudget, timeout)
	}
	return nil
}

// dbQueryRowBytes is one row's weight against --max-bytes: the length of every
// blob and string, and a small constant for the rest, which carry no length of
// their own once the row is held in memory.
func dbQueryRowBytes(values []any) int {
	const otherValueBytes = 8
	n := 0
	for _, value := range values {
		switch t := value.(type) {
		case []byte:
			n += len(t)
		case string:
			n += len(t)
		default:
			n += otherValueBytes
		}
	}
	return n
}

// dbQueryTimeout is the refusal a passed deadline produces: the statement did
// not finish in time, and no fallback to a direct open follows.
func dbQueryTimeout(timeout time.Duration) error {
	return fail(codeRefused, "relevo db query: the statement did not finish within %s", timeout)
}

// classifyDBQuery maps the read-only seam's failure to the frame's codes: a
// query argument that is not one statement is a usage error, and anything the
// seam or the engine refused -- a disallowed keyword, a write, a syntax error
// -- is refused with the engine's own message.
func classifyDBQuery(err error) error {
	if errors.Is(err, db.ErrNotOneStatement) || errors.Is(err, db.ErrPragmaNotReadOnly) || errors.Is(err, db.ErrRecursive) {
		return failNext(codeUsage, "relevo help", "%v", err)
	}
	return failWrap(codeRefused, err, "%v", err)
}

// openDBQuery reaches relevo.db read-only: the owner when its socket answers,
// the file itself when it does not, and one re-dial when the direct open finds
// the daemon holding the file. It never starts the owner. ctx bounds the dial,
// and an expired ctx refuses rather than falling back to a direct open.
func openDBQuery(ctx context.Context) (*db.DB, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "relevo.db")
	if !fileExists(path) {
		return nil, fail(codeRefused, "no relevo.db at %s", path)
	}

	if d, derr := dialOwnerAdHoc(ctx, root, verbDialBudget); derr == nil {
		return d, nil
	} else if ctx.Err() != nil {
		return nil, failWrap(codeRefused, ctx.Err(), "relevo db query: the deadline passed before the owner answered")
	}
	d, err := openReadOnlyDB(path, db.Options{})
	if err == nil {
		return d, nil
	}
	if errors.Is(err, db.ErrNotConverted) {
		return nil, failWrap(codeRefused, err, "open %s", path)
	}
	if !errors.Is(err, db.ErrLocked) {
		return nil, failWrap(codeInternal, err, "open %s", path)
	}
	// The file is held, so the owner is the only reader. One more dial covers a
	// daemon that bound its socket between the first attempt and the open;
	// after that the file stays out of reach.
	if d, derr := dialOwnerAdHoc(ctx, root, verbDialBudget); derr == nil {
		return d, nil
	}
	if sock, sockErr := ownerSocket(root); sockErr != nil {
		return nil, failWrap(codeConflict, err, "relevo.db is locked (%s) and no owner socket is available: %v", path+".lock", sockErr)
	} else {
		return nil, failWrap(codeConflict, err, "relevo.db is locked (%s) and the owner socket at %s did not answer", path+".lock", sock)
	}
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
// the output lines up in a terminal the way sqlite3's did. Every header cell and
// every value cell is sanitised: a value or a column name in relevo.db is
// untrusted text, and a raw ESC would let it manipulate the terminal. --json
// stays the machine form and is not sanitised.
func writeDBQueryTable(w io.Writer, columns []string, rows [][]any) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(columns) > 0 {
		header := make([]string, len(columns))
		for i, column := range columns {
			header[i] = sanitize.Text(column)
		}
		fmt.Fprintln(tw, strings.Join(header, "\t"))
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, value := range row {
			cells[i] = sanitize.Text(formatDBQueryValue(value))
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
