package main

// `relevo db sync rekey`: rotate the log's key to a new epoch. Both passphrases
// travel in the frame's raw tail behind their own lengths, never argv or a log.
// stdin carries at most one secret, so the new one arrives from the environment
// or a prompt asked twice.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

const dbSyncNewPassphraseEnv = "RELEVO_SYNC_NEW_PASSPHRASE"

// dbSyncRekeyFlagValues holds the pointers `db sync rekey` parses into.
type dbSyncRekeyFlagValues struct {
	asJSON          *bool
	passphraseStdin *bool
}

func dbSyncRekeyFlagSet(fs *flag.FlagSet) *dbSyncRekeyFlagValues {
	v := &dbSyncRekeyFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the rekey produced")
	v.passphraseStdin = fs.Bool("passphrase-stdin", false, "read the current passphrase from standard input")
	return v
}

type dbSyncRekeyDoc struct {
	Rekeyed bool `json:"rekeyed"`
}

// dbSyncRekeyPassphrases resolves the pair the rotation needs. The current one
// follows unlock's order; the new one has no stdin route, since stdin carries
// at most one secret, and may not be the current one: rotation answers a leaked
// passphrase, and the same passphrase would re-derive the new key. Nothing here
// puts a passphrase into an error.
func dbSyncRekeyPassphrases(fromStdin bool) (current, newPass []byte, err error) {
	current, err = dbSyncPassphrase(fromStdin, false)
	if err != nil {
		return nil, nil, err
	}
	if newPass, err = dbSyncRekeyNewPassphrase(); err != nil {
		return nil, nil, err
	}
	if string(newPass) == string(current) {
		return nil, nil, fail(codeUsage, "relevo db sync rekey: the new passphrase is the current one")
	}
	return current, newPass, nil
}

// dbSyncRekeyNewPassphrase resolves the passphrase the fleet rotates to: its own
// environment variable, else a prompt asked twice. An empty answer is refused
// the way unlock's is.
func dbSyncRekeyNewPassphrase() ([]byte, error) {
	if raw := dbSyncPassphraseEnvFn(dbSyncNewPassphraseEnv); raw != "" {
		pass := dbSyncTrimmed(raw)
		if len(pass) == 0 {
			return nil, fail(codeUsage, "relevo db sync rekey: the passphrase is empty")
		}
		return pass, nil
	}
	pass, ok, err := dbSyncPassphraseTTY("New sync passphrase: ")
	if err != nil {
		return nil, fail(codeUsage, "relevo db sync rekey: could not read the passphrase")
	}
	if !ok {
		return nil, failWrap(codeUsage, errPassphraseUnavailable, "relevo db sync rekey")
	}
	again, _, err := dbSyncPassphraseTTY("Repeat passphrase: ")
	if err != nil {
		return nil, fail(codeUsage, "relevo db sync rekey: could not read the passphrase")
	}
	if string(again) != string(pass) {
		return nil, fail(codeUsage, "relevo db sync rekey: the two passphrases differ")
	}
	trimmed := dbSyncTrimmed(string(pass))
	if len(trimmed) == 0 {
		return nil, fail(codeUsage, "relevo db sync rekey: the passphrase is empty")
	}
	return trimmed, nil
}

// dbSyncTrimmed strips the trailing newline an interactive or piped passphrase carries.
func dbSyncTrimmed(pass string) []byte {
	return []byte(strings.TrimRight(pass, "\r\n"))
}

// cmdDBSyncRekey sends both passphrases to the daemon, which checks the current
// one against the remote and writes the new epoch's material in one act.
func cmdDBSyncRekey(args []string) error {
	fs := flag.NewFlagSet("db sync rekey", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncRekeyFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync rekey takes no arguments, got %d", fs.NArg())
	}
	current, newPass, err := dbSyncRekeyPassphrases(*v.passphraseStdin)
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

	tail := append(append([]byte{}, current...), newPass...)
	if _, err := sendSyncVerbToken(ctx, shared, wire.SyncVerbRekey, tail, dbSyncVerbOptions{
		Timeout:          dbSyncDefaultTimeout,
		PassphraseLen:    len(current),
		NewPassphraseLen: len(newPass),
	}); err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncRekeyDoc{Rekeyed: true})
	}
	fmt.Println("rekeyed")
	return nil
}
