package main

// The --join half of `db sync enable`, kept beside the verb rather than in it:
// db_sync.go is the whole sync CLI and this is one flag's intake.
//
// Everything here happens before anything is dialed. The code is parsed, the
// passphrase is read in unlock's order, and the three stdin flags are checked
// for a collision -- so a command that cannot go ahead costs no round trip and
// starts no worker.

import (
	"errors"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncEnableJoin is what a --join code resolved to, or that there was none.
//
// A code names the remote and the token, so it replaces both --url and
// --token-stdin. A --url beside it is a contradiction rather than a default: the
// code is the operator's stated destination and a second one means they meant two
// things, so it is refused by name instead of one of them quietly winning.
func dbSyncEnableJoin(code, url string) (dbSyncJoin, error) {
	if code == "" {
		return dbSyncJoin{remote: url}, nil
	}
	if url != "" {
		return dbSyncJoin{}, fail(codeUsage,
			"relevo db sync enable: --join and --url name two remotes; pass one")
	}
	parsed, err := relevosync.ParseJoinCode(code)
	if err != nil {
		return dbSyncJoin{}, fail(codeUsage, "%v", err)
	}
	return dbSyncJoin{remote: parsed.URL, token: []byte(parsed.Token), joining: true}, nil
}

// dbSyncJoin is a resolved --join: which remote, which token the code carried,
// and that the enable came from a code rather than from flags.
type dbSyncJoin struct {
	remote  string
	token   []byte
	joining bool
}

// dbSyncEnablePassphrase resolves the passphrase in unlock's order: standard
// input, else RELEVO_SYNC_PASSPHRASE, else a prompt with echo off. isNew is
// always false: an enable never creates key material on a remote, so a remote
// holding none stays `relevo db sync unlock --new`'s work.
//
// Whether a missing passphrase is a refusal is what required decides. A --join
// needs one -- a joiner that cannot unlock would join holding nothing at all --
// so there it ends in the refusal and the enable stores nothing. A plain enable
// does not: a machine that is already unlocked has no use for one, and a
// scripted enable has no terminal to offer, so "nothing carried one and there is
// no terminal" is an empty answer rather than a failure.
func dbSyncEnablePassphrase(fromStdin, required bool) ([]byte, error) {
	pass, err := dbSyncPassphrase(fromStdin, false)
	if err != nil && !required && errors.Is(err, errPassphraseUnavailable) {
		return nil, nil
	}
	return pass, err
}
