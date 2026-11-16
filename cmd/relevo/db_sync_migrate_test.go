package main

// The migrate verb's CLI shape. None of these tests dials an owner, spawns a
// worker or reaches the network: the gate refuses before the dial, the rest
// are pure functions over flags, results and refusal codes -- the same rule
// the other sync verb tests run under.

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// migrateResult is the answered migration the text and document tests render:
// two origins, a sweep and traffic on both providers.
func migrateResult() *wire.SyncResult {
	return &wire.SyncResult{OK: true, Migrate: &wire.SyncMigrate{
		Rows: 10,
		Origins: []wire.SyncMigrateOrigin{
			{Origin: "laptop", Rows: 7, Objects: 3, Seq: 42},
			{Origin: "contabo", Rows: 3, Objects: 1, Seq: 9},
		},
		ObjectsCreated:  4,
		ObjectsVerified: 4,
		RowsSwept:       2,
		SharedRows:      1,
		R2Put:           2048,
		R2Get:           1000,
		TursoSent:       1500000,
		TursoReceived:   999,
	}}
}

// TestDBSyncMigrateRefusesWithoutTheFlag pins that the confirm gate runs
// before the dial: with no flag the call is a usage refusal naming the flag,
// and no daemon is reached, so CI's lack of one is no obstacle.
//
// The mutation is dropping the gate call, which sends the call to the dial.
// The dial then refuses with another code or hangs on a live socket, so the
// call runs under a second's timeout and exceeding it fails the test.
func TestDBSyncMigrateRefusesWithoutTheFlag(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	done := make(chan error, 1)
	go func() { done <- cmdDBSyncMigrate([]string{}) }()
	select {
	case err := <-done:
		var got *cliError
		if !errors.As(err, &got) {
			t.Fatalf("migrate without the flag = %v, want a cliError", err)
		}
		if got.code != codeUsage {
			t.Errorf("code = %q, want %q", got.code, codeUsage)
		}
		if !strings.Contains(got.Error(), "--confirm-all-upgraded") {
			t.Errorf("refusal %q does not name the flag", got.Error())
		}
	case <-time.After(time.Second):
		t.Fatal("migrate without the flag did not return in a second: the gate must run before the dial")
	}
}

