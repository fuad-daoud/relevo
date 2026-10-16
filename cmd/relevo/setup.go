package main

// cmd/relevo/setup.go is the setup wizard's front end: flags, the two
// documents, the inventory line and the prompts. The apply steps live in
// setup_apply.go and setup_smoke.go; internal/setup owns the probed facts and
// the starter configuration the steps store.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/setup"
)

// setupQuestionsCode is the exit code a run with questions still open takes:
// the document the answer flow needs is already on stdout, so report prints
// nothing on top of it.
const setupQuestionsCode = 3

// setupAnswerNotes is where an answer for a dropped question is noted, so an
// ignored answer is never silent. nil asks the note writer for stderr each
// time, so a test's swapped process streams still see it.
var setupAnswerNotes io.Writer

func answerNoteWriter() io.Writer {
	if setupAnswerNotes != nil {
		return setupAnswerNotes
	}
	return os.Stderr
}

// setupPromptStream is where the numbered prompts render, before the answer
// is read from stdin. Under --json the verb never prompts.
var setupPromptStream io.Writer = os.Stdout

// setupAnswerList is the repeatable --answer flag: a Set keeps every value,
// unlike a scalar flag which forgets the earlier ones.
type setupAnswerList []string

func (a *setupAnswerList) String() string { return strings.Join(*a, " ") }

func (a *setupAnswerList) Set(v string) error {
	*a = append(*a, v)
	return nil
}

// setupFlagValues holds the pointers setup's flags parse into. setupFlagSet
// defines them on fs, so cmds and tests read the same surface.
type setupFlagValues struct {
	answers *setupAnswerList
	asJSON  *bool
}

func setupFlagSet(fs *flag.FlagSet) *setupFlagValues {
	v := &setupFlagValues{}
	v.answers = new(setupAnswerList)
	fs.Var(v.answers, "answer", "answer one question by id and value: id=value (repeatable)")
	v.asJSON = fs.Bool("json", false, "print the setup document as JSON")
	return v
}

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("relevo setup", flag.ContinueOnError)
	v := setupFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "relevo setup takes no arguments, got %v", fs.Args())
	}
	rt, err := newRuntime()
	if err != nil {
		return err
	}
	return setupRun(rt, *v.answers, *v.asJSON, isTerminal(os.Stdin), isTerminal(os.Stdout))
}

// signalContext builds the context the smoke round's wait passes through: an
// interrupt ends the wait and the run like any other blocking verb.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// setupRun starts one run: probe, refuse the states the machine alone
// explains, print the inventory, and move the questions into answers either
// by flag or by prompt.
func setupRun(rt relevo.Runtime, answersFlag []string, asJSON, stdinTTY, stdoutTTY bool) error {
	ctx, stop := signalContext()
	defer stop()

	env, err := agentInstallEnv()
	if err != nil {
		return err
	}
	w := noticeWriter(asJSON)
	// The probe runs every CLI's status and model commands, seconds of
	// silence without this line.
	fmt.Fprintln(w, "checking which coding agents are installed and logged in...")
	inv := setup.Probe(env)
	if inEnvSandbox() {
		inv.Notes = append(inv.Notes, sandboxServiceNote)
	}
	if err := setupPrecheck(inv); err != nil {
		return err
	}
	rec, standing, err := setupGroundedAnswers(rt, inv)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, setupInventoryLine(inv))
	for _, note := range inv.Notes {
		fmt.Fprintln(w, note)
	}

	qs := setup.Questions(inv, standing)
	given, err := parseSetupAnswers(answersFlag, qs)
	if err != nil {
		return fail(codeUsage, "%v", err)
	}
	open := openSetupQuestions(qs, given)
	switch {
	case !anyRequired(open):
		// Only role questions are left, and an unanswered role is derived.
	case setupAskMode(stdinTTY, stdoutTTY, asJSON):
		prompted, err := askSetupQuestions(setupPromptStream, os.Stdin, open, given)
		if err != nil {
			return fail(codeInternal, "%v", err)
		}
		for id, vals := range prompted {
			given[id] = append([]string{}, vals...)
		}
	default:
		return setupPrintQuestions(setupQuestionsDocOf(inv, open))
	}

	return setupApply(ctx, refreshSetupRuntime, env, rt, inv, mergeSetupAnswers(qs, rec, given), asJSON)
}

