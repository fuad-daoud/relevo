package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// explicitBinding takes the binding from --name or a single positional, and
// never from the current directory. done is destructive and not undoable
// except through `relevo bind --resume`, so a bare invocation must fail rather
// than guess.
func explicitBinding(nameFlag string, positional []string) (string, bool) {
	switch {
	case nameFlag != "" && len(positional) == 0:
		return nameFlag, true
	case nameFlag == "" && len(positional) == 1:
		return positional[0], true
	default:
		return "", false
	}
}

// bindingHint names the binding for the current directory, if there is one, so
// a usage line can say what the caller probably meant. verb is the command
// being refused, so the suggestion is the one they actually wanted.
func bindingHint(verb string) string {
	rt, err := newRuntime()
	if err != nil {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	b, found, err := rt.Store.FindByCWD(cwd)
	if err != nil || !found {
		return ""
	}

	return fmt.Sprintf("\nthis directory is bound as %q, so you probably want: relevo %s %s", b.Name, verb, b.Name)
}

// withEnv sets key=val in env, replacing every existing KEY= entry (collapsing
// duplicates) or appending when none is present. It is how the re-exec passes
// the previous version on without duplicating an inherited value. Pure.
func withEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	set := false
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			if !set {
				out = append(out, prefix+val)
				set = true
			}
			continue
		}
		out = append(out, e)
	}
	if !set {
		out = append(out, prefix+val)
	}
	return out
}

// bindingArg picks the binding name out of --name and leftover positionals;
// empty means no name was given. It refuses even agreeing spellings:
// accepting one hides a mistaken model of the command.
func bindingArg(nameFlag string, positional []string) (string, error) {
	switch {
	case nameFlag != "" && len(positional) > 0:
		return "", fail(codeUsage, "binding named twice: --name %s and %q; pass it once", nameFlag, positional[0])
	case len(positional) > 1:
		return "", fail(codeUsage, "too many binding names: %v; pass one", positional)
	case nameFlag != "":
		return nameFlag, nil
	case len(positional) == 1:
		return positional[0], nil
	default:
		return "", nil
	}
}

// resolveBinding names the binding a command acts on: --name, a positional,
// or the binding owning the current directory. The positional form is not
// decoration: dropping it would act on the directory's binding instead.
func resolveBinding(rt relevo.Runtime, nameFlag string, positional []string) (string, error) {
	name, err := bindingArg(nameFlag, positional)
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	b, found, err := rt.Store.FindByCWD(cwd)
	if err != nil {
		return "", err
	}
	if !found {
		return "", failNext(codeBindingNotFound, "relevo bind",
			"no binding for %s; name one with `relevo <command> NAME` or run relevo bind first", cwd)
	}

	return b.Name, nil
}

// warnWaitingOnYou prints one stderr line per other binding waiting on a
// human. It never changes the caller's return: WaitingOnYou is itself only
// a stderr warning.
func warnWaitingOnYou(rt relevo.Runtime, except string) {
	lines, err := relevo.WaitingOnYou(rt, except)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: waiting-on-you check: %v\n", err)
		return
	}
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}

// parseFlags parses one subcommand's flags iteratively, since flag.Parse stops
// at the first non-flag argument; letting Parse do the work keeps this correct
// without knowing any flag's arity (`-h` is a clean exit; no `--` passthrough).
func parseFlags(fs *flag.FlagSet, args []string) error {
	var positional []string

	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return errHelpShown
			}
			return fail(codeUsage, "%v", err)
		}

		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	// Re-parse the positionals so fs.Args() reports them; Parse does not
	// reset already-set flag values.
	if err := fs.Parse(positional); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelpShown
		}
		return fail(codeUsage, "%v", err)
	}
	return nil
}