// TestDBSyncMigrateParsesItsFlags pins the flag shape: the confirm flag and
// --json parse, and both default off so the human line is the default answer.
func TestDBSyncMigrateParsesItsFlags(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("db sync migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := dbSyncMigrateFlagSet(fs)
	if err := parseFlags(fs, []string{"--confirm-all-upgraded", "--json"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !*v.confirm || !*v.asJSON {
		t.Errorf("--confirm-all-upgraded --json parsed to %t, %t", *v.confirm, *v.asJSON)
	}

	plain := flag.NewFlagSet("db sync migrate", flag.ContinueOnError)
	plain.SetOutput(io.Discard)
	d := dbSyncMigrateFlagSet(plain)
	if err := parseFlags(plain, nil); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *d.confirm || *d.asJSON {
		t.Errorf("defaults = %t, %t, want both off", *d.confirm, *d.asJSON)
	}
}

// TestDBSyncMigrateRefusesAnUnknownFlag pins that an unknown flag is a usage
// refusal from the flag parser, before the gate and the dial.
func TestDBSyncMigrateRefusesAnUnknownFlag(t *testing.T) {
	_, _, err := captureOutput(t, func() error { return cmdDBSyncMigrate([]string{"--nope"}) })
	var got *cliError
	if !errors.As(err, &got) {
		t.Fatalf("migrate --nope = %v, want a cliError", err)
	}
	if got.code != codeUsage {
		t.Errorf("code = %q, want %q", got.code, codeUsage)
	}
}

// TestDBSyncMigrateTakesNoArguments pins the positional refusal: it runs
// before the dial even when the flag confirms, so a stray argument never
// reaches a daemon.
func TestDBSyncMigrateTakesNoArguments(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return cmdDBSyncMigrate([]string{"--confirm-all-upgraded", "extra"})
	})
	var got *cliError
	if !errors.As(err, &got) {
		t.Fatalf("migrate with an argument = %v, want a cliError", err)
	}
	if got.code != codeUsage {
		t.Errorf("code = %q, want %q", got.code, codeUsage)
	}
	if !strings.Contains(got.Error(), "takes no arguments") {
		t.Errorf("refusal %q does not say the verb takes no arguments", got.Error())
	}
}

// TestDBSyncMigrateText pins the human answer: one line per origin with its
// rows, objects and last sequence, then the totals and the billed bytes. The
// table covers the normal answer, the idempotent one and the empty one; a
// missing Migrate renders as the empty one rather than panicking.
func TestDBSyncMigrateText(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		res  *wire.SyncResult
		want string
	}{
		{
			name: "normal",
			res:  migrateResult(),
			want: "laptop: 7 rows in 3 objects, last seq 42\n" +
				"contabo: 3 rows in 1 objects, last seq 9\n" +
				"migrated 10 rows from 2 origins into 4 objects (4 verified), swept 2 rows, 1 shared rows\n" +
				"turso sent 1.5 MB, received 999 B; r2 put 2.0 kB, get 1.0 kB\n",
		},
		{
			name: "already migrated",
			res:  &wire.SyncResult{OK: true, Migrate: &wire.SyncMigrate{AlreadyMigrated: true}},
			want: "already at format 6; nothing to migrate\n",
		},
		{
			name: "zero origins",
			res:  &wire.SyncResult{OK: true, Migrate: &wire.SyncMigrate{}},
			want: "migrated 0 rows from 0 origins into 0 objects (0 verified), swept 0 rows, 0 shared rows\n" +
				"turso sent 0 B, received 0 B; r2 put 0 B, get 0 B\n",
		},
		{
			name: "nil migrate",
			res:  &wire.SyncResult{OK: true},
			want: "migrated 0 rows from 0 origins into 0 objects (0 verified), swept 0 rows, 0 shared rows\n" +
				"turso sent 0 B, received 0 B; r2 put 0 B, get 0 B\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := dbSyncMigrateText(tc.res); got != tc.want {
				t.Errorf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDBSyncMigrateDocOf pins the --json mapping: the wire counts under the
// document's names, and a missing Migrate is the zero document.
func TestDBSyncMigrateDocOf(t *testing.T) {
	t.Parallel()

	doc := dbSyncMigrateDocOf(migrateResult())
	if doc.AlreadyMigrated || doc.Rows != 10 || len(doc.Origins) != 2 {
		t.Errorf("doc head = %+v, want unmigrated with 10 rows from 2 origins", doc)
	}
	if doc.ObjectsWritten != 4 || doc.ObjectsVerified != 4 || doc.RowsSwept != 2 || doc.SharedRows != 1 {
		t.Errorf("doc counts = %+v, want 4 written, 4 verified, 2 swept, 1 shared", doc)
	}
	if doc.R2PutBytes != 2048 || doc.R2GetBytes != 1000 || doc.TursoSentBytes != 1500000 || doc.TursoReceivedBytes != 999 {
		t.Errorf("doc bytes = %+v, want the wire byte totals", doc)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"already_migrated"`, `"rows"`, `"origins"`, `"objects_written"`,
		`"objects_verified"`, `"rows_swept"`, `"shared_rows"`, `"r2_put_bytes"`,
		`"r2_get_bytes"`, `"turso_sent_bytes"`, `"turso_received_bytes"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("document %s lacks the key %s", raw, key)
		}
	}

	if got := dbSyncMigrateDocOf(&wire.SyncResult{OK: true}); !reflect.DeepEqual(got, dbSyncMigrateDoc{}) {
		t.Errorf("nil migrate doc = %+v, want the zero document", got)
	}
}

// TestDBSyncMigrateIncompleteNamesTheRerun pins the refusal a migration gets
// when late rows arrived mid-run: nothing was swept, so the next hint is the
// verb itself with the flag, not a bug report. Dropping the arm from
// dbSyncVerbRefusalFor kills this (the code becomes internal and the next
// becomes the bug report).
func TestDBSyncMigrateIncompleteNamesTheRerun(t *testing.T) {
	t.Parallel()

	const message = "sync: rows arrived while migrating; run `relevo db sync migrate --confirm-all-upgraded` again"
	classified := dbSyncVerbRefusalFor(wire.SyncVerbMigrate, "", &client.VerbRefusal{
		Code:    wire.SyncCodeMigrateIncomplete,
		Message: message,
	})

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeRefused {
		t.Errorf("code = %q, want %q", got.code, codeRefused)
	}
	if got.next != "relevo db sync migrate --confirm-all-upgraded" {
		t.Errorf("next = %q, want the confirmed rerun", got.next)
	}
	if got.Error() != "refused: "+message {
		t.Errorf("message = %q, want the refusal's own words untouched", got.Error())
	}
	if strings.Contains(got.next, "bugreport") {
		t.Errorf("next = %q, want no bug report on a rerunnable refusal", got.next)
	}
}

// TestDBSyncMigrateVerbWaitingIsRefusedWithStatus pins the refusal the migrate
// verb itself gets while the remote waits for the migration: nothing was
// written, and the fix is status, which shows the wait and names the fix.
func TestDBSyncMigrateVerbWaitingIsRefusedWithStatus(t *testing.T) {
	t.Parallel()

	const message = "sync: migrate: waiting for another machine to migrate the remote"
	classified := dbSyncVerbRefusalFor(wire.SyncVerbMigrate, "", &client.VerbRefusal{
		Code:    wire.SyncCodeMigrateWaiting,
		Message: message,
	})

	var got *cliError
	if !errors.As(classified, &got) {
		t.Fatalf("classified = %v, want a cliError", classified)
	}
	if got.code != codeRefused {
		t.Errorf("code = %q, want %q", got.code, codeRefused)
	}
	if got.next != "relevo db sync status" {
		t.Errorf("next = %q, want %q", got.next, "relevo db sync status")
	}
}

// TestDBSyncMigrateIsInTheUsage pins that the usage names the verb's line and
// its one-sentence rule: run once, on one machine, after every machine runs
// this release, leaving the machine's own outbox alone.
func TestDBSyncMigrateIsInTheUsage(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"relevo db sync migrate --confirm-all-upgraded [--json]",
		"run it once, on one machine, after every machine",
		"the machine's own outbox is untouched",
	} {
		if !strings.Contains(dbSyncUsage, want) {
			t.Errorf("the usage text does not mention %q:\n%s", want, dbSyncUsage)
		}
	}
}
