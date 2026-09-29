package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// configServerFlagSet declares the bare `config server` dispatcher's flags:
// none today. It exists so the registry's parity test finds exactly one
// installer per verb.
func configServerFlagSet(*flag.FlagSet) {}

// configServer is the server half of `relevo config`: this machine's
// remote-builder identity and its configured servers (§4.1).
func configServer(args []string) error {
	const usage = `usage: relevo config server add <name> <url> (--fingerprint sha256:<hex> | --ca system | --insecure)
       relevo config server rm <name>
       relevo config server list
       relevo config server key [--enroll-line]`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "add":
		return cmdClientAddServer(args[1:])
	case "rm":
		return cmdClientRmServer(args[1:])
	case "list":
		return cmdServers(args[1:])
	case "key":
		return configServerKey(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fail(codeUsage, "relevo config server: unknown command %q", args[0])
	}
}

// configServerKeyFlagValues holds the pointers `config server key` parses
// into.
type configServerKeyFlagValues struct {
	enrollOnly *bool
	asJSON     *bool
}

// configServerKeyFlagSet defines those flags on fs and returns what they parse
// into.
func configServerKeyFlagSet(fs *flag.FlagSet) *configServerKeyFlagValues {
	v := &configServerKeyFlagValues{}
	// The provider parses this line; its format is remote.MarshalPublic's.
	v.enrollOnly = fs.Bool("enroll-line", false, "print only the enrolment line (ed25519 <pubkey> <comment>)")
	v.asJSON = fs.Bool("json", false, "print the client id and the enrolment line as a JSON document")
	return v
}

// configServerKey prints the client id and the enrolment line, generating the
// key when it is absent. This is what `client init` printed.
func configServerKey(args []string) error {
	fs := flag.NewFlagSet("relevo config server key", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configServerKeyFlagSet(fs)
	enrollOnly, asJSON := v.enrollOnly, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "config server key takes no arguments, got %v", fs.Args())
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	pem, _, err := ensureClientKey(rt)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if *asJSON {
		kp, err := remote.ParsePrivate(pem)
		if err != nil {
			return fail(codeInternal, "%v", err)
		}
		return printDoc(serverKeyDoc{ID: string(remote.IDOf(kp.Public)), EnrollLine: client.EnrollLine(kp)})
	}
	return printClientKey(os.Stdout, pem, *enrollOnly)
}

// configSecretFlagSet declares the bare `config secret` dispatcher's flags:
// none today.
func configSecretFlagSet(*flag.FlagSet) {}

// configSecret is the secrets half of `relevo config` (§4.1). A list prints
// names only, never values.
func configSecret(args []string) error {
	const usage = `usage: relevo config secret set <typesafe|client.key>
       relevo config secret rm <typesafe|client.key>
       relevo config secret list`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "set":
		return configSecretSet(args[1:])
	case "rm":
		return configSecretRm(args[1:])
	case "list":
		return configSecretList(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fail(codeUsage, "relevo config secret: unknown command %q", args[0])
	}
}

// checkSecretName refuses a name other than the two secrets relevo stores. The
// coded error names both allowed names, so the frame carries the same words a
// human needs.
func checkSecretName(name string) error {
	if name != config.SecretTypesafe && name != config.SecretClientKey {
		return fail(codeUsage, "unknown secret %q (allowed: %s, %s)",
			name, config.SecretTypesafe, config.SecretClientKey)
	}
	return nil
}

// configSecretSetFlagValues holds the pointer `config secret set` parses into.
type configSecretSetFlagValues struct {
	asJSON *bool
}

// configSecretSetFlagSet defines that flag on fs and returns what it parses
// into.
func configSecretSetFlagSet(fs *flag.FlagSet) *configSecretSetFlagValues {
	v := &configSecretSetFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	return v
}

// configSecretSet reads the value from stdin, trimmed, and stores it. The
// client key is validated by PutSecret.
func configSecretSet(args []string) error {
	return outcomeError(configSecretSetRun(args))
}

