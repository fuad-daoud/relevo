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
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/store"
)

const showUsage = `usage: relevo show <name> [--round N] [--prompt|--report|--diff|--drift|--log|--transcript|--gate|--findings ID|--output|--artifacts|--artifact REL] [--json] [--peek]
       relevo show <name> --diff|--drift [--stat] [--anchors]
       relevo show <name> --log [--follow] [--after N]
       relevo show <chain> --trace [--json]
       relevo show <chain> --workflow [--json]
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
	trace                       bool
	workflow                    bool
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
		{a.trace, relevo.ShowTrace},
		{a.workflow, relevo.ShowWorkflow},
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
		return "", fmt.Errorf("only one of --prompt, --report, --diff, --drift, --log, --transcript, --gate, --findings, --output, --artifacts, --trace, --workflow may be given")
	}
}

// showFlagValues holds the pointers show parses into.
type showFlagValues struct {
	round       *int
	prompt      *bool
	report      *bool
	diff        *bool
	drift       *bool
	logSection  *bool
	transcript  *bool
	gateSection *bool
	output      *bool
	artifacts   *bool
	trace       *bool
	workflow    *bool
	artifact    *string
	findings    *string
	stat        *bool
	anchors     *bool
	follow      *bool
	after       *int
	asJSON      *bool
	peek        *bool
	owner       *string
	state       *string
}

// showFlagSet defines those flags on fs, in the usage text's order, and
// returns what they parse into.
func showFlagSet(fs *flag.FlagSet) *showFlagValues {
	v := &showFlagValues{}
	v.round = fs.Int("round", 0, "the round to read; 0 = the newest completed round")
	v.prompt = fs.Bool("prompt", false, "show the prompt (default)")
	v.report = fs.Bool("report", false, "show the report")
	v.diff = fs.Bool("diff", false, "show the round's captured diff")
	v.drift = fs.Bool("drift", false, "show the round's drift patch")
	v.logSection = fs.Bool("log", false, "show the round's log entries")
	v.transcript = fs.Bool("transcript", false, "show the round's builder transcript")
	v.gateSection = fs.Bool("gate", false, "show the round's gate log")
	v.output = fs.Bool("output", false, "show the round's output file (a reader's <label>.md)")
	v.artifacts = fs.Bool("artifacts", false, "show the round's artifact files")
	v.trace = fs.Bool("trace", false, "on a chain: show its ordered trace")
	v.workflow = fs.Bool("workflow", false, "on a chain: show the workflow it runs, as JSON")
	v.artifact = fs.String("artifact", "", "show one artifact's bytes, raw: --artifact <rel>")
	v.findings = fs.String("findings", "", "show a consult's findings: --findings <id>")
	v.stat = fs.Bool("stat", false, "with --diff/--drift: print the summary line instead of the patch body")
	v.anchors = fs.Bool("anchors", false, "with --diff/--drift: prefix each hunk and line with its path:line")
	v.follow = fs.Bool("follow", false, "with --log: keep printing new entries until the binding is DONE or removed")
	v.after = fs.Int("after", 0, "with --log: show only entries with a Seq greater than this (0 = all)")
	v.asJSON = fs.Bool("json", false, "machine-readable output: the ShowResult, Events included for --log")
	v.peek = fs.Bool("peek", false, "read the section without claiming the binding's pending payload")
	v.owner = fs.String("owner", "", "on the server host: read this owner's binding, a client label or id")
	v.state = fs.String("state", "", "with --owner: the serve state directory")
	return v
}

// cmdShow prints one round's plan, report, diff, drift, log or transcript,
// read from a live binding's files or, for anything not live, from the
// database (docs/specs/2026-09-20-persistence-design.md §5.7). Its --diff,
// --drift and whole-log forms are today's diff and log verbs, byte for byte
// (§4.2).
func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	v := showFlagSet(fs)
	round, prompt, report, diff := v.round, v.prompt, v.report, v.diff
	drift, logSection, transcript, gateSection := v.drift, v.logSection, v.transcript, v.gateSection
	output, artifacts, artifact, findings := v.output, v.artifacts, v.artifact, v.findings
	trace, workflowSection := v.trace, v.workflow
	stat, anchors, follow, after := v.stat, v.anchors, v.follow, v.after
	asJSON, peek, owner, state := v.asJSON, v.peek, v.owner, v.state
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), showUsage)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return fail(codeUsage, "show wants exactly one binding name, got %d", len(fs.Args()))
	}
	name := fs.Args()[0]

	// --state names the serve root, so it means nothing without an owner to
	// read there (§4.1).
	if *state != "" && *owner == "" {
		return fail(codeUsage, "--state only applies with --owner")
	}

	section, serr := showSectionFlags(showSectionArgs{
		prompt: *prompt, report: *report, diff: *diff, drift: *drift,
		log: *logSection, transcript: *transcript, gate: *gateSection,
		output: *output, artifacts: *artifacts,
		trace:      *trace,
		workflow:   *workflowSection,
		findingsID: *findings, artifactRel: *artifact,
	})
	if serr != nil {
		// An --owner invocation is the moved serve show body, so its section
		// conflict keeps that route's name and a hint naming that form (§4.1).
		if *owner != "" {
			return failNext(codeUsage, "relevo show --owner", "serve show: %v", serr)
		}
		return fail(codeUsage, "show: %v", serr)
	}

	// The absorbed flags are valid only with the section they came from
	// (§4.2): --stat/--anchors are diff's, --follow/--after are log's.
	if *stat || *anchors {
		if section != relevo.ShowDiff && section != relevo.ShowDrift {
			bad := "--stat"
			if *anchors {
				bad = "--anchors"
			}
			return fail(codeUsage, "%s requires --diff or --drift", bad)
		}
	}
	if *follow || flagGiven(fs, "after") {
		if section != relevo.ShowLog {
			bad := "--follow"
			if !*follow {
				bad = "--after"
			}
			return fail(codeUsage, "%s requires --log", bad)
		}
	}
	if *after < 0 {
		return fail(codeUsage, "--after must be >= 0")
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
		return classifyReadErr(printDiff(rt, name, *round, *stat, section == relevo.ShowDrift, *anchors))
	case section == relevo.ShowLog && (*round == 0 || *follow):
		// The whole log is the removed log verb, byte for byte, --json's
		// NDJSON included.
		return classifyReadErr(printLog(rt, name, *round, *after, *asJSON, *follow, true))
	}

	opts := relevo.ShowOptions{Name: name, Round: *round, Section: section, JSON: *asJSON, Peek: *peek, FindingsID: *findings, ArtifactRel: *artifact}
	return classifyReadErr(printShow(rt, opts, true, true, ""))
}

// classifyReadErr maps a read verb's failure onto the catalog: a binding the
// store does not hold, a round with nothing completed, an artifact no listing
// names, and everything else internal. The helpers printShow, printLog and
// printDiff keep returning the errors they always did, each wrapping one of
// these causes, so the classification lives at this one boundary.
func classifyReadErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return fail(codeBindingNotFound, "%v", err)
	case errors.Is(err, relevo.ErrNoCompletedRound):
		return fail(codeRoundNotFound, "%v", err)
	case errors.Is(err, relevo.ErrRoundNotFound):
		// An out-of-range --round: the binding is there, the round asked for
		// is not. Same code as an unreadable round, different cause.
		return fail(codeRoundNotFound, "%v", err)
	case errors.Is(err, relevo.ErrNoArtifact):
		return fail(codeArtifactNotFound, "%v", err)
	case errors.Is(err, relevo.ErrNotAChain):
		// --trace names a chain, and only a chain has one: the name exists,
		// the request for it is what is wrong.
		return failWrap(codeUsage, err, "%v", err)
	default:
		return fail(codeInternal, "%v", err)
	}
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
			return fmt.Errorf("open %s: %w", rt.Store.DBPath(), dbErr)
		}
		defer d.Close()
		rt.DB = d
	} else {
		live = true
	}
	// #143: a successful print is what "viewed" means, for a live binding
	// only -- an archived binding's record stays as it is.
	// Best-effort: never fails the read.
	stampViewed := func() {
		if markViewed && live {
			_ = rt.Store.MarkViewed(name, time.Now())
		}
	}

	res, err := relevo.Show(context.Background(), rt, opts)
	if err != nil {
		return err
	}

	if opts.JSON {
		if res.Trace != nil {
			// --trace's document is the trace itself, not the round-shaped
			// ShowResult the other sections encode.
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(res.Trace); err != nil {
				return err
			}
			stampViewed()
			return nil
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
		stampViewed()
		return nil
	}

	// A trace or a workflow names no round: the header says chain and the
	// section instead.
	var header string
	if res.Section == relevo.ShowTrace {
		header = fmt.Sprintf("%s%s · trace", headerPrefix, res.Name)
	} else if res.Section == relevo.ShowWorkflow {
		header = fmt.Sprintf("%s%s · workflow", headerPrefix, res.Name)
	} else {
		header = fmt.Sprintf("%s%s round %d of %d · %s", headerPrefix, res.Name, res.Round, res.Rounds, res.Section)
		if res.Archived {
			header += " · archived " + res.ArchivedAt.Format("2006-01-02")
		}
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

	text := sanitize.Text(res.Text)
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
		return fmt.Errorf("binding %q has no completed round yet: %w", name, relevo.ErrNoCompletedRound)
	}
	// Upper bound: a --round past the binding's counter is a missing round,
	// not a missing diff. Without this the lookup below falls through to
	// ErrNoArtifact and an out-of-range round reads as artifact_not_found.
	if targetRound > b.Round {
		return relevo.RoundOutOfRange(name, targetRound, b.Round, drift)
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
				return fmt.Errorf("no drift recorded for round %d of %q (--drift): %w", targetRound, name, relevo.ErrNoArtifact)
			}
			return fmt.Errorf("no diff recorded for round %d of %q: %w", targetRound, name, relevo.ErrNoArtifact)
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
			return fmt.Errorf("no drift recorded for round %d of %q (--drift): %w", targetRound, name, relevo.ErrNoArtifact)
		}
		return fmt.Errorf("no diff recorded for round %d of %q: %w", targetRound, name, relevo.ErrNoArtifact)
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
