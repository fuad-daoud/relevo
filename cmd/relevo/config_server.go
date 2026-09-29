package main

import (
	"errors"
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
		fmt.Fprintf(os.Stderr, "relevo config server: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// configServerKey prints the client id and the enrolment line, generating the
// key when it is absent. This is what `client init` printed.
func configServerKey(args []string) error {
	fs := flag.NewFlagSet("relevo config server key", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// The provider parses this line; its format is remote.MarshalPublic's.
	enrollOnly := fs.Bool("enroll-line", false, "print only the enrolment line (ed25519 <pubkey> <comment>)")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: relevo config server key [--enroll-line]")
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	pem, _, err := ensureClientKey(rt)
	if err != nil {
		return err
	}
	return printClientKey(pem, *enrollOnly)
}

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
		fmt.Fprintf(os.Stderr, "relevo config secret: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// checkSecretName refuses a name other than the two secrets relevo stores,
// exiting 2 with the allowed names.
func checkSecretName(name string) error {
	if name != config.SecretTypesafe && name != config.SecretClientKey {
		fmt.Fprintf(os.Stderr, "relevo config secret: unknown secret %q (allowed: %s, %s)\n",
			name, config.SecretTypesafe, config.SecretClientKey)
		return exitCodeErr{code: 2}
	}
	return nil
}

// configSecretSet reads the value from stdin, trimmed, and stores it. The
// client key is validated by PutSecret.
func configSecretSet(args []string) error {
	fs := flag.NewFlagSet("relevo config secret set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config secret set <typesafe|client.key>")
		return exitCodeErr{code: 2}
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
	fmt.Printf("stored secret %s\n", name)
	return nil
}

// configSecretRm removes a stored secret.
func configSecretRm(args []string) error {
	fs := flag.NewFlagSet("relevo config secret rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config secret rm <typesafe|client.key>")
		return exitCodeErr{code: 2}
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
	fmt.Printf("removed secret %s\n", name)
	return nil
}

// configSecretList prints the stored secret names, never their values.
func configSecretList(args []string) error {
	fs := flag.NewFlagSet("relevo config secret list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	names, err := rt.Config.SecretNames()
	if err != nil {
		return err
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
// runs `relevo serve enroll --key "<line>"` with. With enrollOnly it prints the
// enrolment line alone.
func printClientKey(pem []byte, enrollOnly bool) error {
	kp, err := remote.ParsePrivate(pem)
	if err != nil {
		return err
	}
	if enrollOnly {
		fmt.Println(client.EnrollLine(kp))
		return nil
	}
	fmt.Printf("client id %s\n", remote.IDOf(kp.Public))
	fmt.Println(client.EnrollLine(kp))
	return nil
}