func configSecretSetRun(args []string) error {
	fs := flag.NewFlagSet("relevo config secret set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configSecretSetFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config secret set wants <typesafe|client.key>")
	}
	name := rest[0]
	if err := checkSecretName(name); err != nil {
		return err
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	value := strings.TrimSpace(string(data))

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if err := rt.Config.As("cli", "config secret set "+name).PutSecret(name, []byte(value)); err != nil {
		return err
	}
	if *asJSON {
		return printDoc(configSecretDocOf(name))
	}
	fmt.Printf("stored secret %s\n", name)
	return nil
}

// configSecretRmFlagValues holds the pointer `config secret rm` parses into.
type configSecretRmFlagValues struct {
	asJSON *bool
}

// configSecretRmFlagSet defines that flag on fs and returns what it parses
// into.
func configSecretRmFlagSet(fs *flag.FlagSet) *configSecretRmFlagValues {
	v := &configSecretRmFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	return v
}

// configSecretRm removes a stored secret.
func configSecretRm(args []string) error {
	return outcomeError(configSecretRmRun(args))
}

func configSecretRmRun(args []string) error {
	fs := flag.NewFlagSet("relevo config secret rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configSecretRmFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config secret rm wants <typesafe|client.key>")
	}
	name := rest[0]
	if err := checkSecretName(name); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if err := rt.Config.As("cli", "config secret rm "+name).SecretDelete(name); err != nil {
		return err
	}
	if *asJSON {
		return printDoc(configSecretDocOf(name))
	}
	fmt.Printf("removed secret %s\n", name)
	return nil
}

// configSecretListFlagValues holds the pointer `config secret list` parses
// into.
type configSecretListFlagValues struct {
	asJSON *bool
}

// configSecretListFlagSet defines that flag on fs and returns what it parses
// into, so the registry's parity test finds exactly one installer per verb.
func configSecretListFlagSet(fs *flag.FlagSet) *configSecretListFlagValues {
	v := &configSecretListFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the stored secret names as a JSON array")
	return v
}

// configSecretList prints the stored secret names, never their values.
func configSecretList(args []string) error {
	fs := flag.NewFlagSet("relevo config secret list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configSecretListFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	names, err := rt.Config.SecretNames()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	if *asJSON {
		if names == nil {
			names = []string{}
		}
		return printDoc(names)
	}
	for _, name := range names {
		fmt.Println(name)
	}
	return nil
}

// ensureClientKey returns the stored client key, generating and storing one
// when it is absent. generated reports whether it had to create one.
func ensureClientKey(rt relevo.Runtime) (pem []byte, generated bool, err error) {
	if stored, ok, err := rt.Config.Secret(config.SecretClientKey); err != nil {
		return nil, false, err
	} else if ok {
		return stored, false, nil
	}

	kp, err := remote.Generate()
	if err != nil {
		return nil, false, err
	}
	pem, err = remote.MarshalPrivate(kp)
	if err != nil {
		return nil, false, err
	}
	if err := rt.Config.As("cli", "config server key").PutSecret(config.SecretClientKey, pem); err != nil {
		return nil, false, err
	}
	return pem, true, nil
}

// printClientKey prints the client id and the enrolment line a server admin
// runs `relevo serve enroll --key "<line>"` with, to w. With enrollOnly it
// prints the enrolment line alone. w is stdout for the read verb and the
// notice stream for `config server add`, whose document owns stdout.
func printClientKey(w io.Writer, pem []byte, enrollOnly bool) error {
	kp, err := remote.ParsePrivate(pem)
	if err != nil {
		return err
	}
	if enrollOnly {
		fmt.Fprintln(w, client.EnrollLine(kp))
		return nil
	}
	fmt.Fprintf(w, "client id %s\n", remote.IDOf(kp.Public))
	fmt.Fprintln(w, client.EnrollLine(kp))
	return nil
}
