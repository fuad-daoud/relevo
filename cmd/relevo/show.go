package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	diffpatch "github.com/fuad-daoud/relevo/internal/patch"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

const showUsage = `usage: relevo show <name> [--round N] [--prompt|--report|--diff|--drift|--log|--transcript|--gate|--findings ID|--output|--artifacts|--artifact REL] [--json]
       relevo show <name> --diff|--drift [--stat] [--anchors]
       relevo show <name> --log [--follow] [--after N]
       relevo show <name> --owner <label|id> [--log] [--state DIR]`

// flagGiven reports whether the named flag was set on the command line. It is
// how `--after 0` is told from the flag's default 0.
func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// showSectionArgs is showSectionFlags' input: which section flags were given.
// It is a struct rather than a parameter list because the count has outgrown
// a readable argument list.
type showSectionArgs struct {
	prompt, report, diff, drift bool
	log, transcript, gate       bool
	output, artifacts           bool
	findingsID                  string
	artifactRel                 string
}

// showSectionFlags counts how many section flags are set and resolves the
// one section they name, defaulting to prompt when none is given. It is a
// pure function so a cmd/relevo test can pin "more than one is a usage
// error" without executing the subcommand. findingsID is `--findings`'s
// value: a non-empty id names the findings section. A non-empty artifactRel
// is `--artifact`'s rel and names the artifacts section, as --artifacts does.
func showSectionFlags(a showSectionArgs) (relevo.ShowSection, error) {
	sections := []struct {
		on      bool
		section relevo.ShowSection
	}{
		{a.prompt, relevo.ShowPrompt},
		{a.report, relevo.ShowReport},
		{a.diff, relevo.ShowDiff},
		{a.drift, relevo.ShowDrift},
		{a.log, relevo.ShowLog},
		{a.transcript, relevo.ShowTranscript},
		{a.gate, relevo.ShowGate},
		{a.findingsID != "", relevo.ShowFindings},
		{a.output, relevo.ShowOutput},
		{a.artifacts || a.artifactRel != "", relevo.ShowArtifacts},
	}
	var chosen relevo.ShowSection
	n := 0
	for _, s := range sections {
		if s.on {
			n++
			chosen = s.section
		}
	}
	switch n {
	case 0:
		return relevo.ShowPrompt, nil
	case 1:
		return chosen, nil
	default:
		return "", fmt.Errorf("only one of --prompt, --report, --diff, --drift, --log, --transcript, --gate, --findings, --output, --artifacts may be given")
	}
}

