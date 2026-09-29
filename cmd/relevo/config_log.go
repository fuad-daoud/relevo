package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
)

// revisionJSON is the --json shape of one revision (§6.4). Snapshot is present
// only for `--rev`, where it is the raw snapshot object.
type revisionJSON struct {
	Rev      int64           `json:"rev"`
	At       string          `json:"at"`
	Source   string          `json:"source"`
	Message  string          `json:"message"`
	Version  int64           `json:"version"`
	Changes  json.RawMessage `json:"changes"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// revChangeList decodes a revision row's raw change list; a row whose changes
// do not parse reads as no changes rather than failing the whole listing.
func revChangeList(r db.RevisionRow) []config.Change {
	var cs []config.Change
	if err := json.Unmarshal(r.Changes, &cs); err != nil {
		return nil
	}
	return cs
}

func revisionJSONOf(r db.RevisionRow, withSnapshot bool) revisionJSON {
	changes := r.Changes
	if !json.Valid(changes) {
		changes = json.RawMessage("[]")
	}
	out := revisionJSON{
		Rev:     r.Rev,
		At:      r.At.UTC().Format(time.RFC3339),
		Source:  r.Source,
		Message: r.Message,
		Version: r.Version,
		Changes: changes,
	}
	if withSnapshot {
		snapshot := r.Snapshot
		if !json.Valid(snapshot) {
			snapshot = json.RawMessage("{}")
		}
		out.Snapshot = snapshot
	}
	return out
}

// printRevisionText prints one revision's header, its message when it has one,
// and one Describe line per change indented by two spaces.
func printRevisionText(r db.RevisionRow) error {
	fmt.Printf("#%d  %s  %s  config version %d\n",
		r.Rev, r.At.Local().Format("2006-01-02 15:04"), r.Source, r.Version)
	if r.Message != "" {
		fmt.Printf("message: %s\n", r.Message)
	}
	if r.Source == "baseline" {
		fmt.Println("  (baseline: the config as it was before revisions were recorded)")
		return nil
	}
	for _, c := range revChangeList(r) {
		fmt.Printf("  %s\n", config.Describe(c))
	}
	return nil
}

// cmdConfigLog lists revisions newest first, or shows one with --rev. The list
// is a header line per revision; --rev adds the message and every change.
// configLogFlagValues holds the pointers `config log` parses into.
type configLogFlagValues struct {
	n      *int
	rev    *int64
	asJSON *bool
}

// configLogFlagSet defines those flags on fs and returns what they parse into.
func configLogFlagSet(fs *flag.FlagSet) *configLogFlagValues {
	v := &configLogFlagValues{}
	v.n = fs.Int("n", 20, "how many revisions to list")
	v.rev = fs.Int64("rev", 0, "show one revision: its header and changes")
	v.asJSON = fs.Bool("json", false, "print JSON")
	return v
}

func cmdConfigLog(args []string) error {
	fs := flag.NewFlagSet("relevo config log", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configLogFlagSet(fs)
	n, rev, asJSON := v.n, v.rev, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fail(codeUsage, "config log takes no arguments, got %v", fs.Args())
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if *rev > 0 {
		r, err := rt.Config.Revision(*rev)
		if err != nil {
			if errors.Is(err, config.ErrNoRevision) {
				return fail(codeRevisionNotFound, "no such revision #%d", *rev)
			}
			return fail(codeInternal, "%v", err)
		}
		if *asJSON {
			out, err := json.Marshal(revisionJSONOf(r, true))
			if err != nil {
				return err
			}
			return printJSON(os.Stdout, out)
		}
		return printRevisionText(r)
	}

	rows, err := rt.Config.Log(*n)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if *asJSON {
		list := make([]revisionJSON, 0, len(rows))
		for _, r := range rows {
			list = append(list, revisionJSONOf(r, false))
		}
		out, err := json.Marshal(list)
		if err != nil {
			return err
		}
		return printJSON(os.Stdout, out)
	}
	if len(rows) == 0 {
		fmt.Println("no revisions yet")
		return nil
	}
	for _, r := range rows {
		fmt.Printf("#%d  %s  %-9s  %d change(s)  %s\n",
			r.Rev, r.At.Local().Format("2006-01-02 15:04"), r.Source, len(revChangeList(r)), r.Message)
	}
	return nil
}

// cmdConfigRollback prints what rolling back to rev would change, confirms
// with the user unless --yes, then writes the rollback as one new revision.
// configRollbackFlagValues holds the pointers `config rollback` parses into.
type configRollbackFlagValues struct {
	yes *bool
	msg *string
}

// configRollbackFlagSet defines those flags on fs and returns what they parse
// into.
func configRollbackFlagSet(fs *flag.FlagSet) *configRollbackFlagValues {
	v := &configRollbackFlagValues{}
	v.yes = fs.Bool("yes", false, "skip the confirmation prompt")
	v.msg = fs.String("m", "", "message for the rollback revision")
	return v
}

func cmdConfigRollback(args []string) error {
	fs := flag.NewFlagSet("relevo config rollback", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configRollbackFlagSet(fs)
	yes, msg := v.yes, v.msg
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config rollback <rev> [--yes] [-m <message>]")
		return exitCodeErr{code: 2}
	}
	rev, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil || rev <= 0 {
		fmt.Fprintf(os.Stderr, "relevo config rollback: not a revision number: %q\n", rest[0])
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	plan, err := rt.Config.RollbackPlan(rev)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrNoRevision):
			return fmt.Errorf("no such revision #%d", rev)
		case errors.Is(err, config.ErrNoChange):
			fmt.Printf("config already equals revision #%d\n", rev)
			return nil
		default:
			return err
		}
	}

	fmt.Printf("rollback to #%d would change:\n", rev)
	for _, c := range plan {
		fmt.Println(config.Describe(c))
	}

	if !*yes {
		if !stdinIsTerminal() {
			fmt.Fprintln(os.Stderr, "relevo: config rollback needs a terminal to confirm; pass --yes")
			return exitCodeErr{code: 2}
		}
		fmt.Printf("Roll back to #%d? [y/N] ", rev)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			fmt.Println("nothing changed")
			return exitCodeErr{code: 1}
		}
	}

	row, err := rt.Config.As("rollback", *msg).Rollback(rev)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrNoRevision):
			return fmt.Errorf("no such revision #%d", rev)
		case errors.Is(err, config.ErrNoChange):
			fmt.Printf("config already equals revision #%d\n", rev)
			return nil
		default:
			return err
		}
	}
	fmt.Printf("rolled back to #%d as #%d (config version %d)\n", rev, row.Rev, row.Version)
	return nil
}
