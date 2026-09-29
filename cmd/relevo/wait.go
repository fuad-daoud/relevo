package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// cmdWait blocks until a round closes or needs a human, per spec
// docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §4.8. It reads
// relevo's own state only: it launches nothing.
func cmdWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	anyFlag := fs.Bool("any", false, "wait on every named binding; the first to close or need you wins, its name printed first")
	round := fs.Int("round", 0, "round to wait on (default: the newest round sent; an earlier round answers from the log)")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait before giving up")
	peek := fs.Bool("peek", false, "print the outcome line only: do not deliver the pending report")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("relevo wait --timeout must be positive")
	}
	if *round < 0 {
		return fmt.Errorf("relevo wait --round must be >= 0")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	var names []string
	if *anyFlag {
		if *name != "" || len(fs.Args()) == 0 {
			return fmt.Errorf("usage: relevo wait --any NAME [NAME...]  (--any takes one or more positional names, not --name)")
		}
		names = fs.Args()
	} else {
		target, err := resolveBinding(rt, *name, fs.Args())
		if err != nil {
			return err
		}
		names = []string{target}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	resName, res, err := relevo.Wait(ctx, rt, relevo.WaitOptions{
		Names: names, Round: *round, Timeout: *timeout, Interval: time.Second, Peek: *peek,
	})
	if err != nil {
		return err
	}

	if *anyFlag && resName != "" {
		fmt.Println(resName)
	}
	if res.Line != "" {
		fmt.Println(res.Line)
	}
	// §4.1: after the outcome line, a blank line and the pending payload --
	// the same text `pull` printed -- for every exit but the timeout and a
	// gone binding. A delivery failure is printed and never changes the exit
	// code (§6).
	if res.DeliverErr != nil {
		fmt.Fprintf(os.Stderr, "relevo wait: round %d not delivered, it stays pending (the next relevo wait prints it): %v\n", res.Round, res.DeliverErr)
	}
	if res.Payload != "" {
		fmt.Println()
		fmt.Println(res.Payload)
	}
	if res.Code == 0 {
		return nil
	}
	return exitCodeErr{res.Code}
}