// cmdShow prints one round's plan, report, diff, drift, log or transcript,
// read from a live binding's files or, for anything not live, from the
// database (docs/specs/2026-09-20-persistence-design.md §5.7). Its --diff,
// --drift and whole-log forms are today's diff and log verbs, byte for byte
// (§4.2).
func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	round := fs.Int("round", 0, "the round to read; 0 = the newest completed round")
	prompt := fs.Bool("prompt", false, "show the prompt (default)")
	report := fs.Bool("report", false, "show the report")
	diff := fs.Bool("diff", false, "show the round's captured diff")
	drift := fs.Bool("drift", false, "show the round's drift patch")
	logSection := fs.Bool("log", false, "show the round's log entries")
	transcript := fs.Bool("transcript", false, "show the round's builder transcript")
	gateSection := fs.Bool("gate", false, "show the round's gate log")
	output := fs.Bool("output", false, "show the round's output file (a reader's <label>.md)")
	artifacts := fs.Bool("artifacts", false, "show the round's artifact files")
	artifact := fs.String("artifact", "", "show one artifact's bytes, raw: --artifact <rel>")
	findings := fs.String("findings", "", "show a consult's findings: --findings <id>")
	stat := fs.Bool("stat", false, "with --diff/--drift: print the summary line instead of the patch body")
	anchors := fs.Bool("anchors", false, "with --diff/--drift: prefix each hunk and line with its path:line")
	follow := fs.Bool("follow", false, "with --log: keep printing new entries until the binding is DONE or removed")
	after := fs.Int("after", 0, "with --log: show only entries with a Seq greater than this (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable output: the ShowResult, Events included for --log")
	owner := fs.String("owner", "", "on the server host: read this owner's binding, a client label or id")
	state := fs.String("state", "", "with --owner: the serve state directory")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), showUsage)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(os.Stderr, showUsage)
		return exitCodeErr{code: 2}
	}
	name := fs.Args()[0]

	// --state names the serve root, so it means nothing without an owner to
	// read there (§4.1).
	if *state != "" && *owner == "" {
		fmt.Fprintln(os.Stderr, "relevo show: --state only applies with --owner")
		fmt.Fprintln(os.Stderr, showUsage)
		return exitCodeErr{code: 2}
	}

	section, serr := showSectionFlags(showSectionArgs{
		prompt: *prompt, report: *report, diff: *diff, drift: *drift,
		log: *logSection, transcript: *transcript, gate: *gateSection,
		output: *output, artifacts: *artifacts,
		findingsID: *findings, artifactRel: *artifact,
	})
	if serr != nil {
		// An --owner invocation is the moved serve show body, so its section
		// conflict keeps that route's prefix and usage line (§4.1).
		if *owner != "" {
			fmt.Fprintln(os.Stderr, "relevo serve show: "+serr.Error())
			fmt.Fprintln(os.Stderr, serveShowUsage)
			return exitCodeErr{code: 2}
		}
		fmt.Fprintln(os.Stderr, "relevo show: "+serr.Error())
		fmt.Fprintln(os.Stderr, showUsage)
		return exitCodeErr{code: 2}
	}

	// The absorbed flags are valid only with the section they came from
	// (§4.2): --stat/--anchors are diff's, --follow/--after are log's.
	if *stat || *anchors {
		if section != relevo.ShowDiff && section != relevo.ShowDrift {
			bad := "--stat"
			if *anchors {
				bad = "--anchors"
			}
			fmt.Fprintf(os.Stderr, "relevo show: %s requires --diff or --drift\n", bad)
			fmt.Fprintln(os.Stderr, showUsage)
			return exitCodeErr{code: 2}
		}
	}
	if *follow || flagGiven(fs, "after") {
		if section != relevo.ShowLog {
			bad := "--follow"
			if !*follow {
				bad = "--after"
			}
			fmt.Fprintf(os.Stderr, "relevo show: %s requires --log\n", bad)
			fmt.Fprintln(os.Stderr, showUsage)
			return exitCodeErr{code: 2}
		}
	}
	if *after < 0 {
		fmt.Fprintln(os.Stderr, "relevo: --after must be >= 0")
		return exitCodeErr{code: 2}
	}

	if *owner != "" {
		// --log without --round is the whole-log read the removed `serve
		// log` did; every other form is the removed `serve show` (§4.1).
		if section == relevo.ShowLog && *round == 0 {
			return serveLog(*owner, *state, name, *round, *after, *asJSON, *follow)
		}
		return serveShow(*owner, *state, name, *round, section, *asJSON, *artifact)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	switch {
	case section == relevo.ShowDiff, section == relevo.ShowDrift:
		// Byte-identical to the removed diff verb, including its default
		// round and its #143 .viewed stamp.
		return printDiff(rt, name, *round, *stat, section == relevo.ShowDrift, *anchors)
	case section == relevo.ShowLog && (*round == 0 || *follow):
		// The whole log is the removed log verb, byte for byte, --json's
		// NDJSON included.
		return printLog(rt, name, *round, *after, *asJSON, *follow, true)
	}

	opts := relevo.ShowOptions{Name: name, Round: *round, Section: section, JSON: *asJSON, FindingsID: *findings, ArtifactRel: *artifact}
	return printShow(rt, opts, true, true, "")
}

// printShow is cmdShow's body after section resolution, moved verbatim.
// allowDB=false means a non-live binding returns store.ErrNotFound instead
// of opening (and so creating) the database: `relevo serve show` reads live
// bindings only. markViewed guards the #143 .viewed stamp the same way
// printLog's does. headerPrefix is prepended to the stderr header, which is
// how `relevo serve show` names the owner it read from.
func printShow(rt relevo.Runtime, opts relevo.ShowOptions, markViewed, allowDB bool, headerPrefix string) error {
	name := opts.Name

	// A live binding needs no database at all; only open one -- and only
	// fail on it -- once we know the binding is not live
	// (docs/specs/2026-09-20-persistence-design.md §4: "show on a live
	// binding still works ... and on a non-live one exits 1 with the
	// error").
	live := false
	if _, loadErr := rt.Store.Load(name); loadErr != nil {
		if !errors.Is(loadErr, store.ErrNotFound) {
			return loadErr
		}
		if !allowDB {
			return fmt.Errorf("%s: %w", name, store.ErrNotFound)
		}
		d, dbErr := openDB(rt.Store.DBPath())
		if dbErr != nil {
			fmt.Fprintf(os.Stderr, "relevo show: %v\n", dbErr)
			return exitCodeErr{code: 1}
		}
		defer d.Close()
		rt.DB = d
	} else {
		live = true
	}
	// #143: a successful print is what "viewed" means, for a live binding
	// only -- there is no .viewed sidecar for a database-only (archived)
	// binding to stamp. Best-effort: never fails the read.
	stampViewed := func() {
		if markViewed && live {
			_ = rt.Store.MarkViewed(name, time.Now())
		}
	}

	res, err := relevo.Show(context.Background(), rt, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo show: %v\n", err)
		return exitCodeErr{code: 1}
	}

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
		stampViewed()
		return nil
	}

	header := fmt.Sprintf("%s%s round %d of %d · %s", headerPrefix, res.Name, res.Round, res.Rounds, res.Section)
	if res.Archived {
		header += " · archived " + res.ArchivedAt.Format("2006-01-02")
	}
	fmt.Fprintln(os.Stderr, header)

	if opts.ArtifactRel != "" {
		// --artifact: the file's bytes, raw, with nothing added. An unlisted
		// rel is an error Show already returned.
		if _, err := os.Stdout.Write([]byte(res.Text)); err != nil {
			return err
		}
		stampViewed()
		return nil
	}

	if res.Missing {
		fmt.Printf("no %s for round %d\n", res.Section, res.Round)
		stampViewed()
		return nil
	}

	if res.Section == relevo.ShowArtifacts {
		for _, f := range res.Artifacts {
			fmt.Println(relevo.ArtifactLine(f))
		}
		stampViewed()
		return nil
	}

	if res.Section == relevo.ShowLog {
		for _, e := range res.Events {
			fmt.Println(relevo.LogLine(e))
		}
		stampViewed()
		return nil
	}

	text := res.Text
	if text != "" && text[len(text)-1] != '\n' {
		text += "\n"
	}
	fmt.Print(text)
	stampViewed()
	return nil
}

