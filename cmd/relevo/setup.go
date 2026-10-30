// setup.go is the setup verb's thin front end: flag parsing, the runtime,
// the tty detection and the exit mapping. The wizard's work lives in
// internal/setup; this file only translates between it and the CLI frame.
package main

import (
	"errors"
	"flag"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/setup"
	"github.com/fuad-daoud/relevo/internal/text"
)

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
	env, err := agentInstallEnv()
	if err != nil {
		return err
	}
	asJSON := *v.asJSON
	deps := setup.Deps{
		Env:                env,
		Refresh:            newRuntime,
		ActorSummary:       actorSummary,
		InstallService:     setupServiceInstall,
		MastermindRegistry: mastermindRegistry,
		ClassifyTransport:  classifySetupTransport,
		InSandbox:          inEnvSandbox(),
		UnitPresent:        setupUnitPresent(),
		Stdin:              os.Stdin,
		Notices:            noticeWriter(asJSON),
		PrintDoc:           printDoc,
	}
	return setupExitOf(setup.Run(rt, deps, []string(*v.answers), asJSON, isTerminal(os.Stdin), isTerminal(os.Stdout)))
}

// setupExitOf is the exit a setup run takes without a second print: a step
// failure's row (or the result document) already said what failed and what
// fixes it, so a shaped failure exits silently. A usage error or a
// machine-state refusal printed nothing yet, so those report like any other
// caller-side failure.
func setupExitOf(err error) error {
	if err == nil {
		return nil
	}
	var ue *setup.UsageError
	if errors.As(err, &ue) {
		return fail(codeUsage, "%v", ue.Err)
	}
	var ie *setup.InternalError
	if errors.As(err, &ie) {
		return fail(codeInternal, "%v", ie.Err)
	}
	var na *setup.NotAvailableError
	if errors.As(err, &na) {
		return failNext(codeNotAvailable, "relevo setup", "%s", na.Message)
	}
	if errors.Is(err, setup.ErrQuestionsOpen) {
		return exitCodeErr{code: setup.QuestionsCode}
	}
	var le *setup.LimitError
	if errors.As(err, &le) {
		return exitCodeErr{code: catalogExit(codeGateActive)}
	}
	var cse *setup.CodedStepError
	if errors.As(err, &cse) {
		return exitCodeErr{code: cse.Code}
	}
	var se *setup.StepError
	if errors.As(err, &se) {
		return exitCodeErr{code: catalogExit(codeInternal)}
	}
	return err
}

// classifySetupTransport shapes a smoke-transport failure writeError
// classified: its row carries the full message with the doctor fix, and it
// exits with its own code. Anything writeError leaves internal crosses raw,
// whose row truncates and exits internal as before.
func classifySetupTransport(err error) error {
	var ce *cliError
	if coded := writeError(err); errors.As(coded, &ce) && ce.code != codeInternal {
		return &setup.CodedStepError{Message: ce.message, Next: "relevo doctor", Code: catalogExit(ce.code)}
	}
	return err
}

// setupServiceInstall installs the service unit as `relevo service install`
// does, shaped as a setup step failure: the row carries the install's own
// message and fix.
func setupServiceInstall() error {
	u, err := osUnitSpec()
	if err != nil {
		return &setup.StepError{Message: err.Error(), Next: catalog[codeInternal].next}
	}
	if _, err := installServiceUnit(u, newServiceRunner(), false); err != nil {
		var ce *cliError
		if errors.As(err, &ce) {
			return &setup.StepError{Message: ce.message, Next: ce.next}
		}
		return &setup.StepError{Message: text.FirstLine(err.Error())}
	}
	return nil
}

// inEnvSandbox reports an env sandbox: the sandbox moves XDG paths, not
// systemd's user manager or launchd, so host-owned installs stay out.
func inEnvSandbox() bool { return os.Getenv("RELEVO_SANDBOX") != "" }

func setupUnitPresent() bool {
	u, err := osUnitSpec()
	if err != nil {
		return false
	}
	_, _, present, rerr := u.read()
	return rerr == nil && present
}
