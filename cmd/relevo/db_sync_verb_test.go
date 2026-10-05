package main

import (
	"context"
	"io"
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
			res.SeedCase = "empty_cloud"
			res.RemoteURL = verb.RemoteURL
		case wire.SyncVerbPush:
			res.Applied = true
		case wire.SyncVerbDisable:
			res.Steps = []string{"final push", "mark off", "delete token", "close handle"}
			res.FinalPush = true
		}
		_ = len(token)
		return res
	}
}

// TestSyncWritersSucceedWhileTheDaemonRuns is the successor to the test that
// pinned the opposite. A daemon in another process holds relevo.db under its
// lock, and enable, push, pull and disable all still succeed -- because none of
// them opens the file any more. Each verb dials the owner and asks it to
// perform the work with the handles the daemon already has.
//
// This is the stop dance's whole removal, so the test is shaped so that
// reintroducing the old path fails it: a writer that opens the file directly
// meets the lock this daemon is holding and returns codeConflict naming
// `relevo daemon stop`, which is neither a success nor the refusal this verb
// now answers with. A refusal of any other class also fails, because the point
// is that the daemon running is the case these verbs are built for.
func TestSyncWritersSucceedWhileTheDaemonRuns(t *testing.T) {
	startSyncStatusDaemon(t, dbSyncStatusCases[2])

	for _, verb := range []struct {
		name string
		run  func() error
	}{
		{"push", func() error { return cmdDBSyncPush(nil) }},
		{"pull", func() error { return cmdDBSyncPull(nil) }},
		{"disable", func() error { return cmdDBSyncDisable(nil) }},
	} {
		t.Run(verb.name, func(t *testing.T) {
			_, _, err := captureOutput(t, verb.run)
			if err != nil {
				t.Fatalf("%s with the daemon running: %v", verb.name, err)
			}
		})
	}

	t.Run("enable", func(t *testing.T) {
		prev := dbSyncTokenStdin
		dbSyncTokenStdin = testVerbStdinReader(t, testVerbTokenStdin)
		t.Cleanup(func() { dbSyncTokenStdin = prev })
		t.Setenv(relevosync.EnvToken, "")

		_, _, err := captureOutput(t, func() error { return cmdDBSyncEnable([]string{"--token-stdin"}) })
		if err != nil {
			t.Fatalf("enable with the daemon running: %v", err)
		}
	})
}

// TestSyncWritersNeverReturnConflict pins by name that no sync writer reaches
// codeConflict any more.
//
// The conflict existed because the CLI competed with the daemon for the file
// lock. With the verb surface there is no second open, so there is no
// genuinely foreign holder left to name with `conflict` -- a held file with no
// answering owner reads as the owner-unavailable refusal, which is `refused`.
// A test that only checked the happy path would not notice conflict creeping
// back in on an error path, so this walks every writer with no daemon running
// at all: the case that used to be a lock conflict and is now a refusal naming
// the socket.
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
		{"disable", func() error { return cmdDBSyncDisable(nil) }},
	} {
		t.Run(verb.name, func(t *testing.T) {
			_, _, err := captureOutput(t, verb.run)
			if err == nil {
				t.Fatalf("%s with no owner serving: want a refusal", verb.name)
			}
			if ce, ok := err.(*cliError); ok && ce.code == codeConflict {
				t.Fatalf("%s returned codeConflict, which no sync writer may return any more", verb.name)
			}
		})
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
}

func (r *testReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
