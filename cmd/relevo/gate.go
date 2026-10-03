package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
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

// gateUntil is when a manually recorded gate expires: --for when the caller
// gave one, and otherwise the reset the --reason names, if it names one this
// parser trusts. "RESOURCE_EXHAUSTED 429: ... Resets in 51m30s" is the provider
// stating the moment the limit lifts, so a gate recorded from it ends then
// rather than sitting until a human clears it. A reason naming no reset keeps
// the until-cleared default (the zero time): relevo does not invent an expiry
// no provider ever stated.
func gateUntil(forFlag, reason string, now time.Time) (time.Time, error) {
	until, err := parseFor(forFlag, now)
	if err != nil {
		return time.Time{}, err
	}
	if until.IsZero() {
		if reset, ok := availability.ResetFromReason(reason, now); ok {
			return reset, nil
		}
	}
	return until, nil
}

// cmdGate is one verb for the gate operations that used to be four: the
// top-level unavailable and available, and the serve gates|available|unavailable
// subverbs (§4.3). With no positional it lists the active
// gates; a positional gates a provider; --clear lifts a gate; --serve sends
// the same three forms to the local serve daemon's own ledger.
// gateFlagValues holds the pointers gate parses into. state is read back off
// the FlagSet by the --serve route, so it has no pointer of its own.
type gateFlagValues struct {
	forFlag   *string
	reason    *string
	clear     *string
	serveFlag *bool
	asJSON    *bool
}

// gateFlagSet defines those flags on fs, in the usage text's order, and
// returns what they parse into. state has no pointer of its own: the --serve
// route reads it back off the FlagSet.
func gateFlagSet(fs *flag.FlagSet) *gateFlagValues {
	v := &gateFlagValues{}
	v.forFlag = fs.String("for", "", "how long to gate the provider, as a Go `duration` (e.g. 2h); omit to leave it gated until relevo gate --clear")
	v.reason = fs.String("reason", "", "why, for the record")
	v.clear = fs.String("clear", "", "clear a recorded rate limit: --clear <group|group@account|provider|token>")
	v.serveFlag = fs.Bool("serve", false, "act on the local serve daemon's gates instead of this machine's")
	v.asJSON = fs.Bool("json", false, "print the gates as JSON")
	_ = fs.String("state", "", "with --serve: state directory")
	return v
}

func cmdGate(args []string) error {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	v := gateFlagSet(fs)
	forFlag, reason, clear, serveFlag, asJSON := v.forFlag, v.reason, v.clear, v.serveFlag, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if *serveFlag {
		return gateServe(fs, positional, *forFlag, *reason, *clear, *asJSON)
	}
	switch {
	case *clear != "":
		return gateClear(*clear, *asJSON)
	case len(positional) == 1:
		return gateUnavailable(positional[0], *forFlag, *reason, *asJSON)
	case len(positional) == 0 && *forFlag == "" && *reason == "":
		return gateList(*asJSON)
	default:
		return fail(codeUsage, "usage: relevo gate [<token> [--for D] [--reason S]] | --clear <provider|token> | --serve")
	}
}

// gateList prints this machine's active gates: the rendering `relevo serve
// gates` printed for the serve root's ledger (§4.3), fed by relevo.Gates, or
// the same ledger as a JSON document.
func gateList(asJSON bool) error {
	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	gates := availability.Gates(relevo.AvailabilityDeps(rt), gateAccounts(rt))
	if asJSON {
		return printDoc(gateRowsOf(gates))
	}
	fmt.Print(serve.RenderGates(gates, rt.Now()))
	return nil
}

// gateUnavailable records a provider rate limit: exactly today's
// cmdUnavailable (§4.3).
func gateUnavailable(token, forFlag, reason string, asJSON bool) error {
	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}

	until, err := gateUntil(forFlag, reason, rt.Now())
	if err != nil {
		return fail(codeUsage, "%v", err)
	}

	// The argument may be a candidate name or a token. It is resolved here,
	// first, so the ledger and every server the bindings name see the
	// canonical token, never the raw argument (A1 §4.2).
	c, err := rt.Candidates.Resolve(token)
	if err != nil {
		return writeError(err)
	}
	canonical := c.Ref().String()

	deps := relevo.AvailabilityDeps(rt)
	keys, accounts := gateKeys(rt, deps, canonical, c.Ref().Provider)

	provider, err := availability.Unavailable(deps, canonical, until, reason, keys...)
	if err != nil {
		return writeError(err)
	}

	count := providerCandidateCount(rt, provider)

	// The gating line and the document both read provider, until and count, so
	// they can never disagree; the daemon-switch line is a notice, which --json
	// moves to stderr (§2.1).
	notices := noticeWriter(asJSON)
	if asJSON {
		if perr := printDoc(gateSetDocOf(provider, until, count)); perr != nil {
			return perr
		}
	} else {
		fmt.Printf("gated %s (%d candidates) %s\n", provider, count, availability.GateUntilText(until))
	}
	if len(accounts) > 0 {
		fmt.Fprintf(notices, "gated accounts: %s\n", strings.Join(accounts, ", "))
	}

	if bs, err := rt.Store.List(); err == nil {
		if names := availability.BindingsOnProvider(bs, provider); len(names) > 0 {
			fmt.Fprintf(notices, "the daemon will switch: %s\n", strings.Join(names, ", "))
		}
	}

	for _, line := range relevo.ForwardUnavailable(context.Background(), rt, canonical, reason) {
		fmt.Fprintln(os.Stderr, line)
	}

	return nil
}

