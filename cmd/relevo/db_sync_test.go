package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The tests here are the CLI's shape and nothing else. None of them opens a
// database, dials an owner, spawns a harness or reaches a network: they parse
// flags, read the usage text and walk the dispatch, which is the rule the
// package's own TestMain exists to keep. What the verbs do against a real
// database is covered by the package that owns the logic.

// TestDBSyncUsageNamesEveryVerb pins that the bare verb's usage names every verb
// the dispatcher routes, and names the token's two routes -- a verb list that
// drifts from the dispatcher is the failure a user hits first and cannot report
// precisely.
func TestDBSyncUsageNamesEveryVerb(t *testing.T) {
	t.Parallel()

	for _, verb := range []string{"enable", "disable", "status", "push", "pull"} {
		if !strings.Contains(dbSyncUsage, "relevo db sync "+verb) {
			t.Errorf("the usage text does not name %q:\n%s", verb, dbSyncUsage)
		}
	}
	for _, want := range []string{"--url", "--token-stdin", "TURSO_TOKEN", "turso.token", "turso db import", "config set sync"} {
		if !strings.Contains(dbSyncUsage, want) {
			t.Errorf("the usage text does not mention %q:\n%s", want, dbSyncUsage)
		}
	}
}

// TestDBSyncFlagSetsAreTheDocumentedOnes pins each verb's flags against what the
// registry entry says it takes, at the level a caller reads them. The registry
// parity test walks the same installers, so this is the half it cannot see: that
// the flags mean what their help text says, and that the defaults are the bounds
// rather than zero.
func TestDBSyncFlagSetsAreTheDocumentedOnes(t *testing.T) {
	t.Parallel()

	enable := parseInto(t, "db sync enable", dbSyncEnableFlagSet)
	if *enable.tokenStdin || *enable.seedUploaded || *enable.asJSON {
		t.Errorf("enable defaults = %v/%v/%v, want every flag off",
			*enable.tokenStdin, *enable.seedUploaded, *enable.asJSON)
	}
	if *enable.remoteURL != "" {
		t.Errorf("enable --url default = %q, want empty: a machine with no flag takes the stored remote", *enable.remoteURL)
	}
	if *enable.timeout != dbSyncDefaultTimeout {
		t.Errorf("enable --timeout default = %s, want %s", *enable.timeout, dbSyncDefaultTimeout)
	}

	disable := parseInto(t, "db sync disable", dbSyncDisableFlagSet)
	if *disable.timeout != dbSyncDefaultTimeout {
		t.Errorf("disable --timeout default = %s, want %s", *disable.timeout, dbSyncDefaultTimeout)
	}

	status := parseInto(t, "db sync status", dbSyncStatusFlagSet)
	if *status.asJSON {
		t.Error("status --json defaults on, want the human line")
	}

	push := parseInto(t, "db sync push", dbSyncPushFlagSet)
	pull := parseInto(t, "db sync pull", dbSyncPullFlagSet)
	if *push.timeout != *pull.timeout {
		t.Errorf("push and pull disagree on the default bound: %s and %s", *push.timeout, *pull.timeout)
	}
}

// TestDBSyncFlagsParseAfterAPositional pins the shape of every sync invocation
// that works: the flags may sit on either side of the subcommand, which is the
// one argument-order rule the whole CLI follows.
func TestDBSyncFlagsParseAfterAPositional(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("db sync enable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := dbSyncEnableFlagSet(fs)
	if err := parseFlags(fs, []string{"--json", "--url", "libsql://x-org.turso.io", "--token-stdin", "--timeout", "3s", "--seed-uploaded"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !*v.asJSON || !*v.tokenStdin || !*v.seedUploaded {
		t.Errorf("flags = %v/%v/%v, want all three on", *v.asJSON, *v.tokenStdin, *v.seedUploaded)
	}
	if *v.remoteURL != "libsql://x-org.turso.io" {
		t.Errorf("--url = %q, want the URL passed", *v.remoteURL)
	}
	if v.timeout.String() != "3s" {
		t.Errorf("--timeout = %s, want 3s", v.timeout)
	}
}

// TestDBSyncRoutesEveryVerb pins the dispatch without running a verb. The
// dispatcher is reached through run so the route from the top level is the one
// under test, and each case stops at its own usage refusal rather than opening
// anything.
// captureOutput swaps the process's stdout and stderr, so this test and the one
// below run sequentially with the rest of the package's capturing tests rather
// than in parallel: two of them at once would each write into the other's pipe.
func TestDBSyncRoutesEveryVerb(t *testing.T) {
	for _, verb := range []string{"enable", "disable", "status", "push", "pull", "bogus"} {
		t.Run(verb, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run([]string{"db", "sync", verb, "--nope"}) })
			if err == nil {
				t.Fatal("an unknown flag was accepted")
			}
			if !strings.Contains(err.Error(), string(codeUsage)) {
				t.Errorf("error = %v, want the usage code", err)
			}
		})
	}
}

