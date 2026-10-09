package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

type dbRelabelFlagValues struct {
	file, binding, feature *string
	overwrite, dryRun      *bool
	asJSON                 *bool
}

// dbRelabelOwnFlags declares the flags db relabel adds to the ones its db
// siblings already declare: --dry-run belongs to db rename-repo's set and
// --json to db query's, and a flag set cannot declare either twice.
func dbRelabelOwnFlags(fs *flag.FlagSet) *dbRelabelFlagValues {
	v := &dbRelabelFlagValues{}
	v.file = fs.String("file", "", "a file of <id><TAB><label> lines")
	v.binding = fs.String("binding", "", "the binding id to label (with --feature)")
	v.feature = fs.String("feature", "", "the label for --binding")
	v.overwrite = fs.Bool("overwrite", false, "replace a label a binding already carries")
	return v
}

func dbRelabelFlagSet(fs *flag.FlagSet) *dbRelabelFlagValues {
	v := dbRelabelOwnFlags(fs)
	v.dryRun = fs.Bool("dry-run", false, "report the counts and the bindings that would change, and write nothing")
	v.asJSON = fs.Bool("json", false, "print the counts as a JSON document")
	return v
}

func cmdDBRelabel(args []string) error {
	return outcomeError(dbRelabelRun(args))
}

func dbRelabelRun(args []string) error {
	fs := flag.NewFlagSet("db relabel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbRelabelFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db relabel takes no arguments, got %d", fs.NArg())
	}
	entries, err := relabelEntries(*v.file, *v.binding, *v.feature, fs)
	if err != nil {
		return err
	}
	d, err := openDB(machineDBPath())
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	counts, err := d.Relabel(db.RelabelParams{Entries: entries, Overwrite: *v.overwrite, DryRun: *v.dryRun})
	if errors.Is(err, db.ErrRelabelRefused) {
		return failWrap(codeConflict, err, "%v", err)
	}
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(counts)
	}
	printRelabelCounts(counts)
	return nil
}

// relabelEntries resolves the one source the flags name into entries; every
// refusal fires before the database is opened.
func relabelEntries(file, binding, feature string, fs *flag.FlagSet) ([]db.RelabelEntry, error) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case set["file"] && (set["binding"] || set["feature"]):
		return nil, fail(codeUsage, "relevo db relabel wants --file or --binding with --feature, not both")
	case set["file"]:
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, failWrap(codeUsage, err, "relevo db relabel: read %s: %v", file, err)
		}
		return parseRelabelFile(string(data))
	case set["binding"] && set["feature"]:
		if binding == "" {
			return nil, fail(codeUsage, "relevo db relabel: --binding is empty")
		}
		if err := store.ValidFeature(feature); err != nil {
			return nil, failWrap(codeUsage, err, "relevo db relabel: %v", err)
		}
		return []db.RelabelEntry{{ID: binding, Feature: feature, Line: 1}}, nil
	}
	return nil, fail(codeUsage, "relevo db relabel wants --file <tsv>, or --binding <id> with --feature <label>")
}

// parseRelabelFile reads <id><TAB><label> lines. It collects every offending
// line into one refusal rather than stopping at the first.
func parseRelabelFile(data string) ([]db.RelabelEntry, error) {
	var entries []db.RelabelEntry
	var problems []string
	firstLine := map[string]int{}
	index := map[string]int{}
	for i, line := range strings.Split(data, "\n") {
		n := i + 1
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, label, ok := strings.Cut(line, "\t")
		switch {
		case !ok || id == "":
			problems = append(problems, fmt.Sprintf("line %d: want <id><TAB><label>", n))
			continue
		case id == "id" && label == "feature":
			continue
		}
		if err := store.ValidFeature(label); err != nil {
			problems = append(problems, fmt.Sprintf("line %d: %v", n, err))
			continue
		}
		if at, seen := index[id]; seen {
			if entries[at].Feature != label {
				problems = append(problems, fmt.Sprintf("line %d: id %s already labelled %q on line %d", n, id, entries[at].Feature, firstLine[id]))
			}
			continue
		}
		index[id], firstLine[id] = len(entries), n
		entries = append(entries, db.RelabelEntry{ID: id, Feature: label, Line: n})
	}
	if len(problems) > 0 {
		return nil, fail(codeUsage, "relevo db relabel: bad file: %s", strings.Join(problems, "; "))
	}
	if len(entries) == 0 {
		return nil, fail(codeUsage, "relevo db relabel: the file names no binding")
	}
	return entries, nil
}

func printRelabelCounts(c db.RelabelCounts) {
	fmt.Printf("dry_run: %t\nrequested: %d\nlabelled: %d\nunchanged: %d\nskipped_labelled: %d\nrecords_rewritten: %d\n",
		c.DryRun, c.Requested, c.Labelled, c.Unchanged, c.SkippedLabelled, c.RecordsRewritten)
	if !c.DryRun {
		return
	}
	for _, ch := range c.Changes {
		old := ch.Old
		if old == "" {
			old = "-"
		}
		fmt.Printf("%s %s %s -> %s\n", ch.ID, ch.Name, old, ch.New)
	}
}
