package main

// Pure tests: the passphrase sources are injected, so nothing here reads a real
// terminal, spawns a worker or reaches the network.

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// withPassphraseSources installs the three sources for one test; tests that use
// it do not run in parallel because the sources are package vars.
func withPassphraseSources(t *testing.T, stdin, env string, tty func(string) ([]byte, bool, error)) {
	t.Helper()
	oldStdin, oldEnv, oldTTY := dbSyncPassphraseStdin, dbSyncPassphraseEnvFn, dbSyncPassphraseTTY
	t.Cleanup(func() { dbSyncPassphraseStdin, dbSyncPassphraseEnvFn, dbSyncPassphraseTTY = oldStdin, oldEnv, oldTTY })
	dbSyncPassphraseStdin = strings.NewReader(stdin)
	dbSyncPassphraseEnvFn = func(string) string { return env }
	dbSyncPassphraseTTY = tty
}

func ttyAnswering(answers ...string) func(string) ([]byte, bool, error) {
	i := 0
	return func(string) ([]byte, bool, error) {
		a := answers[i%len(answers)]
		i++
		return []byte(a), true, nil
	}
}

func TestUnlockReadsStdinOverEnvOverTTY(t *testing.T) {
	withPassphraseSources(t, "from-stdin\n", "from-env", ttyAnswering("from-tty"))
	if got, err := dbSyncPassphrase(true, false); err != nil || string(got) != "from-stdin" {
		t.Fatalf("stdin route = %q, %v; want from-stdin with the newline trimmed", got, err)
	}
	withPassphraseSources(t, "", "from-env", ttyAnswering("from-tty"))
	if got, err := dbSyncPassphrase(false, false); err != nil || string(got) != "from-env" {
		t.Fatalf("env route = %q, %v; want from-env", got, err)
	}
	withPassphraseSources(t, "", "", ttyAnswering("from-tty"))
	if got, err := dbSyncPassphrase(false, false); err != nil || string(got) != "from-tty" {
		t.Fatalf("tty route = %q, %v; want from-tty", got, err)
	}
	withPassphraseSources(t, "\n", "", ttyAnswering("x"))
	if _, err := dbSyncPassphrase(true, false); err == nil {
		t.Error("an empty passphrase was accepted")
	}
}

func TestUnlockNewFromTTYRefusesAMismatchedRepeat(t *testing.T) {
	withPassphraseSources(t, "", "", ttyAnswering("first-secret", "second-secret"))
	_, err := dbSyncPassphrase(false, true)
	if err == nil {
		t.Fatal("a mismatched repeat was accepted")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the refusal echoes a passphrase: %v", err)
	}
	withPassphraseSources(t, "", "", ttyAnswering("same", "same"))
	if got, err := dbSyncPassphrase(false, true); err != nil || string(got) != "same" {
		t.Fatalf("matching repeat = %q, %v", got, err)
	}
}

func TestStatusSaysLockedWhenTheRemoteHasAKeyAndThisMachineDoesNot(t *testing.T) {
	t.Parallel()
	shared, err := db.OpenSplit(t.TempDir()+"/relevo.db", db.Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	doc, err := dbSyncStatusRead(local)
	if err != nil || doc.Key != "none" {
		t.Fatalf("fresh machine key = %q, %v; want none", doc.Key, err)
	}
	if err := local.KVPut(relevosync.KeyRemoteKDF, []byte("1")); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	doc, err = dbSyncStatusRead(local)
	if err != nil || doc.Key != "locked" {
		t.Fatalf("key = %q, %v; want locked", doc.Key, err)
	}
	if line := dbSyncStatusLine(doc); !strings.Contains(line, "locked: run relevo db sync unlock") {
		t.Errorf("status line = %q, want the unlock hint", line)
	}
	if err := relevosync.SetKey(local, []byte("k"), time.Now()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if doc, _ = dbSyncStatusRead(local); doc.Key != "unlocked" {
		t.Errorf("key = %q, want unlocked", doc.Key)
	}
}
