package main

// The --join-stdin half of `db sync enable`, kept beside the verb rather than in
// it. Everything here happens before anything is dialed, so a command that
// cannot go ahead costs no round trip and starts no worker.

import (
	"bytes"
	"errors"
	"io"
	"os"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncJoinStdin is the reader --join-stdin reads; a var so a test can feed it
// without a pipe.
var dbSyncJoinStdin io.Reader = os.Stdin

// dbSyncReadJoinCode reads the code off standard input, or returns none when
// --join-stdin was not passed. The code is a bearer, so it never rides argv.
func dbSyncReadJoinCode(fromStdin bool) (string, error) {
	if !fromStdin {
		return "", nil
	}
	read, err := io.ReadAll(dbSyncJoinStdin)
	if err != nil {
		return "", failWrap(codeUsage, err, "relevo db sync enable --join-stdin")
	}
	code := string(bytes.TrimSpace(read))
	if code == "" {
		return "", fail(codeUsage, "relevo db sync enable --join-stdin: standard input carried no code")
	}
	return code, nil
}

// dbSyncEnableJoin is what a join code resolved to, or that there was none. A
// --url beside a code is a contradiction rather than a default, so it is refused
// by name instead of one quietly winning.
func dbSyncEnableJoin(code, url string) (dbSyncJoin, error) {
	if code == "" {
		return dbSyncJoin{remote: url}, nil
	}
	if url != "" {
		return dbSyncJoin{}, fail(codeUsage,
			"relevo db sync enable: --join-stdin and --url name two remotes; pass one")
	}
	parsed, err := relevosync.ParseJoinCode(code)
	if err != nil {
		return dbSyncJoin{}, fail(codeUsage, "%v", err)
	}
	return dbSyncJoin{remote: parsed.URL, token: []byte(parsed.Token), joining: true}, nil
}

// dbSyncJoin is a resolved --join: which remote, which token the code carried.
type dbSyncJoin struct {
	remote  string
	token   []byte
	joining bool
}

// dbSyncEnablePassphrase resolves the passphrase in unlock's order. isNew is
// always false: an enable never creates key material on a remote. Whether a
// missing passphrase is a refusal is what required decides: a --join needs
// one, because a joiner that cannot unlock would join holding nothing, while
// a plain enable without a terminal gets an empty answer rather than a
// failure.
func dbSyncEnablePassphrase(fromStdin, required bool) ([]byte, error) {
	pass, err := dbSyncPassphrase(fromStdin, false)
	if err != nil && !required && errors.Is(err, errPassphraseUnavailable) {
		return nil, nil
	}
	return pass, err
}