// gateAccounts reads the accounts the config loaded, or nil when there is no
// config store to read: a host with no accounts behaves exactly as before.
func gateAccounts(rt relevo.Runtime) account.Set {
	if rt.Config == nil {
		return nil
	}
	L, err := rt.Config.Load()
	if err != nil {
		return nil
	}
	return L.Accounts
}

// gateKeys resolves the accounts `gate <token>` records and the names to print:
// the account every open round on token draws from, or the account the pick
// would use now. Both nil on a host with no accounts, so the gate stays a bare
// group key exactly as before.
func gateKeys(rt relevo.Runtime, deps availability.Deps, token, provider string) (keys, names []string) {
	accounts := gateAccounts(rt)
	if len(accounts) == 0 || deps.Gates == nil || rt.Store == nil {
		return nil, nil
	}
	l, err := availability.LoadLedger(deps.Gates)
	if err != nil {
		return nil, nil
	}
	bs, err := rt.Store.List()
	if err != nil {
		return nil, nil
	}
	names = availability.GateAccountsFor(token, bs, accounts, availability.LiveGateKeys(l, deps.Now()), account.Mode(rt.Policy.AccountsRotation()))
	for _, n := range names {
		keys = append(keys, account.GateKey(provider, n))
	}
	return keys, names
}

// providerCandidateCount counts the configured candidates a provider serves:
// the number the gating line and the gate document both carry.
func providerCandidateCount(rt relevo.Runtime, provider string) int {
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
	return count
}

// gateClear lifts a recorded rate limit locally and on every server the
// bindings name: exactly today's cmdAvailable (§4.3).
func gateClear(subject string, asJSON bool) error {
	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}

	provider, removed, err := availability.Available(relevo.AvailabilityDeps(rt), subject, availability.ClearedByMasterMind)
	if err != nil {
		return writeError(err)
	}

	// The clear's document carries the same fields as the set's, plus removed,
	// which is what tells "nothing was gating X" from "cleared X (N)".
	notices := noticeWriter(asJSON)
	if asJSON {
		if perr := printDoc(gateClearDocOf(provider, providerCandidateCount(rt, provider), removed)); perr != nil {
			return perr
		}
	} else if removed == 0 {
		fmt.Printf("nothing was gating %s\n", provider)
	} else {
		fmt.Printf("cleared %s (%d entries)\n", provider, removed)
	}

	// The local clear is done either way; every server the bindings name is
	// asked, and each answer is printed -- these are answers, not warnings.
	ctx := context.Background()
	for _, line := range relevo.ForwardAvailable(ctx, rt, subject) {
		fmt.Fprintln(notices, line)
	}

	// On a box that also runs a serve daemon, the client ledger just cleared
	// is not the record that gates anything: a serve daemon keeps its own
	// gates, in its own database (P3b plan §4.5).
	if d, _, err := openMachineDB(); err == nil {
		defer d.Close()
		if p, ok, _ := serve.ReadDaemonPointer(d); ok && pidAlive(p.PID) {
			fmt.Fprint(notices, "note: a relevo serve daemon runs here with its own gates; use relevo gate --serve\n")
		}
	}

	return nil
}

// gateServe sends the three gate forms to the local serve daemon's own
// ledger: today's `relevo serve gates`, `relevo serve unavailable` and
// `relevo serve available`, via the serve root's ledgerRuntime (§4.3).
func gateServe(fs *flag.FlagSet, positional []string, forFlag, reason, clear string, asJSON bool) error {
	switch {
	case clear != "":
		return serveGateClear(fs, clear, asJSON)
	case len(positional) == 1:
		return serveGateUnavailable(fs, positional[0], forFlag, reason, asJSON)
	case len(positional) == 0 && forFlag == "" && reason == "":
		return serveGateList(fs, asJSON)
	default:
		return fail(codeUsage, "usage: relevo gate --serve [--state DIR] [<token> [--for D] [--reason S] | --clear <provider|token>]")
	}
}