// printDiff is diff's body (the old cmdDiff), shared by `show --diff` and
// `show --drift` (§4.2): the output is byte-identical to the removed diff verb
// for the same arguments, including the #143 .viewed stamp.
// round 0 means diff's default: the newest completed round, or the open round
// with drift.
func printDiff(rt relevo.Runtime, name string, round int, stat, drift, anchors bool) error {
	b, err := rt.Store.Load(name)
	if err != nil {
		return err
	}

	targetRound := round
	if targetRound == 0 {
		if drift {
			targetRound = b.Round
		} else {
			targetRound = b.Round - 1
		}
	}
	if targetRound < 1 {
		return fmt.Errorf("binding %q has no completed round yet", name)
	}

	if stat {
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return err
		}
		targetKind := store.KindDiff
		if drift {
			targetKind = store.KindDrift
		}
		var found *store.LogEntry
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == targetRound && entries[i].Kind == targetKind {
				found = &entries[i]
				break
			}
		}
		if found == nil || found.Note == "" {
			if drift {
				return fmt.Errorf("no drift recorded for round %d of %q (--drift)", targetRound, name)
			}
			return fmt.Errorf("no diff recorded for round %d of %q", targetRound, name)
		}
		fmt.Println(found.Note)
		// #143: a successful print is what "viewed" means; the stamp is
		// best-effort and must never fail a read command.
		_ = rt.Store.MarkViewed(name, time.Now())
		return nil
	}

	var patch []byte
	var ok bool
	if drift {
		patch, ok, err = capture.ReadDrift(rt.Store, name, targetRound)
	} else {
		patch, ok, err = capture.ReadDiff(rt.Store, name, targetRound)
	}
	if err != nil {
		return err
	}
	if !ok {
		if drift {
			return fmt.Errorf("no drift recorded for round %d of %q (--drift)", targetRound, name)
		}
		return fmt.Errorf("no diff recorded for round %d of %q", targetRound, name)
	}

	if anchors {
		patch, err = diffpatch.Annotate(patch)
		if err != nil {
			return err
		}
	}

	if _, err := os.Stdout.Write(patch); err != nil {
		return err
	}
	// #143: a successful print is what "viewed" means; the stamp is
	// best-effort and must never fail a read command.
	_ = rt.Store.MarkViewed(name, time.Now())
	return nil
}

// printLog is the log body after flag parsing and newRuntime(): it prints
// name's entries, applying the --round filter, JSON-encoded when asJSON is
// set, and follows new entries until the binding is DONE or removed when
// follow is set. markViewed guards the #143 .viewed stamp: `relevo show --log`
// stamps and the read-only `relevo serve log` must not, because the stamp is
// the owner's, not the admin's.
func printLog(rt relevo.Runtime, name string, round, after int, asJSON, follow, markViewed bool) error {
	// A binding that does not exist is named at once, the way every other
	// command reports it; only a binding that disappears mid-follow (below)
	// ends the loop quietly.
	if _, err := rt.Store.Load(name); err != nil {
		return err
	}

	// emit applies the --round filter, so the initial batch and every
	// followed entry render identically.
	emit := func(e store.LogEntry) {
		if round != 0 && e.Round != round {
			return
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(e)
			return
		}
		fmt.Println(relevo.LogLine(e))
	}

	entries, err := rt.Store.ReadLogAfter(name, after)
	if err != nil {
		return err
	}
	last := after
	for _, e := range entries {
		emit(e)
		last = e.Seq
	}

	if !follow {
		// #143: a successful print is what "viewed" means; the stamp is
		// best-effort and must never fail a read command.
		if markViewed {
			_ = rt.Store.MarkViewed(name, time.Now())
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relevo.FollowLog(ctx, rt, name, last, time.Second, emit); err != nil {
		if ctx.Err() != nil {
			// Interrupted: what was already printed is the answer, and the
			// stamp is #143's the same as any other exit.
			if markViewed {
				_ = rt.Store.MarkViewed(name, time.Now())
			}
			return nil
		}
		return err
	}
	if markViewed {
		_ = rt.Store.MarkViewed(name, time.Now())
	}
	return nil
}
