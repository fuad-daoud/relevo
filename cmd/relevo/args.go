package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// explicitBinding takes the binding from --name or a single positional, and
// never from the current directory. done is destructive and not undoable
// except through `relevo bind --resume`, so a bare invocation must fail rather
// than guess -- guessing has already ended one live loop by accident.
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

// firstLine is the first non-empty line of s, for a preflight failure's
// reason. It is what daemon.json and the log record, not the whole stderr.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
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

// bindingArg picks the binding name out of a --name flag and whatever
// positionals were left over, and refuses to take two. An empty return means
// no name was given at all, which each caller interprets for itself.
//
// It refuses even when both spellings agree: a caller who wrote the name twice
// has a mistaken model of the command, and silently accepting one of them hides
// that until the day the two differ.
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

// resolveBinding names the binding a command should act on: the one given by
// --name or as a positional, else the binding that owns the current directory,
// so the mastermind rarely has to name it at all.
//
// The positional form is not decoration. Before #50 these commands read --name
// only and dropped a positional on the floor, which from a bound directory sent
// `relevo send frontend --file p.md` to the CWD's builder instead of frontend's,
// with no error and a round log that recorded it as legitimate.
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

// warnWaitingOnYou prints one stderr line per other binding that is waiting
// on a human, per spec §4.9. except is the binding the verb just acted on.
// It never changes the caller's return value or exit code: a WaitingOnYou
// error is itself only a stderr warning.
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

// parseFlags parses one subcommand's flags. It turns `-h` into a clean exit:
// the flag package has already printed usage, so the caller just returns.
//
// It parses iteratively rather than once, because flag.Parse stops at the first
// non-flag argument -- which meant `relevo bind --from webshop --round 2` silently
// dropped --round, the exact form the README documents (#48). Each pass takes
// one leftover word as a positional and re-parses the remainder, so flags are
// found wherever they appear. Letting Parse do the work is what keeps this
// correct without knowing any flag's arity: Parse has already consumed a
// flag's value before the leftovers are looked at, so `--round 2` never leaves
// a stray "2" behind.
//
// There is no `--` passthrough anywhere in this CLI, so nothing here needs to
// stop early and treat a tail as literal.
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

	// Re-parse the collected positionals so fs.Args() reports them, leaving
	// every caller's `fs.Args()` working exactly as before. Parse stops at the
	// first non-flag argument and every element here is one, so this consumes
	// nothing and simply reinstates the list. Flag values already set by the
	// passes above survive: Parse does not reset them.
	if err := fs.Parse(positional); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelpShown
		}
		return fail(codeUsage, "%v", err)
	}
	return nil
}

// regateFlag turns --regate into the *int the relevo package takes (#132 part
// 2). The flag's default is -1, meaning "not given", which becomes nil so the
// policy default or the existing binding value stands; any value the human
// actually typed must be >= 0, and a bad one exits 2 -- before any runtime is
// built, so nothing touches the state directory or launches a harness.
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
		fmt.Fprintf(os.Stderr, "relevo: --regate must be >= 0, got %d\n", *regate)
		return nil, exitCodeErr{code: 2}
	}
	return regate, nil
}

// noteRegateNoGate says when a repair budget landed on a binding that has no
// gate to fail (#132 part 2): accepted and inert, because a binding with no
// gate never produces gate=fail, but not worth leaving unexplained.
func noteRegateNoGate(b store.Binding) {
	if b.Regate > 0 && b.Gate == "" {
		fmt.Printf("  regate %d (no gate configured)\n", b.Regate)
	}
}

// isTerminal reports whether f is a character device: the same
// os.ModeCharDevice check internal/ui/source.go uses for stdout.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// bareArgs is what a bare `relevo` runs: `ui` when stdin and stdout are both
// terminals, nil otherwise (round 3, step 3.1). Pure, so a test covers all
// four combinations without a terminal.
func bareArgs(stdinTTY, stdoutTTY bool) []string {
	if stdinTTY && stdoutTTY {
		return []string{"ui"}
	}
	return nil
}
