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

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/ui"
)

// serveInitFlagValues holds the pointer `serve init` parses into. state is
// read back off the FlagSet by serveRoot, so it has no pointer of its own.
type serveInitFlagValues struct {
	hosts *hostSlice
}

// serveInitFlagSet defines those flags on fs and returns what they parse into.
func serveInitFlagSet(fs *flag.FlagSet) *serveInitFlagValues {
	v := &serveInitFlagValues{hosts: &hostSlice{}}
	fs.Var(v.hosts, "host", "hostname or IP to include in certificate SANs")
	_ = fs.String("state", "", "state directory")
	return v
}

func cmdServeInit(args []string) error {
	fs := flag.NewFlagSet("relevo serve init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveInitFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
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
	secrets := serve.SecretStore{DB: d}

	fp, err := serve.InitTLS(secrets, *v.hosts, time.Now())
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

// serveEnrollFlagValues holds the pointers `serve enroll` parses into. state
// is read back off the FlagSet, so it has no pointer of its own.
type serveEnrollFlagValues struct {
	label *string
	key   *string
}

// serveEnrollFlagSet defines those flags on fs and returns what they parse
// into.
func serveEnrollFlagSet(fs *flag.FlagSet) *serveEnrollFlagValues {
	v := &serveEnrollFlagValues{}
	v.label = fs.String("label", "", "client label")
	v.key = fs.String("key", "", "client ed25519 public key line")
	_ = fs.String("state", "", "state directory")
	return v
}

func cmdServeEnroll(args []string) error {
	fs := flag.NewFlagSet("relevo serve enroll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveEnrollFlagSet(fs)
	label, key := v.label, v.key
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *label == "" || *key == "" {
		fmt.Fprintln(os.Stderr, "usage: relevo serve enroll --label <label> --key \"<ed25519 line>\" [--state <dir>]")
		return exitCodeErr{code: 2}
	}

	d, _, err := openMachineDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d)
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

// serveClientsFlagSet declares `serve clients`'s flags: --state only, read
// back off the FlagSet.
func serveClientsFlagSet(fs *flag.FlagSet) {
	_ = fs.String("state", "", "state directory")
}

func cmdServeClients(args []string) error {
	fs := flag.NewFlagSet("relevo serve clients", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	serveClientsFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	_, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d)
	if err != nil {
		return err
	}

	fmt.Print(serve.RenderClients(clients.List()))
	return nil
}

// serveRevokeFlagSet declares `serve revoke`'s flags: --state only.
func serveRevokeFlagSet(fs *flag.FlagSet) {
	_ = fs.String("state", "", "state directory")
}

func cmdServeRevoke(args []string) error {
	fs := flag.NewFlagSet("relevo serve revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	serveRevokeFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo serve revoke <id> [--state <dir>]")
		return exitCodeErr{code: 2}
	}

	id := fs.Arg(0)
	_, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d)
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

// serveFingerprintFlagSet declares `serve fingerprint`'s flags: --state only.
func serveFingerprintFlagSet(fs *flag.FlagSet) {
	_ = fs.String("state", "", "state directory")
}

func cmdServeFingerprint(args []string) error {
	fs := flag.NewFlagSet("relevo serve fingerprint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	serveFingerprintFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	_, d, err := adminRoot(fs)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	fp, err := serve.Fingerprint(serve.SecretStore{DB: d})
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve fingerprint: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Println(fp)
	return nil
}

// serveStatusFlagSet declares `serve status`'s flags: --state and --json,
// both read back off the FlagSet (the verb always prints JSON).
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

// serveUIFlagValues holds the pointer `serve ui` parses into. state is read
// back off the FlagSet, so it has no pointer of its own.
type serveUIFlagValues struct {
	interval *time.Duration
}

// serveUIFlagSet defines those flags on fs and returns what they parse into.
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
			KV:  srv.DB(),
			Key: "serve.ui",
		},
		PipeHint: "relevo serve ui needs a terminal; use relevo serve status when piping",
		Version:  buildVersion(),
	})
}

// serveUnbindFlagValues holds the pointers `serve unbind` parses into. state
// is read back off the FlagSet, so it has no pointer of its own.
type serveUnbindFlagValues struct {
	owner *string
	force *bool
}

// serveUnbindFlagSet defines those flags on fs and returns what they parse
// into.
func serveUnbindFlagSet(fs *flag.FlagSet) *serveUnbindFlagValues {
	v := &serveUnbindFlagValues{}
	v.owner = fs.String("owner", "", "client label or id")
	v.force = fs.Bool("force", false, "unbind even if the round is running")
	_ = fs.String("state", "", "state directory")
	return v
}

func cmdServeUnbind(args []string) error {
	fs := flag.NewFlagSet("relevo serve unbind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveUnbindFlagSet(fs)
	owner, force := v.owner, v.force
	if err := parseFlags(fs, args); err != nil {
		return err
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

// serveGCFlagValues holds the pointers `serve gc` parses into. state is read
// back off the FlagSet, so it has no pointer of its own.
type serveGCFlagValues struct {
	abandoned *string
	dryRun    *bool
}

// serveGCFlagSet defines those flags on fs and returns what they parse into.
func serveGCFlagSet(fs *flag.FlagSet) *serveGCFlagValues {
	v := &serveGCFlagValues{}
	v.abandoned = fs.String("abandoned", "", "abandoned duration threshold")
	v.dryRun = fs.Bool("dry-run", false, "dry run without unbinding")
	_ = fs.String("state", "", "state directory")
	return v
}

func cmdServeGC(args []string) error {
	fs := flag.NewFlagSet("relevo serve gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveGCFlagSet(fs)
	abandoned, dryRun := v.abandoned, v.dryRun
	if err := parseFlags(fs, args); err != nil {
		return err
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
