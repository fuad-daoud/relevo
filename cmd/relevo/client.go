package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/uplink"
)

type clientAddServerFlagValues struct {
	fingerprint *string
	ca          *string
	insecure    *bool
	asJSON      *bool
}

// clientAddServerFlagSet defines those flags on fs, in the usage text's order,
// and returns what they parse into, so the registry's parity test walks the
// same surface the verb does.
func clientAddServerFlagSet(fs *flag.FlagSet) *clientAddServerFlagValues {
	v := &clientAddServerFlagValues{}
	v.fingerprint = fs.String("fingerprint", "", "pin the server's certificate fingerprint (sha256:<hex>)")
	v.ca = fs.String("ca", "", `trust the system CA pool instead of pinning ("system")`)
	v.insecure = fs.Bool("insecure", false, "allow plain http (no TLS)")
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	return v
}

// cmdClientAddServer records a server entry, checking enrollment once when
// --fingerprint pins it. A 401 is not an error, and a missing client key is
// generated first, since the check cannot succeed without it.
func cmdClientAddServer(args []string) error {
	return outcomeError(cmdClientAddServerRun(args))
}

// parseServerAddArgs validates the add positionals and trust flags into the
// server name and entry: exactly one trust mode, and a well-formed entry.
func parseServerAddArgs(rest []string, v *clientAddServerFlagValues) (string, remote.ServerEntry, error) {
	if len(rest) != 2 {
		return "", remote.ServerEntry{}, fail(codeUsage, "config server add wants <name> <url> (--fingerprint sha256:... | --ca system | --insecure)")
	}
	name, rawURL := rest[0], rest[1]

	set := 0
	if *v.fingerprint != "" {
		set++
	}
	if *v.ca != "" {
		set++
	}
	if *v.insecure {
		set++
	}
	if set > 1 {
		return "", remote.ServerEntry{}, fail(codeUsage, "--fingerprint, --ca and --insecure are mutually exclusive")
	}

	entry := remote.ServerEntry{URL: rawURL, Fingerprint: *v.fingerprint, CA: *v.ca, Insecure: *v.insecure}
	if err := remote.ValidateEntry(entry); err != nil {
		return "", remote.ServerEntry{}, fail(codeUsage, "%v", err)
	}
	return name, entry, nil
}

// checkServerEnrollment proves the new server accepts our key: a 401 is not
// an error, and the enrollment lines name what the server admin needs.
func checkServerEnrollment(servers remote.Servers, name, rawURL string, pem []byte, w io.Writer, asJSON bool) error {
	key, err := remote.ParsePrivate(pem)
	if err != nil {
		return err
	}

	c := client.New(servers, key, time.Now)
	who, werr := c.WhoAmI(context.Background(), name)
	if werr != nil {
		var httpErr *client.HTTPError
		if errors.As(werr, &httpErr) && httpErr.Status == 401 {
			if _, err := fmt.Fprintf(w, "not enrolled on %s: give the admin: %s\n", name, client.EnrollLine(key)); err != nil {
				return fmt.Errorf("write server report: %w", err)
			}
			return addServerDoc(asJSON, name, rawURL)
		}
		return fmt.Errorf("%s: %w", name, werr)
	}
	if _, err := fmt.Fprintf(w, "enrolled as %s\n", who.Label); err != nil {
		return fmt.Errorf("write server report: %w", err)
	}
	return addServerDoc(asJSON, name, rawURL)
}