// setupPrecheck refuses before anything prints when the machine itself is
// the problem: no wizard harness on PATH at all, or none logged in. Both
// refusals are Build's own messages asked for with no answers at all, so the
// wording keeps init's shape instead of a second copy.
func setupPrecheck(inv setup.Inventory) error {
	lcd := len(inv.Harnesses) == 0
	if _, err := setup.Build(inv, nil); err != nil && (lcd || isNotAvailable(err)) {
		return setupBuildError(err)
	}
	return nil
}

func isNotAvailable(err error) bool {
	var na *setup.NotAvailableError
	return errors.As(err, &na)
}

// setupGroundedAnswers names the recommendations a rerun keeps: the stored
// builder/planner/tier values are the standing answers, and what plugin
// install and service unit already live here drop their questions.
func setupGroundedAnswers(rt relevo.Runtime, inv setup.Inventory) (setup.Answers, setup.Answers, error) {
	current, err := storedSetupAnswers(rt)
	if err != nil {
		return nil, nil, err
	}
	standing := setupStandingAnswers(current, doctor.RelevoPluginInstalledIn(claudeConfigDir()), setupUnitPresent() || inEnvSandbox())
	return setup.RecommendedAnswers(inv, standing), standing, nil
}

// storedSetupAnswers reads the configured values a rerun keeps: the builder
// actor's candidates, the planner actor's first pick, and policy's ceiling.
func storedSetupAnswers(rt relevo.Runtime) (setup.Answers, error) {
	L, err := rt.Config.Load()
	if err != nil {
		return nil, err
	}
	out := setup.Answers{}
	if refs := storedActorRefs(L.Actors["builder"], L.Candidates); len(refs) > 0 {
		out["builder"] = refs
	}
	if a, ok := L.Actors["planner"]; ok && len(a.Candidates) > 0 {
		if ref := storedActorRef(a.Candidates[0].Candidate, L.Candidates); ref != "" {
			out["planner"] = []string{ref}
		}
	}
	if t := L.Policy.MaxTier; t != "" {
		out["tier"] = []string{t}
	}
	return out, nil
}