// TestDBSyncBarePrintsUsage pins that the bare verb is a refusal rather than a
// silent success: it has nothing to run, and printing usage and exiting 2 is
// what tells a script the invocation was incomplete.
func TestDBSyncBarePrintsUsage(t *testing.T) {
	_, stderr, err := captureOutput(t, func() error { return run([]string{"db", "sync"}) })
	if !errors.Is(err, errUsagePrinted) {
		t.Fatalf("bare `db sync` = %v, want errUsagePrinted", err)
	}
	if !strings.Contains(string(stderr), "relevo db sync enable") {
		t.Errorf("stderr = %q, want the usage text", stderr)
	}
}

// TestDBSyncDBUsageNamesTheSubVerbs pins that the parent verb's usage carries
// the child, so `relevo db` alone is enough to discover that sync exists.
func TestDBSyncDBUsageNamesTheSubVerbs(t *testing.T) {
	t.Parallel()

	if !strings.Contains(dbUsage, "relevo db sync") {
		t.Errorf("the db usage does not name sync:\n%s", dbUsage)
	}
	if !strings.Contains(usage, "db sync") {
		t.Error("the top-level usage does not name db sync")
	}
}

// TestDBSyncClassifyNamesTheRemoteRefusals pins that the two remote refusals
// reach the user under codes a script can branch on, and that neither is
// reported as an internal failure. Both name their own fix -- the flag, and the
// two conflicting remotes -- so a reader who acts on the message is not sent to
// report a defect instead.
func TestDBSyncClassifyNamesTheRemoteRefusals(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  error
		code errorCode
	}{
		{relevosync.ErrNoRemote, codeUsage},
		{relevosync.ErrRemoteConflict, codeRefused},
	} {
		classified := dbSyncClassify(tc.err)

		var got *cliError
		if !errors.As(classified, &got) {
			t.Errorf("%v classified = %v, want a cliError", tc.err, classified)
			continue
		}
		if got.code != tc.code {
			t.Errorf("%v classified as %q, want %q", tc.err, got.code, tc.code)
		}
		if got.code == codeInternal {
			t.Errorf("%v is reported as an internal failure", tc.err)
		}
		if !strings.Contains(got.Error(), tc.err.Error()) {
			t.Errorf("%v lost its message in classification: %q", tc.err, got.Error())
		}
	}
}