func cmdClientAddServerRun(args []string) error {
	fs := flag.NewFlagSet("relevo config server add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := clientAddServerFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// The human lines keep stdout in the default mode and move to stderr under
	// --json, where stdout carries the document alone.
	w := noticeWriter(*v.asJSON)

	name, entry, err := parseServerAddArgs(fs.Args(), v)
	if err != nil {
		return err
	}
	rawURL := entry.URL

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// The key comes first: the enrollment line printed once is the one the
	// server admin needs before the enrollment check below can succeed.
	pem, generated, err := ensureClientKey(rt)
	if err != nil {
		return err
	}
	if generated {
		if err := printClientKey(w, pem, false); err != nil {
			return err
		}
	}

	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	servers := L.Servers
	servers[name] = entry
	body, err := client.EncodeServers(servers)
	if err != nil {
		return err
	}
	if _, err := rt.Config.As("cli", "config server add "+name).Put(config.Servers, body); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "added server %s (%s)\n", name, rawURL); err != nil {
		return fmt.Errorf("write server report: %w", err)
	}

	if entry.Fingerprint == "" {
		// Pinned by --ca or plain --insecure: there is no fingerprint to pin
		// the transport to, so there is nothing to enrollment-check yet.
		return addServerDoc(*v.asJSON, name, rawURL)
	}
	return checkServerEnrollment(servers, name, rawURL, pem, w, *v.asJSON)
}

// addServerDoc prints config server add's document in --json mode, or nothing
// in the human default, whose lines were the whole answer.
func addServerDoc(asJSON bool, name, url string) error {
	if !asJSON {
		return nil
	}
	return printDoc(configServerAddDocOf(name, url))
}

type clientRmServerFlagValues struct {
	retire *bool
	dryRun *bool
	asJSON *bool
}

// clientRmServerFlagSet defines those flags on fs and returns what they
// parse into, so the registry's parity test walks the same surface the verb
// does.
func clientRmServerFlagSet(fs *flag.FlagSet) *clientRmServerFlagValues {
	v := &clientRmServerFlagValues{}
	v.retire = fs.Bool("retire", false, "archive the server's bindings locally instead of refusing while any names it")
	v.dryRun = fs.Bool("dry-run", false, "with --retire: list what would be archived, change nothing")
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	return v
}

// cmdClientRmServer forgets a configured server, or archives its bindings
// first under --retire.
func cmdClientRmServer(args []string) error {
	return outcomeError(cmdClientRmServerRun(args))
}

