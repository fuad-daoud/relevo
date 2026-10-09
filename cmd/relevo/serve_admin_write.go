package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
)

// serveInitFlagValues holds the pointers `serve init` parses into. state is
// read back off the FlagSet by serveRoot, so it has no pointer of its own.
type serveInitFlagValues struct {
	hosts  *hostSlice
	asJSON *bool
}

// serveInitFlagSet defines those flags on fs and returns what they parse into.
func serveInitFlagSet(fs *flag.FlagSet) *serveInitFlagValues {
	v := &serveInitFlagValues{hosts: &hostSlice{}}
	fs.Var(v.hosts, "host", "hostname or IP to include in certificate SANs")
	_ = fs.String("state", "", "state directory")
	v.asJSON = fs.Bool("json", false, "print the document the init produced")
	return v
}

func cmdServeInit(args []string) error {
	return outcomeError(cmdServeInitRun(args))
}

func cmdServeInitRun(args []string) error {
	fs := flag.NewFlagSet("relevo serve init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveInitFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
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

	// A user-mode server keeps its state at the fixed root-owned directory and
	// needs the traversable 0711 modes; every other mode keeps the owner-only
	// state root it always had.
	L, err := loadConfig(d)
	if err != nil {
		return err
	}
	if m, perr := isolate.Parse(L.Policy.ServeIsolation()); perr == nil && m == isolate.ModeUser {
		if fs.Lookup("state").Value.String() == "" {
			root = serve.UserModeRoot
		}
		if err := serve.EnsureUserModeRoot(root); err != nil {
			return err
		}
	} else if err := serve.EnsureStateRoot(root); err != nil {
		return err
	}

	secrets := serve.SecretStore{DB: d}

	fp, err := serve.InitTLS(secrets, *v.hosts, time.Now())
	if errors.Is(err, serve.ErrTLSExists) {
		existingFP, fpErr := serve.Fingerprint(secrets)
		if fpErr != nil {
			return fpErr
		}
		if *asJSON {
			return printDoc(serveInitDocOf(root, existingFP, false))
		}
		fmt.Printf("already initialised; fingerprint %s\n", existingFP)
		return nil
	}
	if err != nil {
		return err
	}

	if *asJSON {
		fmt.Fprintln(os.Stderr, `clients: run relevo serve enroll --label <who> --key "<their relevo config server key line>"`)
		return printDoc(serveInitDocOf(root, fp, true))
	}
	fmt.Printf("fingerprint %s\n", fp)
	fmt.Println(`clients: run relevo serve enroll --label <who> --key "<their relevo config server key line>"`)
	return nil
}

// serveEnrollFlagValues holds the pointers `serve enroll` parses into. state
// is read back off the FlagSet, so it has no pointer of its own.
type serveEnrollFlagValues struct {
	label  *string
	key    *string
	user   *string
	asJSON *bool
}

// serveEnrollFlagSet defines those flags on fs and returns what they parse
// into.
func serveEnrollFlagSet(fs *flag.FlagSet) *serveEnrollFlagValues {
	v := &serveEnrollFlagValues{}
	v.label = fs.String("label", "", "client label")
	v.key = fs.String("key", "", "client ed25519 public key line")
	v.user = fs.String("user", "", "unix user this owner's builders run as (user mode)")
	_ = fs.String("state", "", "state directory")
	v.asJSON = fs.Bool("json", false, "print the document the enroll produced")
	return v
}

func cmdServeEnroll(args []string) error {
	return outcomeError(cmdServeEnrollRun(args))
}

func cmdServeEnrollRun(args []string) error {
	fs := flag.NewFlagSet("relevo serve enroll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveEnrollFlagSet(fs)
	label, key, asJSON, unixUser := v.label, v.key, v.asJSON, v.user
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *label == "" || *key == "" {
		return fail(codeUsage, "serve enroll wants --label <label> --key \"<ed25519 line>\" [--user <unix user>] [--state <dir>]")
	}

	// A declared user is checked against this host before anything is written:
	// an unknown name is refused with the exact command that creates it.
	if *unixUser != "" {
		if _, err := serve.CheckUnixUser(lookupUnixUser, *unixUser); err != nil {
			return err
		}
	}

	d, _, err := openMachineDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d.LocalOrSelf())
	if err != nil {
		return err
	}

	cl, err := clients.Add(*label, *key, *unixUser, time.Now())
	if err != nil {
		return err
	}

	if *asJSON {
		return printDoc(serveEnrollDocOf(cl.Label, string(cl.ID)))
	}
	fmt.Printf("enrolled %s %s\n", cl.Label, cl.ID)
	return nil
}

// serveRevokeFlagValues holds the pointer `serve revoke` parses into. state is
// read back off the FlagSet by adminRoot, so it has no pointer of its own.
type serveRevokeFlagValues struct {
	asJSON *bool
}

// serveRevokeFlagSet defines that flag on fs and returns what it parses into.
func serveRevokeFlagSet(fs *flag.FlagSet) *serveRevokeFlagValues {
	v := &serveRevokeFlagValues{}
	_ = fs.String("state", "", "state directory")
	v.asJSON = fs.Bool("json", false, "print the document the revoke produced")
	return v
}

func cmdServeRevoke(args []string) error {
	return outcomeError(cmdServeRevokeRun(args))
}

func cmdServeRevokeRun(args []string) error {
	fs := flag.NewFlagSet("relevo serve revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveRevokeFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fail(codeUsage, "serve revoke wants <id> [--state <dir>]")
	}

	id := fs.Arg(0)
	_, d, err := adminRoot(fs)
	if err != nil {
		return failNext(codeNotAvailable, "relevo serve init", "%v", err)
	}
	defer func() { _ = d.Close() }()

	clients, err := serve.LoadClients(d.LocalOrSelf())
	if err != nil {
		return err
	}

	if err := clients.Revoke(remote.ClientID(id), time.Now()); err != nil {
		return err
	}
	if *asJSON {
		return printDoc(serveRevokeDocOf(id))
	}
	return nil
}

// serveUnbindFlagValues holds the pointers `serve unbind` parses into. state
// is read back off the FlagSet, so it has no pointer of its own.
type serveUnbindFlagValues struct {
	owner  *string
	force  *bool
	asJSON *bool
}

// serveUnbindFlagSet defines those flags on fs and returns what they parse
// into.
func serveUnbindFlagSet(fs *flag.FlagSet) *serveUnbindFlagValues {
	v := &serveUnbindFlagValues{}
	v.owner = fs.String("owner", "", "client label or id")
	v.force = fs.Bool("force", false, "unbind even if the round is running")
	_ = fs.String("state", "", "state directory")
	v.asJSON = fs.Bool("json", false, "print the document the unbind produced")
	return v
}

func cmdServeUnbind(args []string) error {
	return outcomeError(cmdServeUnbindRun(args))
}

func cmdServeUnbindRun(args []string) error {
	fs := flag.NewFlagSet("relevo serve unbind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveUnbindFlagSet(fs)
	owner, force, asJSON := v.owner, v.force, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *owner == "" || fs.NArg() < 1 {
		return fail(codeUsage, "serve unbind wants --owner <label|id> <name> [--state <dir>] [--force]")
	}

	name := fs.Arg(0)
	root, d, err := adminRoot(fs)
	if err != nil {
		return failNext(codeNotAvailable, "relevo serve init", "%v", err)
	}
	defer func() { _ = d.Close() }()

	srv, err := serve.New(serveAdminConfig(root, d))
	if err != nil {
		return err
	}

	res, err := serve.AdminUnbind(context.Background(), srv, *owner, name, *force)
	if err != nil {
		// A round still running on the binding is a state conflict the human
		// can resolve with --force; anything else is classified as it comes.
		if strings.Contains(err.Error(), "is running") {
			return failWrap(codeConflict, err, "%v", err)
		}
		return err
	}

	if *asJSON {
		return printDoc(serveUnbindDocOf(*owner, name, res))
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
	asJSON    *bool
}

// serveGCFlagSet defines those flags on fs and returns what they parse into.
func serveGCFlagSet(fs *flag.FlagSet) *serveGCFlagValues {
	v := &serveGCFlagValues{}
	v.abandoned = fs.String("abandoned", "", "abandoned duration threshold")
	v.dryRun = fs.Bool("dry-run", false, "dry run without unbinding")
	_ = fs.String("state", "", "state directory")
	v.asJSON = fs.Bool("json", false, "print the document the sweep produced")
	return v
}

func cmdServeGC(args []string) error {
	return outcomeError(cmdServeGCRun(args))
}

func cmdServeGCRun(args []string) error {
	fs := flag.NewFlagSet("relevo serve gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serveGCFlagSet(fs)
	abandoned, dryRun, asJSON := v.abandoned, v.dryRun, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *abandoned == "" {
		return fail(codeUsage, "serve gc wants --abandoned <duration> [--dry-run] [--state <dir>]")
	}

	dur, err := time.ParseDuration(*abandoned)
	if err != nil {
		return fail(codeUsage, "serve gc: invalid duration %q: %v", *abandoned, err)
	}

	root, d, err := adminRoot(fs)
	if err != nil {
		return failNext(codeNotAvailable, "relevo serve init", "%v", err)
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

	if *asJSON {
		return printDoc(serveGCDocOf(*dryRun, results))
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
