package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// runStopChain is cmdStop's chain arm. A halted chain names `relevo done` as
// the command that closes it; its refusal prints, then returns as the coded
// failure a script can branch on.
func runStopChain(rt relevo.Runtime, name string, asJSON bool) error {
	res, err := relevo.ChainStop(context.Background(), rt, name)
	// A halted chain carries its own wording, so it is classified rather than
	// printed here: both shapes come out of the coded refusal.
	if errors.Is(err, chain.ErrChainHalted) {
		return writeError(err)
	}
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
	// The member had no open round, so the chain was stopped directly.
	if res.Action == relevo.ChainStopActionStopped {
		fmt.Printf("stopped chain %s: its awaited member had no open round\n", name)
		return nil
	}
	fmt.Println(relevo.StopText(name, res))
	return nil
}

// runDoneChain is cmdDone's chain arm: every member is released and the chain
// is marked done. A member whose stop failed still marked itself done, so the
// result prints before the failure is returned.
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

type doneFlagValues struct {
	name   *string
	pick   *bool
	asJSON *bool
}

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
	// A chain name releases every member, builder first, and the chain row
	// follows.
	if statusChain(rt, target) {
		return runDoneChain(rt, target, *asJSON)
	}
	res, err := relevo.Done(context.Background(), rt, target)
	if err != nil && !errors.Is(err, relevo.ErrStopFailed) {
		return writeError(err)
	}

	// A failed stop still marked the binding done, so the result prints before
	// the failure is returned.
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

type stopFlagValues struct {
	name   *string
	asJSON *bool
}

func stopFlagSet(fs *flag.FlagSet) *stopFlagValues {
	v := &stopFlagValues{}
	v.name = fs.String("name", "", "binding whose open round to stop")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

// cmdStop ends a binding's open round on purpose: a headless builder is
// killed now and the round closes without a report unless one is already
// on disk. It never takes the binding from the current directory -- a stop
// must not guess.
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
	// A chain name stops the chain, not one of its members: the awaited
	// member's open round ends.
	if statusChain(rt, target) {
		return runStopChain(rt, target, *asJSON)
	}
	res, err := relevo.Stop(context.Background(), rt, target, relevo.StopOptions{})
	if errors.Is(err, relevo.ErrNothingToStop) {
		// Nothing to stop is an answer, not a failure: its document names the
		// action "nothing".
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
