package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fuad-daoud/relevo/internal/delivery"
)

// pushFlagValues holds the pointer push parses into.
type pushFlagValues struct {
	mastermind *string
}

// pushFlagSet defines push's flags on fs and returns what they parse into.
func pushFlagSet(fs *flag.FlagSet) *pushFlagValues {
	v := &pushFlagValues{}
	v.mastermind = fs.String("mastermind", "", "mastermind id or name (default: $RELEVO_MASTERMIND, else this session's host)")
	return v
}

// cmdPush runs relevo push: it resolves this MasterMind like every verb, then
// holds its push claim and drains its mailbox to stdout as NDJSON, confirming
// each entry on an "ack <seq>" line from stdin. A refused claim (ErrClaimHeld)
// means another push already holds it: this process exits without draining.
func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	v := pushFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	deps := delivery.Deps{
		Store:       rt.Store,
		Now:         rt.Now,
		Channels:    rt.Channels,
		Deliverers:  rt.Deliverers,
		MasterMinds: rt.MasterMinds,
	}
	if err := delivery.RunPush(ctx, deps, rec.ID, os.Stdin, os.Stdout); err != nil {
		if errors.Is(err, delivery.ErrClaimHeld) {
			fmt.Fprintf(os.Stderr, "relevo push: %v\n", err)
			return exitCodeErr{code: 1}
		}
		return err
	}
	return nil
}