// regateFlag turns --regate into the *int the relevo package takes: the -1
// default means not given and becomes nil, a typed value must be >= 0, and a
// bad one exits 2 before any runtime is built.
func regateFlag(fs *flag.FlagSet, regate *int) (*int, error) {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "regate" {
			given = true
		}
	})
	if !given {
		return nil, nil
	}
	if *regate < 0 {
		return nil, fail(codeUsage, "--regate must be >= 0, got %d", *regate)
	}
	return regate, nil
}

// noteRegateNoGate says when a repair budget landed on a binding with no
// gate to fail: accepted and inert. w is stdout by default, stderr under
// --json.
func noteRegateNoGate(w io.Writer, b store.Binding) {
	if b.Regate > 0 && b.Gate == "" {
		fmt.Fprintf(w, "  regate %d (no gate configured)\n", b.Regate)
	}
}

// noticeWriter is where a write verb's supplementary lines go: stdout for the
// human default, unchanged; stderr under --json, where stdout carries the
// result document and nothing else.
func noticeWriter(jsonMode bool) io.Writer {
	if jsonMode {
		return os.Stderr
	}
	return os.Stdout
}

// writeError classifies a failure out of the relevo write paths into the
// frame's catalog code: the sentinels relevo and store export are the only
// distinctions, and anything else is internal. Coded errors pass through.
func writeError(err error) error {
	if err == nil {
		return nil
	}
	var ce *cliError
	if errors.As(err, &ce) {
		return err
	}
	var openMember *relevo.RoundOpenError
	var candOpen *relevo.CandidateRoundOpenError
	var envErr *relevo.EnvironmentError
	switch {
	// The busy family is checked before the bare refusal case: each sentinel
	// also unwraps to refusal.ErrRefused, and only the named reason picks the
	// next hint.
	case errors.Is(err, relevo.ErrBuilderBusy):
		return failNext(codeRefused, "relevo done", "%v", err)
	case errors.Is(err, relevo.ErrReportPending):
		return failNext(codeRefused, "relevo wait", "%v", err)
	case errors.Is(err, relevo.ErrScopeActive):
		return failNext(codeRefused, "relevo stop", "%v", err)
	case errors.Is(err, refusal.ErrRefused):
		return fail(codeRefused, "%v", err)
	case errors.Is(err, store.ErrInvalidName):
		// A binding name is the caller's own argument: a corrected --name is
		// the whole way out, so this is usage (exit 2), never internal.
		return fail(codeUsage, "%v", err)
	case errors.Is(err, store.ErrInvalidFeature), errors.Is(err, store.ErrInvalidTicket):
		// Same shape as a bad name: the flag value itself is what is wrong.
		return fail(codeUsage, "%v", err)
	case errors.Is(err, refusal.ErrBadInput):
		// A flag pair that cannot both hold, a flag the shape does not
		// honour, a name already taken: usage, not an internal failure.
		return fail(codeUsage, "%v", err)
	case errors.Is(err, refusal.ErrRoundNotFound):
		return fail(codeRoundNotFound, "%v", err)
	case errors.Is(err, store.ErrHandedOver), errors.Is(err, db.ErrWriterMoved):
		return fail(codeRefused, "%v", err)
	case errors.Is(err, store.ErrNotFound):
		return fail(codeBindingNotFound, "%v", err)
	case errors.Is(err, delivery.ErrAckForeignBinding):
		return fail(codeMastermindMismatch, "%v", err)
	case errors.Is(err, delivery.ErrAckUnknownSeq):
		return fail(codePushSeqNotFound, "%v", err)
	case errors.Is(err, delivery.ErrAckAlreadyConfirmed):
		return fail(codePushAlreadyConfirm, "%v", err)
	case errors.Is(err, delivery.ErrAckNotAdmitted):
		return fail(codePushNotAdmitted, "%v", err)
	case errors.Is(err, delivery.ErrAckNoClaim):
		return fail(codePushNoClaim, "%v", err)
	case errors.Is(err, relevo.ErrSeedOverCap):
		return failNext(codeUsage, "trim the seed or pass --force", "%v", err)
	case errors.Is(err, relevo.ErrRoundCap):
		// A binding that used every round its cap allows needs a fresh
		// builder; the cap is not an internal failure.
		return failWrap(codeRoundCap, err, "%v", err)
	case errors.Is(err, store.ErrCWDTaken), errors.Is(err, chain.ErrRunningMember), errors.Is(err, relevo.ErrChainRunning), errors.Is(err, relevo.ErrChainDone), errors.Is(err, chain.ErrChainHalted):
		// A halted chain's stop answer is about the chain's own state, so it
		// is the conflict family like its running and done siblings. The
		// message already names `relevo done`, so the catalog adds no hint.
		return fail(codeConflict, "%v", err)
	case errors.As(err, &openMember):
		return failNext(codeConflict, "relevo stop "+openMember.Member, "%v", err)
	case errors.As(err, &candOpen):
		return failNext(codeConflict, "relevo stop "+candOpen.Name, "%v", err)
	case errors.Is(err, resolve.ErrNoViablePlacement):
		return failNext(codePolicyRefused, "relevo config", "%v", err)
	case errors.Is(err, relevo.ErrNoMasterMindSession):
		return failNext(codeUsage, "relevo mastermind init", "%v", err)
	case errors.Is(err, resolve.ErrTierAboveMax), errors.Is(err, harness.ErrTierUnsupported):
		return fail(codeTierCap, "%v", err)
	case errors.Is(err, resolve.ErrAllGated):
		return fail(codeGateActive, "%v", err)
	case errors.Is(err, resolve.ErrCandidateCannotRun):
		return failNext(codePolicyRefused, "relevo doctor", "%v", err)
	case errors.Is(err, resolve.ErrNoCandidates), errors.Is(err, resolve.ErrRoleNotServed):
		// `relevo config` shows each actor's candidate list and the pick, which
		// is where a refused candidate gets added.
		return failNext(codePolicyRefused, "relevo config", "%v", err)
	case errors.Is(err, resolve.ErrAmbiguousCandidate),
		errors.Is(err, resolve.ErrUnknownRole),
		errors.Is(err, roles.ErrBadActors),
		errors.Is(err, roles.ErrBadRoles):
		return failNext(codePolicyRefused, "relevo config", "%v", err)
	case errors.Is(err, candidate.ErrUnknownCandidate),
		errors.Is(err, candidate.ErrBadRef),
		errors.Is(err, availability.ErrUnknownProvider):
		// A candidate, ref or provider named on the command line is the
		// caller's own argument, so a typo is usage and never `relevo
		// bugreport`. The message names the accepted forms.
		return fail(codeUsage, "%s", availability.UsageText(err))
	case errors.Is(err, client.ErrUnreachable):
		return fail(codeRemoteUnreachable, "%v", err)
	case errors.Is(err, errOwnerSandboxDenied):
		// The host's sandbox refused the owner socket: the verb, the daemon
		// and the socket are all fine and only the shell is not, so the
		// catalog names the unsandboxed shell rather than `relevo bugreport`.
		return fail(codeOwnerSandboxDenied, "%v", err)
	case errors.Is(err, relevo.ErrRemoteUnavailable):
		return fail(codeNotAvailable, "%v", err)
	case errors.As(err, &envErr):
		// A machine short of disk is the host's state, not a relevo bug:
		// doctor shows where the space went.
		return failNext(codeNotAvailable, "relevo doctor", "%v", err)
	default:
		return fail(codeInternal, "%v", err)
	}
}

// isTerminal reports whether f is a character device: the same
// os.ModeCharDevice check internal/ui/source.go uses for stdout.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// bareArgs is what a bare `relevo` runs: `ui` when stdin and stdout are both
// terminals, nil otherwise. Pure, so a test covers all four combinations
// without a terminal.
func bareArgs(stdinTTY, stdoutTTY bool) []string {
	if stdinTTY && stdoutTTY {
		return []string{"ui"}
	}
	return nil
}
