package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// testVerbTokens are the values the fixture hands every verb. A test that greps
// for one of them is looking for a credential that leaked; they are long and
// unmistakable on purpose.
const (
	testVerbTokenStdin = "FIXTURE-TOKEN-STDIN-aaaaaaaaaaaaaaaaaaaa"
	testVerbTokenPush  = "FIXTURE-TOKEN-PUSH-bbbbbbbbbbbbbbbbbbbb"
	// testVerbAside is the folder the scripted hook says a rebuild moved the
	// copy into. It is a fixture value like the tokens: unmistakable, and
	// nowhere a real folder.
	testVerbAside = "FIXTURE-ASIDE-relevo-sync-aside"
)

// installTestVerbHook gives an owner a verb executor that answers from a script
// rather than from a remote.
//
// It exists so the writer verbs can be exercised end to end -- dial, request,
// answer -- with no network and no harness. It is deliberately NOT the real
// executor: what these tests pin is which process performs the verb and how the
// answer is classified, and the real executor's semantics are covered against
// the fake driver in internal/relevo. A fake here also means a verb can never
// quietly start reaching a network from a cmd/relevo test.
//
// The token is recorded only as its length. The whole point of these tests is
// that a token is never observable outside the request that carried it, so a
// helper that stored the value would be the bug it is here to rule out.
func installTestVerbHook(srv *owner.Server, d *db.DB) {
	srv.OnSyncVerb = func(_ context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
		res := &wire.SyncResult{OK: true, TokenPresent: true}
		switch verb.Verb {
		case wire.SyncVerbEnable:
			res.RemoteURL = verb.RemoteURL
		case wire.SyncVerbPush:
			res.Applied = true
		case wire.SyncVerbDisable:
			res.Steps = []string{"final export", "mark off", "delete token", "delete key", "stop worker", "delete replica", "drop client"}
			res.FinalPush = true
		case wire.SyncVerbRetry:
			// A rebuild rides the retry verb as its flag; the hook answers it
			// with the folder a real daemon would have moved the copy into.
			if verb.Rebuild {
				res.Aside = testVerbAside
			}
		}
		_ = len(token)
		return res
	}
}

// TestSyncWritersNeverReturnConflict pins by name that no sync writer reaches
// codeConflict.
//
// With the verb surface there is no second open, so no writer competes with the
// daemon for the file lock and no genuinely foreign holder is left to name with
// `conflict` -- a held file with no answering owner reads as the
// owner-unavailable refusal, which is `refused`. A test that only checked the
// happy path would not notice conflict creeping back in on an error path, so
// this walks every writer with no daemon running at all: a held file and no
// owner, which is the case that a direct open would answer with a conflict.
func TestSyncWritersNeverReturnConflict(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	if _, err := openDBDirect(machineDBPath()); err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}

	for _, verb := range []struct {
		name string
		run  func() error
	}{
		{"push", func() error { return cmdDBSyncPush(nil) }},
		{"pull", func() error { return cmdDBSyncPull(nil) }},
		{"retry", func() error { return cmdDBSyncRetry(nil) }},
		{"disable", func() error { return cmdDBSyncDisable(nil) }},
	} {
		t.Run(verb.name, func(t *testing.T) {
			_, _, err := captureOutput(t, verb.run)
			if err == nil {
				t.Fatalf("%s with no owner serving: want a refusal", verb.name)
			}
			var ce *cliError
			if errors.As(err, &ce) && ce.code == codeConflict {
				t.Fatalf("%s returned codeConflict, which no sync writer may return any more", verb.name)
			}
		})
	}
}

