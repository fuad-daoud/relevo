package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// cmdClientAddServer records a server entry in the servers section, and, when
// --fingerprint pins it, checks enrollment once with WhoAmI (§4.7). A 401
// here is not an error: it means the admin has not enrolled this client's key
// yet, and the reply names the enrollment line to hand them. When no client
// key exists yet it first generates one, as `client init` did, and prints the
// enrollment line.
// clientAddServerFlagValues holds the pointers `config server add` parses into.
type clientAddServerFlagValues struct {
	fingerprint *string
	ca          *string
	insecure    *bool
}

// clientAddServerFlagSet defines those flags on fs, in the usage text's order,
// and returns what they parse into, so the registry's parity test walks the
// same surface the verb does.
func clientAddServerFlagSet(fs *flag.FlagSet) *clientAddServerFlagValues {
	v := &clientAddServerFlagValues{}
	v.fingerprint = fs.String("fingerprint", "", "pin the server's certificate fingerprint (sha256:<hex>)")
	v.ca = fs.String("ca", "", `trust the system CA pool instead of pinning ("system")`)
	v.insecure = fs.Bool("insecure", false, "allow plain http (no TLS)")
	return v
}

func cmdClientAddServer(args []string) error {
	fs := flag.NewFlagSet("relevo config server add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := clientAddServerFlagSet(fs)
	fingerprint, ca, insecure := v.fingerprint, v.ca, v.insecure
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "usage: relevo config server add <name> <url> (--fingerprint sha256:... | --ca system | --insecure)")
		return exitCodeErr{code: 2}
	}
	name, rawURL := rest[0], rest[1]

	set := 0
	if *fingerprint != "" {
		set++
	}
	if *ca != "" {
		set++
	}
	if *insecure {
		set++
	}
	if set > 1 {
		fmt.Fprintln(os.Stderr, "relevo config server add: --fingerprint, --ca and --insecure are mutually exclusive")
		return exitCodeErr{code: 2}
	}

	entry := remote.ServerEntry{URL: rawURL, Fingerprint: *fingerprint, CA: *ca, Insecure: *insecure}
	if err := remote.ValidateEntry(entry); err != nil {
		return err
	}

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
		if err := printClientKey(pem, false); err != nil {
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
	fmt.Printf("added server %s (%s)\n", name, rawURL)

	if *fingerprint == "" {
		// Pinned by --ca or plain --insecure: there is no fingerprint to pin
		// the transport to, so there is nothing to enrollment-check yet.
		return nil
	}

	key, err := remote.ParsePrivate(pem)
	if err != nil {
		return err
	}

	c := client.New(servers, key, time.Now)
	who, werr := c.WhoAmI(context.Background(), name)
	if werr != nil {
		var httpErr *client.HTTPError
		if errors.As(werr, &httpErr) && httpErr.Status == 401 {
			fmt.Printf("not enrolled on %s: give the admin: %s\n", name, client.EnrollLine(key))
			return nil
		}
		return fmt.Errorf("%s: %w", name, werr)
	}
	fmt.Printf("enrolled as %s\n", who.Label)
	return nil
}

// cmdClientRmServer refuses while any binding in the store still names the
// server (relevo.ServerInUse is the pure rule this checks; it is tested in
// internal/relevo so this thin wrapper needs no harness or network access to
// test the refusal shape -- see CLAUDE.md's CI rule).
// clientRmServerFlagSet declares `config server rm`'s flags: none today. It
// exists so the registry's parity test finds exactly one installer per verb.
func clientRmServerFlagSet(*flag.FlagSet) {}

func cmdClientRmServer(args []string) error {
	fs := flag.NewFlagSet("relevo config server rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config server rm <name>")
		return exitCodeErr{code: 2}
	}
	name := rest[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return err
	}
	if inUse := relevo.ServerInUse(bindings, name); len(inUse) > 0 {
		return fmt.Errorf("server %q is used by %s; unbind them first", name, strings.Join(inUse, ", "))
	}

	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	servers := L.Servers
	if _, ok := servers[name]; !ok {
		return fmt.Errorf("no such server %q", name)
	}
	delete(servers, name)
	body, err := client.EncodeServers(servers)
	if err != nil {
		return err
	}
	if _, err := rt.Config.As("cli", "config server rm "+name).Put(config.Servers, body); err != nil {
		return err
	}
	fmt.Printf("removed server %s\n", name)
	return nil
}

// serversFlagValues holds the pointer `config server list` parses into.
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

// cmdServers prints one row per configured server: name, url, and this
// client's enrollment on it (§4.7), via the same relevo.ProbeServers doctor's
// per-server checks use. A machine with no client key probes every server
// "no key" without dialling; the same probe feeds the human table and --json.
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
