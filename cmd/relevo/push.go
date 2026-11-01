package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/fuad-daoud/relevo/internal/delivery"
)

type pushFlagValues struct {
	mastermind *string
	ack        *string
	asJSON     *bool
}

// pushFlagSet defines push's flags, so cmdPush and the tests read one flag
// surface.
func pushFlagSet(fs *flag.FlagSet) *pushFlagValues {
	v := &pushFlagValues{}
	v.mastermind = fs.String("mastermind", "", "mastermind id or name (default: $RELEVO_MASTERMIND, else this session's host)")
	v.ack = fs.String("ack", "", "confirm this binding's entry with this seq, then exit")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

// pushAckArgs is the parsed `--ack <binding> <seq>` shape. ok is false with a
// usage error attached whenever the ack shape is wrong.
func pushAckArgs(v *pushFlagValues, positional []string) (binding string, seq int, ok bool, err error) {
	if *v.ack == "" {
		if len(positional) > 0 {
			return "", 0, false, fail(codeUsage, "relevo push takes no positional arguments; --ack <binding> <seq> confirms an entry")
		}
		return "", 0, false, nil
	}
	if len(positional) != 1 {
		return "", 0, false, fail(codeUsage, "--ack takes exactly one seq, got %d arguments", len(positional))
	}
	n, convErr := strconv.Atoi(positional[0])
	if convErr != nil {
		return "", 0, false, fail(codeUsage, "seq %q is not an integer", positional[0])
	}
	return *v.ack, n, true, nil
}

// cmdPush holds this MasterMind's push claim and drains its mailbox to stdout
// as NDJSON. With --ack it confirms one entry and exits silently; a refused
// claim means another push already holds it.
func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	v := pushFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// The --ack shape is checked before any runtime is built, so a wrong
	// invocation touches no state directory.
	binding, seq, acking, err := pushAckArgs(v, fs.Args())
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	rec, _, err := resolveMCPMasterMind(rt, *v.mastermind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo push: %v\n", err)
		return exitCodeErr{code: 2}
	}

	deps := delivery.Deps{
		Store:       rt.Store,
		Now:         rt.Now,
		Channels:    rt.Channels,
		Deliverers:  rt.Deliverers,
		MasterMinds: rt.MasterMinds,
	}
	if acking {
		res, ackErr := delivery.AckPush(deps, rec.ID, binding, seq)
		if ackErr != nil {
			return writeError(ackErr)
		}
		// Without --json the ack stays silent and exits 0: the holder reads the
		// confirmation off the log, not off stdout.
		if *v.asJSON {
			return printDoc(pushAckDocOf(res))
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := delivery.RunPush(ctx, deps, rec.ID, os.Stdout); err != nil {
		if errors.Is(err, delivery.ErrClaimHeld) {
			fmt.Fprintf(os.Stderr, "relevo push: %v\n", err)
			return exitCodeErr{code: 1}
		}
		return err
	}
	return nil
}