func cmdClientRmServerRun(args []string) error {
	fs := flag.NewFlagSet("relevo config server rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := clientRmServerFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *v.dryRun && !*v.retire {
		return fail(codeUsage, "--dry-run needs --retire")
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config server rm wants <name>")
	}
	name := rest[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if *v.retire {
		return retireServerRun(rt, name, *v.dryRun, *asJSON)
	}
	return plainServerRmRun(rt, name, *asJSON)
}

// plainServerRmRun forgets a configured server while nothing still names it;
// uplink.ServerInUse is the pure rule this checks, so this wrapper needs no
// harness or network to test the refusal shape.
func plainServerRmRun(rt relevo.Runtime, name string, asJSON bool) error {
	bindings, err := rt.Store.List()
	if err != nil {
		return err
	}
	if inUse := uplink.ServerInUse(bindings, name); len(inUse) > 0 {
		return failNext(codeConflict, "relevo config server rm "+name+" --retire", "server %q is used by %s; unbind them first", name, strings.Join(inUse, ", "))
	}

	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	servers := L.Servers
	if _, ok := servers[name]; !ok {
		return fail(codeServerNotFound, "no such server %q", name)
	}
	delete(servers, name)
	body, err := client.EncodeServers(servers)
	if err != nil {
		return err
	}
	if _, err := rt.Config.As("cli", "config server rm "+name).Put(config.Servers, body); err != nil {
		return err
	}
	if asJSON {
		return printDoc(configServerRmDocOf(name, nil))
	}
	fmt.Printf("removed server %s\n", name)
	return nil
}

// retireServerRun archives every binding naming a dead server without
// contacting it, then forgets the entry. A dry run lists the plan and
// changes nothing.
func retireServerRun(rt relevo.Runtime, name string, dryRun, asJSON bool) error {
	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	if _, ok := L.Servers[name]; !ok {
		return fail(codeServerNotFound, "no such server %q", name)
	}
	res, err := relevo.RetireServerBindings(context.Background(), rt, name, dryRun)
	if dryRun {
		if err != nil {
			return err
		}
		if perr := printRetirePlan(res.Plan, asJSON); perr != nil {
			return perr
		}
		if len(res.Plan.Refusals) > 0 {
			return retireRefusalsError(name, res.Plan)
		}
		if asJSON {
			return printDoc(configServerRmDocOf(name, res.Plan.Archive))
		}
		return nil
	}
	if len(res.Plan.Refusals) > 0 {
		return retireRefusalsError(name, res.Plan)
	}
	if err != nil {
		landed := strings.Join(res.Archived, ", ")
		if landed == "" {
			landed = "nothing"
		}
		return failWrap(codeInternal, err, "archived %s; server %q is kept, rerun relevo config server rm %s --retire to archive the rest", landed, name, name)
	}
	servers := L.Servers
	delete(servers, name)
	body, err := client.EncodeServers(servers)
	if err != nil {
		return err
	}
	if _, err := rt.Config.As("cli", "config server rm "+name).Put(config.Servers, body); err != nil {
		return err
	}
	if asJSON {
		return printDoc(configServerRmDocOf(name, res.Archived))
	}
	fmt.Printf("removed server %s (%d bindings archived)\n", name, len(res.Archived))
	return nil
}

// printRetirePlan lists what a retire would archive and what holds it back,
// on stderr under --json where stdout carries the document alone.
func printRetirePlan(plan relevo.RetirePlan, asJSON bool) error {
	w := noticeWriter(asJSON)
	for _, archived := range plan.Archive {
		if _, err := fmt.Fprintf(w, "would archive %s\n", archived); err != nil {
			return fmt.Errorf("write retire report: %w", err)
		}
	}
	for _, refused := range plan.Refusals {
		if _, err := fmt.Fprintf(w, "refused %s: %s\n", refused.Name, refused.Reason); err != nil {
			return fmt.Errorf("write retire report: %w", err)
		}
	}
	return nil
}

// retireRefusalsError names every binding and chain a retire held back, so
// the operator sees what still pins the server.
func retireRefusalsError(name string, plan relevo.RetirePlan) error {
	held := make([]string, 0, len(plan.Refusals))
	for _, r := range plan.Refusals {
		held = append(held, r.Name+" ("+r.Reason+")")
	}
	return fail(codeConflict, "server %q is still held by %s", name, strings.Join(held, ", "))
}

type serversFlagValues struct {
	asJSON *bool
}

// serversFlagSet defines that flag on fs and returns what it parses into, so
// the registry's parity test finds exactly one installer per verb.
func serversFlagSet(fs *flag.FlagSet) *serversFlagValues {
	v := &serversFlagValues{}
	v.asJSON = fs.Bool("json", false, "print one JSON object per configured server")
	return v
}

// cmdServers prints one row per configured server with this client's
// enrollment, via the same probe the doctor's checks use. With no client key
// every server probes "no key" without dialling.
func cmdServers(args []string) error {
	fs := flag.NewFlagSet("relevo config server list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serversFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	L, err := rt.Config.Load()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	servers := L.Servers

	var remoteRT relevo.Runtime
	enrollLine := ""
	if key, kerr := remote.ParsePrivate(L.ClientKey); kerr == nil {
		remoteRT.Remote = client.New(servers, key, time.Now)
		enrollLine = client.EnrollLine(key)
	}
	probes := relevo.ProbeServers(context.Background(), remoteRT, servers, enrollLine)

	if *asJSON {
		return printDoc(serverRowsOf(probes))
	}
	if len(servers) == 0 {
		fmt.Println("no servers configured; relevo config server add <name> <url>")
		return nil
	}
	fmt.Print(relevo.RenderServers(probes))
	return nil
}