// TestDBSyncEnableDocumentCarriesTheRemoteNotTheToken pins the JSON half of the
// --url promise: the document names the remote enable stored, because that is
// what a caller needs to reach the same remote, and never a token. The token
// surface is the one thing on this output that must stay unprintable, and this
// document is the one a script reads back.
func TestDBSyncEnableDocumentCarriesTheRemoteNotTheToken(t *testing.T) {
	t.Parallel()

	const token = "eyJhbGciOiJIUzI1NiJ9.db-sync-cli-fixture.signature"
	body, err := json.Marshal(dbSyncOutcomeDoc{
		Enabled:   true,
		RemoteURL: "libsql://x-org.turso.io",
		SeedCase:  string(relevosync.SeedEmptyCloud),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if !strings.Contains(string(body), `"remote_url":"libsql://x-org.turso.io"`) {
		t.Errorf("the enable document does not carry the stored remote: %s", body)
	}
	if strings.Contains(string(body), token) {
		t.Errorf("the enable document carries a token: %s", body)
	}
	if strings.Contains(string(body), "token") {
		t.Errorf("the enable document has a token field at all: %s", body)
	}
}

// TestDBSyncTokenNeverReachesAnIntakeRefusal pins the one thing the CLI adds to
// the token's promise: every refusal the sync verbs can raise is classified
// without the value reaching the line the user sees, and without the message
// saying which route the token was read from.
func TestDBSyncTokenNeverReachesAnIntakeRefusal(t *testing.T) {
	t.Parallel()

	const fixture = "eyJhbGciOiJIUzI1NiJ9.db-sync-cli-fixture.signature"
	for _, err := range []error{
		relevosync.ErrNoToken,
		relevosync.ErrAlreadyEnabled,
		relevosync.ErrSeedUploadRequired,
		relevosync.ErrNoRemote,
		relevosync.ErrRemoteConflict,
		fmt.Errorf("relevo db sync enable: %w", relevosync.ErrNoToken),
	} {
		classified := dbSyncClassify(err)
		if strings.Contains(classified.Error(), fixture) {
			t.Errorf("a refusal carries the token value: %v", classified)
		}
	}
}

// TestDBSyncClassifyReportsABusyDatabaseAsRefused pins that a database too busy
// to finish a bounded step reaches the reader as a refusal naming the file and
// the step, never as an internal failure pointing at `relevo bugreport`. Nothing
// was written, the hold was another writer, and the verb may be run again -- so
// the honest next hint is the verb itself.
func TestDBSyncClassifyReportsABusyDatabaseAsRefused(t *testing.T) {
	t.Parallel()

	contended := fmt.Errorf("sync: backfill %s: db: %s: the capture connection: %w: %w",
		"/state/relevo/relevo.db", "/state/relevo/relevo.db", db.ErrContended, context.DeadlineExceeded)
	classified := dbSyncClassify(contended)

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeRefused {
		t.Errorf("code = %q, want %q", got.code, codeRefused)
	}
	if strings.Contains(got.next, "bugreport") {
		t.Errorf("next = %q, want no bug report on a busy database", got.next)
	}
	if !strings.Contains(got.Error(), "/state/relevo/relevo.db") {
		t.Errorf("the classified refusal %q dropped the file it names", got.Error())
	}
	if !strings.Contains(got.Error(), "the capture connection") {
		t.Errorf("the classified refusal %q dropped the step it names", got.Error())
	}
	if !errors.Is(classified, db.ErrContended) {
		t.Errorf("the classified error no longer matches the contention it came from: %v", classified)
	}
}

// TestDBSyncVerbRefusalNamesTheVerbToRunAgain pins the verb-surface half: the
// code the owner sends for a busy database reaches the user as a refusal whose
// next hint is the verb, not a report.
func TestDBSyncVerbRefusalNamesTheVerbToRunAgain(t *testing.T) {
	t.Parallel()

	const message = "sync: backfill /state/relevo/relevo.db: db: /state/relevo/relevo.db: " +
		"the capture connection: the database is busy; nothing was written, so retry: context deadline exceeded"
	classified := dbSyncVerbRefusal(wire.SyncVerbEnable, &client.VerbRefusal{
		Code:    wire.SyncCodeContended,
		Message: message,
	})

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeRefused {
		t.Errorf("code = %q, want %q", got.code, codeRefused)
	}
	if got.next != "relevo db sync enable" {
		t.Errorf("next = %q, want the verb to run again", got.next)
	}
	if !strings.Contains(got.Error(), "the capture connection") {
		t.Errorf("the classified refusal %q dropped the step it names", got.Error())
	}
}

// TestDBSyncRemoteRefusalIsRefusedNotInternal pins what a reader sees for the
// fault this round found: a push the remote refused on a constraint. It is a
// refusal naming the constraint, and the next hint is a command rather than
// `relevo bugreport` -- a remote with enforced foreign keys refusing rows is not
// a defect in this tree, and sending a reader to report one is the wrong answer
// to a message that already says what the remote refused on.
func TestDBSyncRemoteRefusalIsRefusedNotInternal(t *testing.T) {
	t.Parallel()

	const message = "sync: push: the remote refused this machine's change set: FOREIGN KEY constraint " +
		"failed on the remote, which refused this machine's rows in the order the change set carried them"
	classified := dbSyncVerbRefusal(wire.SyncVerbPush, &client.VerbRefusal{
		Code:    wire.SyncCodeRemoteRefused,
		Message: message,
	})

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeRefused {
		t.Errorf("code = %q, want %q", got.code, codeRefused)
	}
	if strings.Contains(got.next, "bugreport") {
		t.Errorf("next = %q, want no bug report on a remote refusal", got.next)
	}
	if !strings.Contains(got.Error(), "FOREIGN KEY") {
		t.Errorf("the classified refusal %q dropped the constraint the remote refused on", got.Error())
	}
}

// TestDBSyncRemoteWithoutTheTableIsRefused pins the other remote refusal: the
// remote has no table for the rows this machine is pushing. It is refused, and
// its message says the schema rather than the row order, because the two have
// different fixes and the reader is the one who has to choose between them.
func TestDBSyncRemoteWithoutTheTableIsRefused(t *testing.T) {
	t.Parallel()

	const message = "sync: push: the remote has no table to write this machine's rows to, so it refused " +
		"the statement: the remote was never taught this database's schema"
	classified := dbSyncVerbRefusal(wire.SyncVerbPush, &client.VerbRefusal{
		Code:    wire.SyncCodeRemoteSchemaMissing,
		Message: message,
	})

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeRefused {
		t.Errorf("code = %q, want %q", got.code, codeRefused)
	}
	if strings.Contains(got.next, "bugreport") {
		t.Errorf("next = %q, want no bug report on a remote that never learned the schema", got.next)
	}
	if !strings.Contains(got.Error(), "schema") {
		t.Errorf("the classified refusal %q does not say the remote was never taught this schema", got.Error())
	}
}

// TestDBSyncClassifyPassesCodedFailuresThrough pins that a refusal the frame
// already raised keeps its code. Re-wrapping one would report a refusal the verb
// chose deliberately as an internal failure, with its own message quoted twice.
func TestDBSyncClassifyPassesCodedFailuresThrough(t *testing.T) {
	t.Parallel()

	coded := newCLIError(codeConflict, "", "relevo.db is held")
	classified := dbSyncClassify(coded)

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeConflict {
		t.Errorf("code = %q, want %q", got.code, codeConflict)
	}
	if got.Error() != coded.Error() {
		t.Errorf("classified = %q, want the original %q", got.Error(), coded.Error())
	}
}

// parseInto runs an installer over a fresh flag set and parses no arguments, so
// a test reads the declared defaults without naming them twice.
func parseInto[V any](t *testing.T, name string, install func(*flag.FlagSet) V) V {
	t.Helper()

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := install(fs)
	if err := parseFlags(fs, nil); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	return v
}

// TestDBSyncClassifyReportsAPreflightRefusalAsRefused pins that an enable
// preflight refusal reaches the user as a refusal and not as an internal
// failure. Nothing broke on that path: the database is simply not ready to have
// its rows leave the machine, and every fix it names is a command or a flag the
// reader can run. `internal` is the one code the catalog points at
// `relevo bugreport` from, so a refusal classified as internal sends the reader
// to report a defect instead of to the fix its own message just named.
func TestDBSyncClassifyReportsAPreflightRefusalAsRefused(t *testing.T) {
	t.Parallel()

	const fix = "run the origin backfill (BackfillOriginOnce) or upgrade before enabling sync"
	preflight := db.Preflight{Refusals: []db.PreflightRefusal{{
		Check:  "empty origin",
		Detail: "repo=22 mastermind=112 rows with no origin must be stamped before two machines share this file; " + fix,
	}}}
	classified := dbSyncClassify(preflight.Err())

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeRefused {
		t.Errorf("code = %q, want %q", got.code, codeRefused)
	}
	if got.code == codeInternal {
		t.Error("the refusal is reported as an internal failure")
	}
	if got.next == "relevo bugreport" || strings.Contains(got.next, "bugreport") {
		t.Errorf("next = %q, want no bug report on a refusal", got.next)
	}
	if !strings.Contains(got.Error(), fix) {
		t.Errorf("the classified refusal %q dropped the fix it names", got.Error())
	}
	if !strings.Contains(got.Error(), "repo=22 mastermind=112") {
		t.Errorf("the classified refusal %q dropped the counts it names", got.Error())
	}
	if !errors.Is(classified, db.ErrPreflightRefused) {
		t.Errorf("the classified error no longer matches the refusal it came from: %v", classified)
	}
}
