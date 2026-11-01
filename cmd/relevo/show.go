package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	diffpatch "github.com/fuad-daoud/relevo/internal/patch"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

const showUsage = `usage: relevo show <name> [--round N] [--prompt|--report|--diff|--drift|--log|--transcript|--gate|--findings ID|--output|--artifacts|--artifact REL] [--json] [--peek]
       relevo show <name> --diff|--drift [--stat] [--anchors]
       relevo show <name> --log [--follow] [--after N]
       relevo show <chain> --trace [--json]
       relevo show <chain> --workflow [--json]
       relevo show <name> --owner <label|id> [--log] [--state DIR]`

// flagGiven reports whether the named flag was set; it is how `--after 0` is
// told from the default 0.
func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// showSectionArgs is showSectionFlags' input. It is a struct because the
// section flags do not fit a readable argument list.
type showSectionArgs struct {
	prompt, report, diff, drift bool
	log, transcript, gate       bool
	output, artifacts           bool
	trace                       bool
	workflow                    bool
	findingsID                  string
	artifactRel                 string
}

// showSectionFlags resolves the one section the flags name, defaulting to
// prompt. It is pure so a test can pin "more than one is a usage error"
// without executing the subcommand.
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

// renamedShowFlag answers --summary with the flag that replaced it; old
// references still name it.
func renamedShowFlag(args []string) error {
	for _, a := range args {
		if a == "--summary" || a == "-summary" || strings.HasPrefix(a, "--summary=") {
			return failNext(codeUsage, "relevo show <name> --output", "--summary is now --output")
		}
	}
	return nil
}

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

// cmdShow prints one round's plan, report, diff, drift, log or transcript.
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
	if err := renamedShowFlag(args); err != nil {
		return err
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return fail(codeUsage, "show wants exactly one binding name, got %d", len(fs.Args()))
	}
	name := fs.Args()[0]

	// --state names the serve root, so it means nothing without an owner to
	// read there.
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
		// An --owner invocation runs the serve show body, so its section
		// conflict keeps that route's name.
		if *owner != "" {
			return failNext(codeUsage, "relevo show --owner", "serve show: %v", serr)
		}
		return fail(codeUsage, "show: %v", serr)
	}

	// The absorbed flags are valid only with their own section:
	// --stat/--anchors are diff's, --follow/--after are log's.
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
		// --log without --round is the whole-log read; every other form is
		// the `serve show` section read.
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
		// The diff body stays byte-identical, including its default round
		// and its .viewed stamp.
		return classifyReadErr(printDiff(rt, name, *round, *stat, section == relevo.ShowDrift, *anchors))
	case section == relevo.ShowLog && (*round == 0 || *follow):
		// The whole log prints byte for byte, --json's NDJSON included.
		return classifyReadErr(printLog(rt, name, *round, *after, *asJSON, *follow, true))
	}

	opts := relevo.ShowOptions{Name: name, Round: *round, Section: section, JSON: *asJSON, Peek: *peek, FindingsID: *findings, ArtifactRel: *artifact}
	return classifyReadErr(printShow(rt, opts, true, true, ""))
}

// classifyReadErr maps a read verb's failure onto the catalog. The helpers
// keep returning the errors they always did, so the classification lives at
// this one boundary.
func classifyReadErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return fail(codeBindingNotFound, "%v", err)
	case errors.Is(err, relevo.ErrNoCompletedRound):
		return fail(codeRoundNotFound, "%v", err)
	case errors.Is(err, refusal.ErrRoundNotFound):
		// An out-of-range --round names a round that is not there: same code
		// as an unreadable round, different cause.
		return fail(codeRoundNotFound, "%v", err)
	case errors.Is(err, relevo.ErrNoArtifact):
		return fail(codeArtifactNotFound, "%v", err)
	case errors.Is(err, relevo.ErrNotAChain):
		// --trace names a chain, and only a chain has one.
		return failWrap(codeUsage, err, "%v", err)
	default:
		return fail(codeInternal, "%v", err)
	}
}

// printShow is cmdShow's body after section resolution. allowDB=false returns
// store.ErrNotFound for a non-live binding instead of opening the database;
// markViewed guards the .viewed stamp, and headerPrefix names the owner.
func printShow(rt relevo.Runtime, opts relevo.ShowOptions, markViewed, allowDB bool, headerPrefix string) error {
	name := opts.Name

	// A live binding needs no database; only open one once the binding is
	// known not live.
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
	// A successful print is what "viewed" means, for a live binding only;
	// best-effort, never failing the read.
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
	// section.
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
		// --artifact writes the file's bytes, raw; an unlisted rel is an
		// error Show already returned.
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
			fmt.Println(view.ArtifactLine(f))
		}
		stampViewed()
		return nil
	}

	if res.Section == relevo.ShowLog {
		for _, e := range res.Events {
			fmt.Println(view.LogLine(e))
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

// printDiff is diff's body for --diff and --drift; the output stays
// byte-identical, including the .viewed stamp. The default round is the newest
// completed round, or the open round with drift.
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
	// A --round past the binding's counter is a missing round, not a missing
	// diff; without this the lookup below falls through to ErrNoArtifact.
	if targetRound > b.Round {
		return refusal.RoundOutOfRange(name, targetRound, b.Round, drift)
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
		// A successful print is what "viewed" means; the stamp is best-effort
		// and never fails a read.
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
	// A successful print is what "viewed" means; the stamp is best-effort
	// and never fails a read.
	_ = rt.Store.MarkViewed(name, time.Now())
	return nil
}

// printLog prints name's entries and follows new ones when follow is set.
// markViewed guards the .viewed stamp: the read-only `relevo serve log` must
// not stamp, because the stamp is the owner's.
func printLog(rt relevo.Runtime, name string, round, after int, asJSON, follow, markViewed bool) error {
	// A missing binding is named at once; only one that disappears mid-follow
	// ends the loop quietly.
	if _, err := rt.Store.Load(name); err != nil {
		return err
	}

	// emit applies the --round filter, so the initial batch and every followed
	// entry render identically.
	emit := func(e store.LogEntry) {
		if round != 0 && e.Round != round {
			return
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(e)
			return
		}
		fmt.Println(view.LogLine(e))
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
		// A successful print is what "viewed" means; the stamp is best-effort
		// and never fails a read.
		if markViewed {
			_ = rt.Store.MarkViewed(name, time.Now())
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relevo.FollowLog(ctx, rt, name, last, time.Second, emit); err != nil {
		if ctx.Err() != nil {
			// Interrupted: what was already printed is the answer.
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
