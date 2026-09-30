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

// WaitDoc is `relevo wait --json`'s document: the binding the wait resolved,
// the round it waited on, the protocol code (which is also the exit), the
// outcome line, and -- when one was delivered -- the pending payload. The
// deliver_error field carries a delivery failure that never changed the exit.
type WaitDoc struct {
	Name         string `json:"name"`
	Round        int    `json:"round"`
	Code         int    `json:"code"`
	Line         string `json:"line"`
	Payload      string `json:"payload,omitempty"`
	DeliverError string `json:"deliver_error,omitempty"`
}

// waitDocOf builds the document from the name wait resolved and the result it
// classified. It is a pure function, so the shape is pinned without a harness.
func waitDocOf(name string, res relevo.WaitResult) WaitDoc {
	doc := WaitDoc{Name: name, Round: res.Round, Code: res.Code, Line: res.Line, Payload: res.Payload}
	if res.DeliverErr != nil {
		doc.DeliverError = res.DeliverErr.Error()
	}
	return doc
}

// cmdWait blocks until a round closes or needs a human, per spec
// docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §4.8. It reads
// relevo's own state only: it launches nothing.
// waitFlagValues holds the pointers wait parses into.
type waitFlagValues struct {
	name    *string
	any     *bool
	round   *int
	timeout *time.Duration
	peek    *bool
	asJSON  *bool
}

// waitFlagSet defines those flags on fs and returns what they parse into.
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

	var names []string
	chain := ""
	if *anyFlag {
		if *name != "" || len(fs.Args()) == 0 {
			return fail(codeUsage, "--any takes one or more positional names, not --name")
		}
		names = fs.Args()
	} else {
		target, err := resolveBinding(rt, *name, fs.Args())
		if err != nil {
			return err
		}
		// A name that is a chain waits on the chain, not on a binding's round:
		// the caller asked about the whole chain, and its end is one event.
		if _, cerr := rt.Store.Chain(target); cerr == nil {
			chain = target
		} else if !errors.Is(cerr, store.ErrNotFound) {
			return fail(codeInternal, "%v", cerr)
		}
		names = []string{target}
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
		return fail(codeInternal, "%v", err)
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
	}
	if res.Code == 0 {
		return nil
	}
	return exitCodeErr{res.Code}
}

// waitChain is cmdWait's chain arm: a name that is a chain blocks until the
// chain is no longer running, then prints the chain's end line and, unless
// --peek, the one end delivery it queued. The protocol is the chain's own: 0
// finished, 3 halted or stopped, 124 on the timeout.
func waitChain(ctx context.Context, rt relevo.Runtime, name string, timeout time.Duration, peek, asJSON bool) error {
	res, err := relevo.WaitChain(ctx, rt, name, timeout, time.Second, peek)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fail(codeBindingNotFound, "%v", err)
		}
		return fail(codeInternal, "%v", err)
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
