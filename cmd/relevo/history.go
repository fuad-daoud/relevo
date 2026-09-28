package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// historyJSONRow is one `relevo history --json` row: db.RoundRow's fields
// plus the candidate's short name beside the token (A1 §4.4). The embedded
// struct keeps every token field where it always was.
type historyJSONRow struct {
	db.RoundRow
	BuilderName string `json:"BuilderName,omitempty"`
}

// historyJSONRows wraps each row with its candidate's short name as set
// resolves it. A token the set no longer holds stays as the name, exactly as
// the text listing renders it (Set.NameOf never errors).
func historyJSONRows(rows []db.RoundRow, set *candidate.Set) []historyJSONRow {
	out := make([]historyJSONRow, len(rows))
	for i, r := range rows {
		row := historyJSONRow{RoundRow: r}
		if r.Candidate != nil {
			row.BuilderName = set.NameOf(*r.Candidate)
		}
		out[i] = row
	}
	return out
}

const historyUsage = `usage: relevo history [--here] [--binding <name>] [--feature <label>] [--ticket <ref>]
                     [--mastermind <session>] [--since <window>] [--limit <n>] [--by <axis>]
                     [-q "<query>"] [--json] [--rows]`

// groupJSON shapes the groups `--json --by` prints: their Rows are blanked
// unless --rows asked for them. A nil list encodes as [], not null.
func groupJSON(groups []histq.GroupRow, withRows bool) []histq.GroupRow {
	if groups == nil {
		groups = []histq.GroupRow{}
	}
	if withRows {
		return groups
	}
	out := make([]histq.GroupRow, len(groups))
	for i, g := range groups {
		g.Rows = nil
		out[i] = g
	}
	return out
}

// cmdHistory prints history as JSON across every binding relevo has ever
// recorded, live or archived, newest first.
func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	here := fs.Bool("here", false, "filter to the repo the current directory belongs to")
	binding := fs.String("binding", "", "filter to this binding name")
	feature := fs.String("feature", "", "filter to this feature label")
	ticket := fs.String("ticket", "", "filter by ticket: N or owner/repo#N")
	mastermind := fs.String("mastermind", "", "filter to this mastermind session id")
	since := fs.String("since", "", "only rounds started after this: 24h, 7d, or YYYY-MM-DD")
	limit := fs.Int("limit", 200, "max rows to print; 0 = all")
	by := fs.String("by", "", "regroup the rows by an axis, e.g. ticket")
	_ = fs.Bool("json", false, "machine-readable output: a JSON array of RoundRow")
	query := fs.String("q", "", "a query: harness:agy outcome:halted since:30d cost>1 by:candidate")
	withRows := fs.Bool("rows", false, "with grouped JSON, include each group's rows")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), historyUsage)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, historyUsage)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	d, err := openDB(rt.Store.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo history: %v\n", err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()
	rt.DB = d

	opts := relevo.HistoryOptions{
		Binding:    *binding,
		Feature:    *feature,
		Ticket:     *ticket,
		MasterMind: *mastermind,
		Names:      rt.Candidates,
		Since:      *since,
		Limit:      *limit,
		Query:      *query,
		By:         *by,
	}
	if *here {
		cwd, cerr := os.Getwd()
		if cerr != nil {
			return cerr
		}
		opts.Here = cwd
	}

	f, notes, ferr := opts.Filter(context.Background(), rt, time.Now())
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "relevo history: %v\n", ferr)
		var eq histq.ErrQuery
		if errors.As(ferr, &eq) {
			return exitCodeErr{code: 2}
		}
		return exitCodeErr{code: 1}
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	rows, qerr := rt.DB.Query(f)
	if qerr != nil {
		return qerr
	}

	parsed := opts.ParsedQuery()
	rows = parsed.Apply(rows)

	if parsed.By != histq.AxisNone {
		groups := histq.Group(rows, parsed.By, time.Local)
		return json.NewEncoder(os.Stdout).Encode(groupJSON(groups, *withRows))
	}

	if rows == nil {
		rows = []db.RoundRow{}
	}
	return json.NewEncoder(os.Stdout).Encode(historyJSONRows(rows, rt.Candidates))
}
