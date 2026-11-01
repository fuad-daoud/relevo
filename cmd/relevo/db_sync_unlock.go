package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

const dbSyncPassphraseEnv = "RELEVO_SYNC_PASSPHRASE"

// errPassphraseUnavailable is a passphrase nothing carried with no terminal to
// ask on. It is its own sentinel so a caller that does not need one can tell
// "none is available" apart from "the one supplied was blank".
var errPassphraseUnavailable = errors.New("no passphrase is available: pass --passphrase-stdin or set " + dbSyncPassphraseEnv)

// The passphrase sources are package vars so a test never touches a real handle.
var (
	dbSyncPassphraseStdin io.Reader = os.Stdin
	dbSyncPassphraseEnvFn           = os.Getenv
	// dbSyncPassphraseTTY prompts with echo off; ok is false when there is no
	// terminal to prompt on.
	dbSyncPassphraseTTY = readPassphraseTTY
)

func readPassphraseTTY(prompt string) (pass []byte, ok bool, err error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, false, nil
	}
	fmt.Fprint(os.Stderr, prompt)
	pass, err = term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return pass, true, err
}

type dbSyncUnlockFlagValues struct {
	asJSON          *bool
	isNew           *bool
	passphraseStdin *bool
}

func dbSyncUnlockFlagSet(fs *flag.FlagSet) *dbSyncUnlockFlagValues {
	v := &dbSyncUnlockFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the unlock produced")
	v.isNew = fs.Bool("new", false, "create the remote's key where it holds none")
	v.passphraseStdin = fs.Bool("passphrase-stdin", false, "read the passphrase from standard input")
	return v
}

type dbSyncUnlockDoc struct {
	Unlocked bool `json:"unlocked"`
	Created  bool `json:"created"`
}

// dbSyncPassphrase resolves the passphrase: standard input, else the environment,
// else a prompt with echo off, asked twice for a new key. Nothing here puts the
// passphrase into an error.
func dbSyncPassphrase(fromStdin, isNew bool) ([]byte, error) {
	var pass []byte
	switch {
	case fromStdin:
		raw, err := io.ReadAll(dbSyncPassphraseStdin)
		if err != nil {
			return nil, fail(codeUsage, "relevo db sync unlock --passphrase-stdin: could not read standard input")
		}
		pass = raw
	case dbSyncPassphraseEnvFn(dbSyncPassphraseEnv) != "":
		pass = []byte(dbSyncPassphraseEnvFn(dbSyncPassphraseEnv))
	default:
		var err error
		if pass, err = dbSyncPassphrasePrompt(isNew); err != nil {
			return nil, err
		}
	}
	pass = []byte(strings.TrimRight(string(pass), "\r\n"))
	if len(pass) == 0 {
		return nil, fail(codeUsage, "relevo db sync unlock: the passphrase is empty")
	}
	return pass, nil
}

func dbSyncPassphrasePrompt(isNew bool) ([]byte, error) {
	pass, ok, err := dbSyncPassphraseTTY("Sync passphrase: ")
	if err != nil {
		return nil, fail(codeUsage, "relevo db sync unlock: could not read the passphrase")
	}
	if !ok {
		return nil, failWrap(codeUsage, errPassphraseUnavailable, "relevo db sync unlock")
	}
	if isNew {
		again, _, err := dbSyncPassphraseTTY("Repeat passphrase: ")
		if err != nil {
			return nil, fail(codeUsage, "relevo db sync unlock: could not read the passphrase")
		}
		if string(again) != string(pass) {
			return nil, fail(codeUsage, "relevo db sync unlock: the two passphrases differ")
		}
	}
	return pass, nil
}

// cmdDBSyncUnlock sends the passphrase to the daemon, which stores the derived
// key when it matches the remote's check.
func cmdDBSyncUnlock(args []string) error {
	fs := flag.NewFlagSet("db sync unlock", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncUnlockFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync unlock takes no arguments, got %d", fs.NArg())
	}
	pass, err := dbSyncPassphrase(*v.passphraseStdin, *v.isNew)
	if err != nil {
		return err
	}

	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), dbSyncDefaultTimeout+verbDialSlack)
	defer cancel()

	res, err := sendSyncVerbToken(ctx, shared, wire.SyncVerbUnlock, pass,
		dbSyncVerbOptions{Timeout: dbSyncDefaultTimeout, New: *v.isNew})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncUnlockDoc{Unlocked: true, Created: res.Created})
	}
	fmt.Println("unlocked")
	return nil
}

// dbSyncKeyClause is the status line's key clause, empty when there is no key
// and the remote holds none.
func dbSyncKeyClause(key string) string {
	switch key {
	case "unlocked":
		return ", key: unlocked"
	case "locked":
		return ", key: locked: run relevo db sync unlock"
	case "stale":
		return ", key: the passphrase changed: run relevo db sync unlock"
	}
	return ""
}
