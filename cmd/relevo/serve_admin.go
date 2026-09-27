package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/ui"
)

func cmdServeInit(args []string) error {
	fs := flag.NewFlagSet("relevo serve init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var hosts hostSlice
	fs.Var(&hosts, "host", "hostname or IP to include in certificate SANs")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	if err := serve.EnsureStateRoot(root); err != nil {
		return err
	}

	d, _, err := openMachineDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	secrets := serve.SecretStore{DB: d, Root: root}

	fp, err := serve.InitTLS(secrets, hosts, time.Now())
	if errors.Is(err, serve.ErrTLSExists) {
		existingFP, fpErr := serve.Fingerprint(secrets)
		if fpErr != nil {
			return fpErr
		}
		fmt.Printf("already initialised; fingerprint %s\n", existingFP)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Printf("fingerprint %s\n", fp)
	fmt.Println(`clients: run relevo serve enroll --label <who> --key "<their relevo config server key line>"`)
	return nil
}

func cmdServeEnroll(args []string) error {
	fs := flag.NewFlagSet("relevo serve enroll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	label := fs.String("label", "", "client label")
	key := fs.String("key", "", "client ed25519 public key line")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *label == "" || *key == "" {
		fmt.Fprintln(os.Stderr, "usage: relevo serve enroll --label <label> --key \"<ed25519 line>\" [--state <dir>]")
		return exitCodeErr{code: 2}
	}

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	d, _, err := openMachineDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d, filepath.Join(root, "clients.json"))
	if err != nil {
		return err
	}

	cl, err := clients.Add(*label, *key, time.Now())
	if errors.Is(err, serve.ErrAlreadyEnrolled) {
		fmt.Fprintf(os.Stderr, "relevo serve enroll: client already enrolled: %s\n", *label)
		return exitCodeErr{code: 1}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve enroll: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("enrolled %s %s\n", cl.Label, cl.ID)
	return nil
}

func cmdServeClients(args []string) error {
	fs := flag.NewFlagSet("relevo serve clients", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d, filepath.Join(root, "clients.json"))
	if err != nil {
		return err
	}

	fmt.Print(serve.RenderClients(clients.List()))
	return nil
}

func cmdServeRevoke(args []string) error {
	fs := flag.NewFlagSet("relevo serve revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo serve revoke <id> [--state <dir>]")
		return exitCodeErr{code: 2}
	}

	id := fs.Arg(0)
	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d, filepath.Join(root, "clients.json"))
	if err != nil {
		return err
	}

	if err := clients.Revoke(remote.ClientID(id), time.Now()); err != nil {
		if errors.Is(err, serve.ErrNoSuchClient) {
			fmt.Fprintf(os.Stderr, "relevo serve revoke: no such client %s\n", id)
			return exitCodeErr{code: 1}
		}
		return err
	}
	return nil
}

func cmdServeFingerprint(args []string) error {
	fs := flag.NewFlagSet("relevo serve fingerprint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	fp, err := serve.Fingerprint(serve.SecretStore{DB: d, Root: root})
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve fingerprint: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Println(fp)
	return nil
}

func cmdServeStatus(args []string) error {
	fs := flag.NewFlagSet("relevo serve status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	_ = fs.Bool("json", false, "print the census as JSON")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	cfg, err := serveAdminConfigWithPolicy(root, d)
	if err != nil {
		return err
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	owners, builders, err := serve.AdminStatus(context.Background(), srv)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(serve.StatusDocument(owners, builders))
}

func cmdServeUI(args []string) error {
	fs := flag.NewFlagSet("relevo serve ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	interval := fs.Duration("interval", 0, "poll interval (0 uses the ui default)")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
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
			KV:         srv.DB(),
			Key:        "serve.ui",
			LegacyPath: filepath.Join(root, "ui.json"),
		},
		PipeHint: "relevo serve ui needs a terminal; use relevo serve status when piping",
		Version:  buildVersion(),
	})
}

func cmdServeUnbind(args []string) error {
	fs := flag.NewFlagSet("relevo serve unbind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	owner := fs.String("owner", "", "client label or id")
	force := fs.Bool("force", false, "unbind even if the round is running")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *owner == "" || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo serve unbind --owner <label|id> <name> [--state <dir>] [--force]")
		return exitCodeErr{code: 2}
	}

	name := fs.Arg(0)
	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	srv, err := serve.New(serveAdminConfig(root, d))
	if err != nil {
		return err
	}

	res, err := serve.AdminUnbind(context.Background(), srv, *owner, name, *force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve unbind: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("unbound %s/%s\n", *owner, name)
	if txt := relevo.UnbindText(name, res); txt != "" {
		fmt.Println(txt)
	}
	return nil
}

func cmdServeGC(args []string) error {
	fs := flag.NewFlagSet("relevo serve gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	abandoned := fs.String("abandoned", "", "abandoned duration threshold")
	dryRun := fs.Bool("dry-run", false, "dry run without unbinding")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *abandoned == "" {
		fmt.Fprintln(os.Stderr, "relevo serve gc: --abandoned <duration> is required")
		return exitCodeErr{code: 2}
	}

	dur, err := time.ParseDuration(*abandoned)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve gc: invalid duration %q: %v\n", *abandoned, err)
		return exitCodeErr{code: 2}
	}

	root, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	srv, err := serve.New(serveAdminConfig(root, d))
	if err != nil {
		return err
	}

	results, err := serve.GCAbandoned(context.Background(), srv, dur, time.Now(), *dryRun)
	if err != nil {
		return err
	}

	for _, r := range results {
		if *dryRun {
			fmt.Printf("%s  %s  abandoned (last seen %s)\n", r.Label, r.Name, r.LastSeen.Format("2006-01-02 15:04:05"))
		} else {
			fmt.Printf("%s  %s  archived\n", r.Label, r.Name)
		}
	}
	return nil
}

// serveGateList lists the gates on the server-wide ledger: `relevo serve
// gates` was the answer to `relevo gate` printing "nothing was gating" on a
// box whose gates live on the serve root's ledger, not the caller's (§4.3).
func serveGateList(fs *flag.FlagSet) error {
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

	fmt.Print(serve.RenderGates(serve.AdminGates(srv), time.Now()))
	return nil
}

// serveGateClear lifts the server-side gate on a provider, in place, with
// no forwarding: the verb for the box that runs the daemon (§4.3).
func serveGateClear(fs *flag.FlagSet, subject string) error {
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

	if removed == 0 {
		fmt.Printf("nothing was gating %s\n", provider)
		return nil
	}
	fmt.Printf("cleared %s (%d entries)\n", provider, removed)
	return nil
}

// serveGateUnavailable records a server-side gate: the server ledger's
// counterpart to gateUnavailable, with no daemon-switch line and no forwarding
// (the gate is already on the ledger the daemon reads) (§4.3).
func serveGateUnavailable(fs *flag.FlagSet, token, forFlag, reason string) error {
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
		return err
	}

	provider, err := serve.AdminUnavailable(srv, token, until, reason)
	if err != nil {
		return err
	}

	count := 0
	for _, ref := range cfg.Candidates.Refs() {
		parsed, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		if parsed.Provider == provider {
			count++
		}
	}

	fmt.Printf("gated %s (%d candidates) %s\n", provider, count, availability.GateUntilText(until))
	return nil
}
