package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/serve"
)

// parseFor turns --for into an absolute expiry. Empty means "until cleared"
// (a zero time); anything else must be a positive Go duration -- relevo does
// not know a provider's reset schedule, so it never invents one (spec §1).
func parseFor(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--for %q: %w", s, err)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("--for must be a positive duration, got %q", s)
	}
	return now.Add(d), nil
}

// cmdGate is one verb for the gate operations that used to be four: the
// top-level unavailable and available, and the serve gates|available|unavailable
// subverbs (§4.3). With no positional it lists the active
// gates; a positional gates a provider; --clear lifts a gate; --serve sends
// the same three forms to the local serve daemon's own ledger.
func cmdGate(args []string) error {
	const gateUsage = `usage: relevo gate
       relevo gate <token> [--for D] [--reason S]
       relevo gate --clear <provider|token>
       relevo gate --serve [--state DIR] [<token> [--for D] [--reason S] | --clear <provider|token>]`

	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	forFlag := fs.String("for", "", "how long to gate the provider, as a Go `duration` (e.g. 2h); omit to leave it gated until relevo gate --clear")
	reason := fs.String("reason", "", "why, for the record")
	clear := fs.String("clear", "", "clear a recorded rate limit: --clear <provider|token>")
	serveFlag := fs.Bool("serve", false, "act on the local serve daemon's gates instead of this machine's")
	_ = fs.String("state", "", "with --serve: state directory")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if *serveFlag {
		return gateServe(fs, positional, *forFlag, *reason, *clear)
	}
	switch {
	case *clear != "":
		return gateClear(*clear)
	case len(positional) == 1:
		return gateUnavailable(positional[0], *forFlag, *reason)
	case len(positional) == 0 && *forFlag == "" && *reason == "":
		return gateList()
	default:
		fmt.Fprintln(os.Stderr, gateUsage)
		return exitCodeErr{code: 2}
	}
}

// gateList prints this machine's active gates: the rendering `relevo serve
// gates` printed for the serve root's ledger (§4.3), fed by relevo.Gates.
func gateList() error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}
	fmt.Print(serve.RenderGates(availability.Gates(relevo.AvailabilityDeps(rt)), rt.Now()))
	return nil
}

// gateUnavailable records a provider rate limit: exactly today's
// cmdUnavailable (§4.3).
func gateUnavailable(token, forFlag, reason string) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	until, err := parseFor(forFlag, rt.Now())
	if err != nil {
		return err
	}

	// The argument may be a candidate name or a token. It is resolved here,
	// first, so the ledger and every server the bindings name see the
	// canonical token, never the raw argument (A1 §4.2).
	c, err := rt.Candidates.Resolve(token)
	if err != nil {
		return err
	}
	canonical := c.Ref().String()

	provider, err := availability.Unavailable(relevo.AvailabilityDeps(rt), canonical, until, reason)
	if err != nil {
		return err
	}

	count := 0
	for _, ref := range rt.Candidates.Refs() {
		parsed, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		if parsed.Provider == provider {
			count++
		}
	}

	fmt.Printf("gated %s (%d candidates) %s\n", provider, count, availability.GateUntilText(until))

	if bs, err := rt.Store.List(); err == nil {
		if names := availability.BindingsOnProvider(bs, provider); len(names) > 0 {
			fmt.Printf("the daemon will switch: %s\n", strings.Join(names, ", "))
		}
	}

	for _, line := range relevo.ForwardUnavailable(context.Background(), rt, canonical, reason) {
		fmt.Fprintln(os.Stderr, line)
	}

	return nil
}

// gateClear lifts a recorded rate limit locally and on every server the
// bindings name: exactly today's cmdAvailable (§4.3).
func gateClear(subject string) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	provider, removed, err := availability.Available(relevo.AvailabilityDeps(rt), subject, availability.ClearedByPlanner)
	if err != nil {
		return err
	}

	if removed == 0 {
		fmt.Printf("nothing was gating %s\n", provider)
	} else {
		fmt.Printf("cleared %s (%d entries)\n", provider, removed)
	}

	// The local clear is done either way; every server the bindings name is
	// asked, and each answer is printed -- these are answers, not warnings.
	ctx := context.Background()
	for _, line := range relevo.ForwardAvailable(ctx, rt, subject) {
		fmt.Println(line)
	}

	// On a box that also runs a serve daemon, the client ledger just cleared
	// is not the record that gates anything: a serve daemon keeps its own
	// gates, in the serve root's database (P3b plan §4.5). The pointer lives
	// under the serve root (#372 §4.6): the client root's daemon.json is
	// DaemonInfo, which decodes as a pointer with a live pid.
	if defRoot, err := defaultServeRoot(); err == nil {
		if p, ok, _ := serve.ReadPointer(defRoot); ok && pidAlive(p.PID) {
			fmt.Print("note: a relevo serve daemon runs here with its own gates; use relevo gate --serve\n")
		}
	}

	return nil
}

// gateServe sends the three gate forms to the local serve daemon's own
// ledger: today's `relevo serve gates`, `relevo serve unavailable` and
// `relevo serve available`, via the serve root's ledgerRuntime (§4.3).
func gateServe(fs *flag.FlagSet, positional []string, forFlag, reason, clear string) error {
	switch {
	case clear != "":
		return serveGateClear(fs, clear)
	case len(positional) == 1:
		return serveGateUnavailable(fs, positional[0], forFlag, reason)
	case len(positional) == 0 && forFlag == "" && reason == "":
		return serveGateList(fs)
	default:
		return fmt.Errorf("usage: relevo gate --serve [--state DIR] [<token> [--for D] [--reason S] | --clear <provider|token>]")
	}
}
