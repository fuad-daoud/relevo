package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/ui"
)

type serveClientsFlagValues struct {
	asJSON *bool
}

// The state flag has no pointer of its own: it is read back off the FlagSet.
func serveClientsFlagSet(fs *flag.FlagSet) *serveClientsFlagValues {
	v := &serveClientsFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the enrolled clients as JSON")
	_ = fs.String("state", "", "state directory")
	return v
}

func cmdServeClients(args []string) error {
	fs := flag.NewFlagSet("relevo serve clients", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveClientsFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	_, d, err := adminRoot(fs)
	if err != nil {
		return failNext(codeNotAvailable, "relevo serve init", "%v", err)
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d.LocalOrSelf())
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	list := clients.List()
	if *asJSON {
		if list == nil {
			list = []serve.Client{}
		}
		return printDoc(list)
	}

	fmt.Print(serve.RenderClients(list))
	return nil
}

type serveFingerprintFlagValues struct {
	asJSON *bool
}

// The state flag has no pointer of its own: it is read back off the FlagSet.
func serveFingerprintFlagSet(fs *flag.FlagSet) *serveFingerprintFlagValues {
	v := &serveFingerprintFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the fingerprint as a JSON document")
	_ = fs.String("state", "", "state directory")
	return v
}

func cmdServeFingerprint(args []string) error {
	fs := flag.NewFlagSet("relevo serve fingerprint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveFingerprintFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	_, d, err := adminRoot(fs)
	if err != nil {
		return failNext(codeNotAvailable, "relevo serve init", "%v", err)
	}
	defer func() { _ = d.Close() }()

	fp, err := serve.Fingerprint(serve.SecretStore{DB: d})
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	if *asJSON {
		return printDoc(fingerprintDoc{Fingerprint: fp})
	}
	fmt.Println(fp)
	return nil
}

// Both flags are read back off the FlagSet; the verb always prints JSON.
func serveStatusFlagSet(fs *flag.FlagSet) {
	_ = fs.String("state", "", "state directory")
	_ = fs.Bool("json", false, "print the census as JSON")
}

func cmdServeStatus(args []string) error {
	fs := flag.NewFlagSet("relevo serve status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	serveStatusFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	root, d, err := adminRoot(fs)
	if err != nil {
		return failNext(codeNotAvailable, "relevo serve init", "%v", err)
	}
	defer func() { _ = d.Close() }()

	cfg, err := serveAdminConfigWithPolicy(root, d)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	owners, builders, err := serve.AdminStatus(context.Background(), srv)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(serve.StatusDocument(owners, builders))
}

type serveUIFlagValues struct {
	interval *time.Duration
}

// The state flag has no pointer of its own: it is read back off the FlagSet.
func serveUIFlagSet(fs *flag.FlagSet) *serveUIFlagValues {
	v := &serveUIFlagValues{}
	_ = fs.String("state", "", "state directory")
	v.interval = fs.Duration("interval", 0, "poll interval (0 uses the ui default)")
	return v
}

func cmdServeUI(args []string) error {
	fs := flag.NewFlagSet("relevo serve ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveUIFlagSet(fs)
	interval := v.interval
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// The root resolves before any tty check, so an uninitialised --state
	// dir reads as a state error and not a terminal one.
	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	srv, err := serve.New(serveAdminConfig(root, d))
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return ui.RunSource(ctx, ui.ServerSource(srv), ui.Options{
		Interval: *interval,
		Prefs: ui.PrefsStore{
			KV:  srv.DB().LocalOrSelf(),
			Key: "serve.ui",
		},
		PipeHint: "relevo serve ui needs a terminal; use relevo serve status when piping",
		Version:  buildVersion(),
	})
}

// serveGateList lists the gates on the server-wide ledger, the serve root's
// ledger rather than the caller's.
func serveGateList(fs *flag.FlagSet, asJSON bool) error {
	root, d, err := adminRoot(fs)
	if err != nil {
		return failNext(codeNotAvailable, "relevo serve init", "%v", err)
	}
	defer func() { _ = d.Close() }()

	cfg, err := serveAdminConfigWithCandidates(root, d)
	if err != nil {
		return fail(codeInternal, "relevo gate --serve: %v", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return fail(codeInternal, "relevo gate --serve: %v", err)
	}

	gates := serve.AdminGates(srv)
	if asJSON {
		return printDoc(gateRowsOf(gates))
	}
	fmt.Print(serve.RenderGates(gates, time.Now()))
	return nil
}

// serveGateClear lifts the server-side gate on a provider, in place, with
// no forwarding: the verb for the box that runs the daemon.
func serveGateClear(fs *flag.FlagSet, subject string, asJSON bool) error {
	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	cfg, err := serveAdminConfigWithCandidates(root, d)
	if err != nil {
		return fmt.Errorf("relevo gate --serve: %w", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	provider, removed, err := serve.AdminAvailable(srv, subject)
	if err != nil {
		return err
	}

	// The document is the same GateDoc the client ledger prints; the counting
	// rule is the one the server's own gating line uses.
	if asJSON {
		return printDoc(gateClearDocOf(provider, serveCandidateCount(cfg, provider), removed))
	}

	if removed == 0 {
		fmt.Printf("nothing was gating %s\n", provider)
		return nil
	}
	fmt.Printf("cleared %s (%d entries)\n", provider, removed)
	return nil
}

// serveGateUnavailable records a server-side gate: the server ledger's
// counterpart to gateUnavailable, with no daemon-switch line and no
// forwarding (the gate is already on the ledger the daemon reads).
func serveGateUnavailable(fs *flag.FlagSet, token, forFlag, reason string, asJSON bool) error {
	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	cfg, err := serveAdminConfigWithCandidates(root, d)
	if err != nil {
		return fmt.Errorf("relevo gate --serve: %w", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	until, err := parseFor(forFlag, time.Now())
	if err != nil {
		return fail(codeUsage, "%v", err)
	}

	provider, err := serve.AdminUnavailable(srv, token, until, reason)
	if err != nil {
		return err
	}

	if asJSON {
		return printDoc(gateSetDocOf(provider, until, serveCandidateCount(cfg, provider)))
	}

	fmt.Printf("gated %s (%d candidates) %s\n", provider, serveCandidateCount(cfg, provider), availability.GateUntilText(until))
	return nil
}

// serveCandidateCount counts the configured candidates a provider serves on a
// serve root's own config: the number its gating line and document carry.
func serveCandidateCount(cfg serve.Config, provider string) int {
	count := 0
	if cfg.Candidates == nil {
		return count
	}
	for _, ref := range cfg.Candidates.Refs() {
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