// TestDBSyncEnableTokenResolvesBeforeTheDaemon pins what the client sends, which
// is the only half of the token rule this process owns: the flag beats the
// environment, a flag asked for and left empty falls through to the environment,
// and a token is never rendered into the error a refusal carries.
//
// The case this names and the ordinary ones are one table because they are one
// rule. The daemon holds the stored token and falls back to it, so a bare enable
// is a request with an empty token rather than a refusal here: an enable that
// stopped before the join finished left the mark off, and the retry that resumes
// it is exactly the invocation carrying no token. A machine with nothing stored
// is refused by the daemon, naming both routes, with the code and the text it
// already had.
//
// The mutation is dbSyncEnableToken refusing on intake's own ErrNoToken, which
// sends a resume down a usage error before anything is dialed.
func TestDBSyncEnableTokenResolvesBeforeTheDaemon(t *testing.T) {
	const envToken = "FIXTURE-TOKEN-ENV-cccccccccccccccccccc"
	cases := []struct {
		name      string
		fromStdin bool
		stdin     string
		env       string
		want      []byte
	}{
		{name: "bare", want: nil},
		{name: "stdin", fromStdin: true, stdin: testVerbTokenStdin, want: []byte(testVerbTokenStdin)},
		{name: "env", env: envToken, want: []byte(envToken)},
		// The flag beats the environment, and a flag that was asked for and left
		// empty is not an answer, so the environment still answers.
		{name: "stdin beats env", fromStdin: true, stdin: testVerbTokenStdin, env: envToken, want: []byte(testVerbTokenStdin)},
		{name: "empty stdin falls through to env", fromStdin: true, stdin: "  \n", env: envToken, want: []byte(envToken)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev := dbSyncTokenStdin
			dbSyncTokenStdin = &testReader{data: []byte(c.stdin)}
			t.Cleanup(func() { dbSyncTokenStdin = prev })
			t.Setenv(relevosync.EnvToken, c.env)

			token, err := dbSyncEnableToken(dbSyncJoin{}, c.fromStdin)
			if err != nil {
				t.Fatalf("dbSyncEnableToken: %v", err)
			}
			if string(token) != string(c.want) {
				t.Errorf("token = %q, want %q", token, c.want)
			}
		})
	}
}

// TestDBSyncEnableTokenNeverRendersItsValue pins that the one refusal this
// process can still raise about a token carries the routes and nothing else.
// The value is never formatted into an error, so a log line built from one
// cannot leak the credential.
func TestDBSyncEnableTokenNeverRendersItsValue(t *testing.T) {
	prev := dbSyncTokenStdin
	dbSyncTokenStdin = &testReader{err: errors.New("pipe is closed")}
	t.Cleanup(func() { dbSyncTokenStdin = prev })

	_, err := dbSyncEnableToken(dbSyncJoin{}, true)
	if err == nil {
		t.Fatal("a stdin that cannot be read was accepted")
	}
	if strings.Contains(err.Error(), relevosync.SecretToken) {
		t.Errorf("the refusal rendered the secret name from a value it never read: %v", err)
	}
	if !strings.Contains(err.Error(), "relevo db sync enable --token-stdin") {
		t.Errorf("the refusal does not name the flag to fix it: %v", err)
	}
}

// TestDBSyncRetryLineSaysWhenNothingWasMoved pins the --rebuild answer for a
// machine with no replica file: it is not the moved-aside line, which would
// name a folder that holds nothing, and not the plain line, which would hide
// that the rebuild was asked for.
func TestDBSyncRetryLineSaysWhenNothingWasMoved(t *testing.T) {
	t.Parallel()

	got := dbSyncRetryLine(true, "")
	if !strings.Contains(got, "no replica to move aside") {
		t.Errorf("retry --rebuild with nothing moved printed %q", got)
	}
	if got == dbSyncRetryLine(false, "") {
		t.Errorf("retry --rebuild with nothing moved printed the plain line %q", got)
	}
}

// testVerbStdinReader hands a verb its token without a pipe, so no test reads
// the caller's real stdin.
func testVerbStdinReader(t *testing.T, value string) *testReader {
	t.Helper()
	return &testReader{data: []byte(value)}
}

type testReader struct {
	data []byte
	off  int
	// err is returned instead of the data when a test wants a pipe that failed
	// rather than one that closed cleanly.
	err error
}

func (r *testReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
