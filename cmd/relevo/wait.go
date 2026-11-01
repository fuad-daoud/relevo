package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// WaitDoc is `relevo wait --json`'s document: the binding, the round waited
// on, the protocol code (also the exit), the outcome line, and the pending
// payload. deliver_error carries a delivery failure that never changed the
// exit.
type WaitDoc struct {
	Name         string `json:"name"`
	Round        int    `json:"round"`
	Code         int    `json:"code"`
	Line         string `json:"line"`
	Payload      string `json:"payload,omitempty"`
	DeliverError string `json:"deliver_error,omitempty"`
}

// waitDocOf builds the document from the name and the result. It is pure, so
// the shape is pinned without a harness.
func waitDocOf(name string, res relevo.WaitResult) WaitDoc {
	doc := WaitDoc{Name: name, Round: res.Round, Code: res.Code, Line: res.Line, Payload: res.Payload}
	if res.DeliverErr != nil {
		doc.DeliverError = res.DeliverErr.Error()
	}
	return doc
}

type waitFlagValues struct {
	name    *string
	any     *bool
	round   *int
	timeout *time.Duration
	peek    *bool
	asJSON  *bool
}

func waitFlagSet(fs *flag.FlagSet) *waitFlagValues {
	v := &waitFlagValues{}
	v.name = fs.String("name", "", "binding name (default: the binding for this cwd)")
	v.any = fs.Bool("any", false, "wait on every named binding; the first to close or need you wins, its name printed first")
	v.round = fs.Int("round", 0, "round to wait on (default: the newest round sent; an earlier round answers from the log)")
	v.timeout = fs.Duration("timeout", 10*time.Minute, "how long to wait before giving up")
	v.peek = fs.Bool("peek", false, "print the outcome line only: do not deliver the pending report")
	v.asJSON = fs.Bool("json", false, "machine-readable output: the WaitDoc document")
	return v
}

// cmdWait blocks until a round closes or needs a human. It reads relevo's own
// state only; it launches nothing.
func cmdWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	v := waitFlagSet(fs)
	name, anyFlag := v.name, v.any
	round, timeout, peek := v.round, v.timeout, v.peek
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fail(codeUsage, "--timeout must be positive")
	}
	if *round < 0 {
		return fail(codeUsage, "--round must be >= 0")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	names, chain, err := resolveWaitNames(rt, *name, *anyFlag, fs.Args())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if chain != "" {
		return waitChain(ctx, rt, chain, *timeout, *peek, *asJSON)
	}

	resName, res, err := relevo.Wait(ctx, rt, relevo.WaitOptions{
		Names: names, Round: *round, Timeout: *timeout, Interval: time.Second, Peek: *peek,
	})
	if err != nil {
		return waitFailure(err)
	}

	// --json prints one WaitDoc and nothing else; the human form below is the
	// default and stays byte-identical.
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(waitDocOf(resName, res)); err != nil {
			return err
		}
	} else {
		if *anyFlag && resName != "" {
			fmt.Println(resName)
		}
		if res.Line != "" {
			fmt.Println(res.Line)
		}
		// After the outcome line, a blank line and the pending payload for every
		// exit but the timeout and a gone binding. A delivery failure is printed
		// and never changes the exit code.
		if res.DeliverErr != nil {
			fmt.Fprintf(os.Stderr, "relevo wait: round %d not delivered, it stays pending (the next relevo wait prints it): %v\n", res.Round, res.DeliverErr)
		}
		if res.Payload != "" {
			fmt.Println()
			fmt.Println(res.Payload)
		}
	}
	if res.Code == 0 {
		return nil
	}
	return exitCodeErr{res.Code}
}

// resolveWaitNames maps the name flags to the bindings to wait on. --any takes
// the positionals as the set; otherwise the one name resolves through the
// binding table, and a chain name waits on the chain itself. chain is empty
// unless the wait follows a chain.
func resolveWaitNames(rt relevo.Runtime, name string, anyFlag bool, positional []string) (names []string, chain string, err error) {
	if anyFlag {
		if name != "" || len(positional) == 0 {
			return nil, "", fail(codeUsage, "--any takes one or more positional names, not --name")
		}
		return positional, "", nil
	}
	target, err := resolveBinding(rt, name, positional)
	if err != nil {
		return nil, "", err
	}
	// A name that resolves to a chain waits on the chain -- unless the
	// chain is no longer running and its builder has a manual round open,
	// in which case the wait follows that round as any binding's would.
	binding, isChain, err := relevo.ChainWaitTarget(rt, target)
	if err != nil {
		return nil, "", waitFailure(err)
	}
	if isChain {
		chain = target
	}
	return []string{binding}, chain, nil
}

// waitFailure classifies a wait error: a missing binding is the caller's
// name to correct, anything else is internal.
func waitFailure(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fail(codeBindingNotFound, "%v", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fail(codeInternal, "wait was cancelled")
	}
	return fail(codeInternal, "%v", err)
}

// waitChain is cmdWait's chain arm: it blocks until the chain is no longer
// running. The protocol is the chain's own: 0 finished, 3 halted or stopped,
// 124 on the timeout.
func waitChain(ctx context.Context, rt relevo.Runtime, name string, timeout time.Duration, peek, asJSON bool) error {
	res, err := relevo.WaitChain(ctx, rt, name, timeout, time.Second, peek)
	if err != nil {
		return waitFailure(err)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(waitDocOf(name, res)); err != nil {
			return err
		}
	} else {
		if res.Line != "" {
			fmt.Println(res.Line)
		}
		if res.DeliverErr != nil {
			fmt.Fprintf(os.Stderr, "relevo wait: chain %s not delivered, it stays pending (the next relevo wait prints it): %v\n", name, res.DeliverErr)
		}
		if res.Payload != "" {
			fmt.Println()
			fmt.Println(res.Payload)
		}
	}
	if res.Code == 0 {
		return nil
	}
	return exitCodeErr{res.Code}
}
