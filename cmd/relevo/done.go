package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// doneFlagValues holds the pointers done parses into.
type doneFlagValues struct {
	name *string
	pick *bool
}

// doneFlagSet defines those flags on fs and returns what they parse into.
func doneFlagSet(fs *flag.FlagSet) *doneFlagValues {
	v := &doneFlagValues{}
	v.name = fs.String("name", "", "binding to mark done")
	v.pick = fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	return v
}

func cmdDone(args []string) error {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	v := doneFlagSet(fs)
	name, pickFlag := v.name, v.pick
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
		return fmt.Errorf("usage: relevo done <name> | --pick  (or --name <name>)%s\n"+
			"done stops relaying for a binding; it will not guess which one you meant",
			bindingHint("done"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	res, err := relevo.Done(context.Background(), rt, target)
	if err != nil && !errors.Is(err, relevo.ErrStopFailed) {
		return err
	}

	fmt.Println(relevo.DoneText(target, res))
	if err != nil {
		return err
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
	name *string
}

// stopFlagSet defines that flag on fs and returns what it parses into.
func stopFlagSet(fs *flag.FlagSet) *stopFlagValues {
	v := &stopFlagValues{}
	v.name = fs.String("name", "", "binding whose open round to stop")
	return v
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	v := stopFlagSet(fs)
	name := v.name
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relevo stop <name> | --name <name>\n" +
			"stop kills the builder process and closes its round; it must not guess")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	res, err := relevo.Stop(context.Background(), rt, target, relevo.StopOptions{})
	if errors.Is(err, relevo.ErrNothingToStop) {
		// Nothing to stop is an answer, not a failure.
		fmt.Printf("nothing to stop: %s has no open round\n", target)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Println(relevo.StopText(target, res))
	return nil
}
