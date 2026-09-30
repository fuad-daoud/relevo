package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// runStopChain is cmdStop's chain arm: the name is a chain, so the chain's own
// stop runs. A chain that is not running is the same "nothing to stop" answer a
// binding with no open round gives, and it changes nothing.
func runStopChain(rt relevo.Runtime, name string, asJSON bool) error {
	res, err := relevo.ChainStop(context.Background(), rt, name)
	if errors.Is(err, relevo.ErrNothingToStop) {
		if asJSON {
			return printDoc(chainStopDocOf(name, res))
		}
		fmt.Printf("nothing to stop: chain %s is not running\n", name)
		return nil
	}
	if err != nil {
		return writeError(err)
	}

	if asJSON {
		return printDoc(chainStopDocOf(name, res))
	}
	// The member had no open round, so the chain was stopped directly rather
	// than by a member's close; StopText's wording is about a round.
	if res.Action == relevo.ChainStopActionStopped {
		fmt.Printf("stopped chain %s: its awaited member had no open round\n", name)
		return nil
	}
	fmt.Println(relevo.StopText(name, res))
	return nil
}

// runDoneChain is cmdDone's chain arm: the name is a chain, so every member is
// released and the chain is marked done. A running chain is refused with the
// conflict that says to stop it first. A member whose process could not be
// stopped still marked itself done, so the result prints before the failure is
// returned, exactly as the binding path does.
func runDoneChain(rt relevo.Runtime, name string, asJSON bool) error {
	res, err := relevo.ChainDone(context.Background(), rt, name)
	if err != nil && !errors.Is(err, relevo.ErrStopFailed) {
		return writeError(err)
	}

	if asJSON {
		if perr := printDoc(chainDoneDocOf(name, res)); perr != nil {
			return perr
		}
	} else {
		fmt.Println(relevo.DoneText(name, res))
	}
	if err != nil {
		return writeError(err)
	}
	return nil
}

// doneFlagValues holds the pointers done parses into.
type doneFlagValues struct {
	name   *string
	pick   *bool
	asJSON *bool
}

// doneFlagSet defines those flags on fs and returns what they parse into.
func doneFlagSet(fs *flag.FlagSet) *doneFlagValues {
	v := &doneFlagValues{}
	v.name = fs.String("name", "", "binding to mark done")
	v.pick = fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

func cmdDone(args []string) error {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	v := doneFlagSet(fs)
	name, pickFlag, asJSON := v.name, v.pick, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbDone})
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fail(codeUsage, "usage: relevo done <name> | --pick  (or --name <name>)%s\n"+
			"done stops relaying for a binding; it will not guess which one you meant",
			bindingHint("done"))
	}

	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}
	// A name that resolves to a chain releases the whole chain: every member
	// is marked done, builder first, and the chain row follows.
	if statusChain(rt, target) {
		return runDoneChain(rt, target, *asJSON)
	}
	res, err := relevo.Done(context.Background(), rt, target)
	if err != nil && !errors.Is(err, relevo.ErrStopFailed) {
		return writeError(err)
	}

	// A failed process stop still marked the binding done, so the result is
	// printed before the failure is returned -- as the document under --json.
	if *asJSON {
		if perr := printDoc(doneDocOf(target, res)); perr != nil {
			return perr
		}
	} else {
		fmt.Println(relevo.DoneText(target, res))
	}
	if err != nil {
		return writeError(err)
	}
	warnWaitingOnYou(rt, target)
	return nil
}

// cmdStop ends a binding's open round on purpose (#138): a headless builder
// is killed now and the round closes without a report unless one is already
// on disk. It takes the binding from --name or a positional and never from
// the current directory -- a stop ends a round, so it must not guess.
// stopFlagValues holds the pointer stop parses into.
type stopFlagValues struct {
	name   *string
	asJSON *bool
}

// stopFlagSet defines that flag on fs and returns what it parses into.
func stopFlagSet(fs *flag.FlagSet) *stopFlagValues {
	v := &stopFlagValues{}
	v.name = fs.String("name", "", "binding whose open round to stop")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	v := stopFlagSet(fs)
	name, asJSON := v.name, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fail(codeUsage, "usage: relevo stop <name> | --name <name>\n"+
			"stop kills the builder process and closes its round; it must not guess")
	}

	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}
	// A name that resolves to a chain stops the chain, not one of its members:
	// the member the chain awaits is the one whose open round ends, and its
	// stopped close is what marks the chain stopped.
	if statusChain(rt, target) {
		return runStopChain(rt, target, *asJSON)
	}
	res, err := relevo.Stop(context.Background(), rt, target, relevo.StopOptions{})
	if errors.Is(err, relevo.ErrNothingToStop) {
		// Nothing to stop is an answer, not a failure: its document names the
		// action "nothing" rather than leaving an empty one.
		if *asJSON {
			return printDoc(stopDocOf(target, res))
		}
		fmt.Printf("nothing to stop: %s has no open round\n", target)
		return nil
	}
	if err != nil {
		return writeError(err)
	}

	if *asJSON {
		return printDoc(stopDocOf(target, res))
	}
	fmt.Println(relevo.StopText(target, res))
	return nil
}
