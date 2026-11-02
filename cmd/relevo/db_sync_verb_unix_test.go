//go:build unix

package main

import (
	"testing"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// TestSyncWritersSucceedWhileTheDaemonRuns pins that enable, push, pull and
// disable all succeed while a daemon holds relevo.db under its lock. Each verb
// dials the owner and asks it to perform the work with the handles the daemon
// already has, so none of them opens the file itself.
//
// The test is shaped so that a writer that opens the file directly fails it: it
// meets the lock this daemon is holding and returns codeConflict naming
// `relevo daemon stop`, which is neither a success nor the refusal this verb
// answers with. A refusal of any other class also fails, because the point is
// that the daemon running is the case these verbs are built for.
func TestSyncWritersSucceedWhileTheDaemonRuns(t *testing.T) {
	startSyncStatusDaemon(t, dbSyncStatusCases[2])

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

// TestDBSyncRetryRebuildOutput pins both halves of the retry's answer through
// the scripted hook. The plain line is the string it is today -- byte for
// byte, which is what proves the flag changed nothing when it was not asked
// for -- and the --rebuild line names the folder the copy was moved into, the
// evidence a user goes straight to. The --json documents carry the same
// split: applied alone when nothing moved, the folder beside it when one did.
//
// The mutation is dropping the aside branch from cmdDBSyncRetry, which makes
// the rebuild half print the plain line and fails the rebuild subtest.
func TestDBSyncRetryRebuildOutput(t *testing.T) {
	startSyncStatusDaemon(t, dbSyncStatusCases[2])

	t.Run("plain", func(t *testing.T) {
		out, _, err := captureOutput(t, func() error { return cmdDBSyncRetry(nil) })
		if err != nil {
			t.Fatalf("plain retry: %v", err)
		}
		if want := "sync retry: the latch is cleared and the worker will restart\n"; string(out) != want {
			t.Errorf("the plain retry printed %q, want %q", out, want)
		}
		out, _, err = captureOutput(t, func() error { return cmdDBSyncRetry([]string{"--json"}) })
		if err != nil {
			t.Fatalf("plain retry --json: %v", err)
		}
		if want := "{\n  \"applied\": true\n}\n"; string(out) != want {
			t.Errorf("the plain --json document = %q, want %q", out, want)
		}
	})

	t.Run("rebuild", func(t *testing.T) {
		out, _, err := captureOutput(t, func() error { return cmdDBSyncRetry([]string{"--rebuild"}) })
		if err != nil {
			t.Fatalf("retry --rebuild: %v", err)
		}
		if want := dbSyncRebuiltLine(testVerbAside) + "\n"; string(out) != want {
			t.Errorf("the rebuild line = %q, want %q", out, want)
		}
		out, _, err = captureOutput(t, func() error { return cmdDBSyncRetry([]string{"--rebuild", "--json"}) })
		if err != nil {
			t.Fatalf("retry --rebuild --json: %v", err)
		}
		if want := "{\n  \"applied\": true,\n  \"aside\": \"" + testVerbAside + "\"\n}\n"; string(out) != want {
			t.Errorf("the rebuild --json document = %q, want %q", out, want)
		}
	})
}