// storedActorRefs turns a stored actor's candidate names into the
// harness/provider/model tokens the setup questions speak, in the actor's
// own order; a name the candidate set no longer holds is skipped.
func storedActorRefs(a roles.Actor, set *candidate.Set) []string {
	refs := make([]string, 0, len(a.Candidates))
	for _, e := range a.Candidates {
		if ref := storedActorRef(e.Candidate, set); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func storedActorRef(name string, set *candidate.Set) string {
	if set == nil || name == "" {
		return ""
	}
	c, err := set.Resolve(name)
	if err != nil {
		return ""
	}
	return c.Ref().String()
}

// setupStandingAnswers merges the two install facts into the stored answers:
// an installed plugin and a present service unit are standing yes/nos, and
// their questions drop while they stay.
func setupStandingAnswers(current setup.Answers, pluginInstalled, unitPresent bool) setup.Answers {
	out := make(setup.Answers, len(current)+2)
	for id, vals := range current {
		out[id] = vals
	}
	if pluginInstalled {
		out["plugin"] = []string{"yes"}
	}
	if unitPresent {
		out["service"] = []string{"yes"}
	}
	return out
}

// claudeConfigDir is where Claude Code keeps its plugins: CLAUDE_CONFIG_DIR
// when set, else ~/.claude.
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// sandboxServiceNote says why an env sandbox gets no service question: the
// unit is the host's one relevo.service, so installing it from a sandbox
// would restart the developer's own daemon on the sandbox binary.
const sandboxServiceNote = "service: skipped inside an env sandbox; it would replace the host's relevo.service"

func inEnvSandbox() bool { return os.Getenv("RELEVO_SANDBOX") != "" }

func setupUnitPresent() bool {
	u, err := osUnitSpec()
	if err != nil {
		return false
	}
	_, _, present, rerr := u.read()
	return rerr == nil && present
}

// setupAskMode says whether still-open questions are asked on the run's
// terminals: both stdin and stdout must be character devices, and --json
// never prompts because its document carries the questions.
func setupAskMode(stdinTTY, stdoutTTY, asJSON bool) bool {
	return stdinTTY && stdoutTTY && !asJSON
}

// parseSetupAnswers collects the --answer flags into per-id value lists: a
// repeated flag appends, each value validates against its own question, and
// a flag naming a question the standing facts dropped is ignored with a
// stderr note.
func parseSetupAnswers(raw []string, questions []setup.Question) (setup.Answers, error) {
	out := make(setup.Answers, len(raw))
	for _, piece := range raw {
		id, value, ok := setupAnswerPiece(piece)
		if !ok {
			return nil, fmt.Errorf("%q is not --answer id=value", piece)
		}
		q, known := findQuestion(questions, id)
		if !known {
			fmt.Fprintf(answerNoteWriter(), "relevo setup: --answer %s is for a question that no longer applies; it is ignored\n", id)
			continue
		}
		vals, err := setup.ParseAnswer(q, value)
		if err != nil {
			return nil, err
		}
		out[id] = append(out[id], vals...)
	}
	return out, nil
}

// setupAnswerPiece reads one flag value as "id=value", accepting and dropping
// the leading --answer spelling.
func setupAnswerPiece(piece string) (string, string, bool) {
	extra := strings.TrimPrefix(piece, "--answer")
	id, value, found := strings.Cut(strings.TrimSpace(extra), "=")
	if !found || id == "" || value == "" {
		return "", "", false
	}
	return strings.TrimSpace(id), strings.TrimSpace(value), true
}

func findQuestion(qs []setup.Question, id string) (setup.Question, bool) {
	for _, q := range qs {
		if q.ID == id {
			return q, true
		}
	}
	return setup.Question{}, false
}

// openSetupQuestions are the questions no given answer fills yet.
func openSetupQuestions(qs []setup.Question, given setup.Answers) []setup.Question {
	open := make([]setup.Question, 0, len(qs))
	for _, q := range qs {
		if len(given[q.ID]) == 0 {
			open = append(open, q)
		}
	}
	return open
}

func anyRequired(qs []setup.Question) bool {
	for _, q := range qs {
		if !q.Optional {
			return true
		}
	}
	return false
}

// mergeSetupAnswers names every answer the apply carries: the given flags
// and prompts win, the recommendations fill what nothing answered. A role
// nothing answered stays empty, so Build derives it from the answers given.
func mergeSetupAnswers(qs []setup.Question, rec, given setup.Answers) setup.Answers {
	out := make(setup.Answers, len(qs))
	for _, q := range qs {
		if vals := given[q.ID]; len(vals) > 0 {
			out[q.ID] = append([]string{}, vals...)
			continue
		}
		if q.Optional {
			continue
		}
		if vals := rec[q.ID]; len(vals) > 0 {
			out[q.ID] = append([]string{}, vals...)
			continue
		}
		if len(q.Recommended) > 0 {
			out[q.ID] = append([]string{}, q.Recommended...)
		}
	}
	return out
}

// setupInventoryLine is the one probe line a run prints: every wizard
// harness on PATH, logged in or not, with the login command naming the way
// in.
func setupInventoryLine(inv setup.Inventory) string {
	parts := make([]string, 0, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		if s.LoggedIn {
			parts = append(parts, s.Harness+": logged in")
			continue
		}
		parts = append(parts, s.Harness+": on PATH, not logged in -> "+s.Login)
	}
	return strings.Join(parts, " · ")
}
