package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
)

// adoptSlice collects the repeatable --adopt-cwd flag.
type adoptSlice []string

func (a *adoptSlice) String() string { return fmt.Sprint([]string(*a)) }

func (a *adoptSlice) Set(val string) error {
	*a = append(*a, val)
	return nil
}

type dbRenameRepoFlagValues struct {
	from, to *string
	adopt    *adoptSlice
	dryRun   *bool
	asJSON   *bool
}

// dbRenameRepoOwnFlags declares the flags db rename-repo adds to --json, so the
// db dispatcher can list them beside db query's without declaring --json twice.
func dbRenameRepoOwnFlags(fs *flag.FlagSet) *dbRenameRepoFlagValues {
	v := &dbRenameRepoFlagValues{adopt: &adoptSlice{}}
	v.from = fs.String("from", "", "the old origin URL")
	v.to = fs.String("to", "", "the new origin URL")
	fs.Var(v.adopt, "adopt-cwd", "adopt repo-less bindings whose cwd is under this path (repeatable)")
	v.dryRun = fs.Bool("dry-run", false, "report the counts without writing")
	return v
}

func dbRenameRepoFlagSet(fs *flag.FlagSet) *dbRenameRepoFlagValues {
	v := dbRenameRepoOwnFlags(fs)
	v.asJSON = fs.Bool("json", false, "print the counts as a JSON document")
	return v
}

func cmdDBRenameRepo(args []string) error {
	return outcomeError(dbRenameRepoRun(args))
}

func dbRenameRepoRun(args []string) error {
	fs := flag.NewFlagSet("db rename-repo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbRenameRepoFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db rename-repo takes no arguments, got %d", fs.NArg())
	}
	params, err := renameRepoParams(*v.from, *v.to, *v.adopt, *v.dryRun)
	if err != nil {
		return err
	}
	d, err := openDB(machineDBPath())
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	counts, err := d.RenameRepo(params)
	if errors.Is(err, db.ErrNothingToRename) {
		return failWrap(codeConflict, err, "nothing to rename: no repo row holds %s or %s", params.FromURL, params.ToURL)
	}
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(counts)
	}
	printRenameCounts(counts)
	return nil
}

func renameRepoParams(from, to string, adopt []string, dryRun bool) (db.RenameRepoParams, error) {
	if from == "" || to == "" {
		return db.RenameRepoParams{}, fail(codeUsage, "relevo db rename-repo wants --from <url> and --to <url>")
	}
	p := db.RenameRepoParams{
		FromURL: git.NormalizeOriginURL(from), ToURL: git.NormalizeOriginURL(to),
		AdoptCWD: adopt, DryRun: dryRun,
	}
	if p.FromURL == p.ToURL {
		return db.RenameRepoParams{}, fail(codeUsage, "relevo db rename-repo: --from and --to are the same URL after normalisation")
	}
	p.FromOwnerRepo, p.ToOwnerRepo = git.OwnerRepo(p.FromURL), git.OwnerRepo(p.ToURL)
	return p, nil
}

func printRenameCounts(c db.RenameRepoCounts) {
	fmt.Printf("dry_run: %t\nfrom: %s\nto: %s\nrepos_merged: %d\nrepos_renamed: %d\nbindings_repointed: %d\n"+
		"tickets_rewritten: %d\nbindings_adopted: %d\nrecords_rewritten: %d\n",
		c.DryRun, c.From, c.To, c.ReposMerged, c.ReposRenamed, c.BindingsRepointed,
		c.TicketsRewritten, c.BindingsAdopted, c.RecordsRewritten)
}
