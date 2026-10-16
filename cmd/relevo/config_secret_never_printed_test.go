package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// A secret value is stored where the CLI can reach it, so the surfaces that
// print the config are exactly where it would leak. These tests pin each of
// them with a canary: the value is written through the real `secret set` path
// and every command a person would run in this situation is checked for it.
//
// No test here reaches a network or spawns anything: they are the in-process
// `run` entry point with a temporary home.

// canarySecret is the plaintext these tests watch for. It is a value nothing
// else in the tree holds, so finding it means a surface printed it.
const canarySecret = "CANARY-3f9a-plaintext-that-must-never-travel"

// secretFixtureKey is the key the canary is sealed under, so the document's
// export path runs too.
var secretFixtureKey = []byte("0123456789abcdef0123456789abcdef")

// seedSecret sets a value through the CLI, so the revision the write records is
// the one a real machine would hold rather than one a test wrote directly.
func seedSecret(t *testing.T, name, value string) {
	t.Helper()
	orig := os.Stdin
	os.Stdin = stdinFile(t, value)
	defer func() { os.Stdin = orig }()
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "secret", "set", name})
	}); err != nil {
		t.Fatalf("config secret set %s: %v (stderr: %s)", name, err, stderr)
	}
}

// localOf is the machine-local file the sync package writes its key to.
func localOf(t *testing.T, d *db.DB) relevosync.Local {
	t.Helper()
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return local
}

// seedConfigAndSecret leaves a machine with a candidate and a secret, which is
// the smallest state in which every surface below answers rather than refusing
// for want of configuration.
func seedConfigAndSecret(t *testing.T) {
	t.Helper()
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", secretFixtureCandidates})
	}); err != nil {
		t.Fatalf("config set candidates: %v (stderr: %s)", err, stderr)
	}
	seedSecret(t, config.SecretTypesafe, canarySecret)
}

// secretFixtureCandidates is one candidate, so `config get candidates` has
// something to print.
const secretFixtureCandidates = `[{"name":"m","harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`

// surface is one command, and whether its exit code means it answered.
type surface struct {
	args []string
	// reportsVerdict marks a command whose non-zero exit is its answer rather
	// than a failure: doctor exits 1 on a machine with something wrong with it,
	// which is exactly the machine a person runs it on.
	reportsVerdict bool
}

// surfacesToCheck is every command a person runs when a machine holds a secret
// and something has to be looked at: the ones that read the config, the ones
// that list what is stored, the ones that report on the machine, and the
// revision log.
//
// `db sync status` is not here because it answers through the running daemon's
// socket, and a machine with no daemon has nothing for it to report; the text
// that surface would show is the trouble marker, which the sync package pins
// without a socket.
func surfacesToCheck() []surface {
	return []surface{
		{args: []string{"config"}},
		{args: []string{"config", "export"}},
		{args: []string{"config", "get", "candidates"}},
		{args: []string{"config", "log"}},
		{args: []string{"config", "log", "--json"}},
		{args: []string{"config", "secret", "list"}},
		{args: []string{"config", "secret", "list", "--json"}},
		{args: []string{"doctor"}, reportsVerdict: true},
		{args: []string{"doctor", "--json"}, reportsVerdict: true},
	}
}

// TestNoSecretValueIsPrinted pins every surface at once: with the canary stored,
// not one of these commands prints it. A surface that did would be a leak into a
// terminal, a log, or a bugreport someone pastes into an issue.
func TestNoSecretValueIsPrinted(t *testing.T) {
	initRoot(t)
	seedConfigAndSecret(t)

	for _, s := range surfacesToCheck() {
		out, stderr, err := captureOutput(t, func() error { return run(s.args) })
		// A surface that exits early prints nothing for the wrong reason, and
		// the check below would pass anyway. So the only surfaces allowed to
		// fail here are the ones whose exit code is their answer.
		if err != nil && !s.reportsVerdict {
			t.Fatalf("relevo %s: %v (stderr: %s)", strings.Join(s.args, " "), err, stderr)
		}
		if len(out)+len(stderr) == 0 {
			t.Errorf("relevo %s printed nothing at all, so the check below proves nothing",
				strings.Join(s.args, " "))
		}
		for stream, text := range map[string][]byte{"stdout": out, "stderr": stderr} {
			if strings.Contains(string(text), canarySecret) {
				t.Errorf("relevo %s printed the secret on %s:\n%s", strings.Join(s.args, " "), stream, text)
			}
		}
	}
}

// TestTheRevisionLogNamesTheSecretAndNotItsValue pins the surface the earlier
// one only covers by accident: a revision is the one place a value could be
// read back long after the write, and the `set` change it records names the
// secret alone.
func TestTheRevisionLogNamesTheSecretAndNotItsValue(t *testing.T) {
	initRoot(t)
	seedSecret(t, config.SecretTypesafe, canarySecret)

	out, stderr, err := captureOutput(t, func() error { return run([]string{"config", "log", "--json"}) })
	if err != nil {
		t.Fatalf("config log --json: %v (stderr: %s)", err, stderr)
	}
	var list []revisionJSON
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("log is not JSON: %v\n%s", err, out)
	}
	found := false
	for _, r := range list {
		if !strings.Contains(string(r.Changes), "secret."+config.SecretTypesafe) {
			continue
		}
		found = true
		if strings.Contains(string(r.Changes), canarySecret) || strings.Contains(r.Message, canarySecret) {
			t.Fatalf("revision #%d carries the value: %s", r.Rev, r.Changes)
		}
	}
	if !found {
		t.Errorf("no revision names the secret: the write recorded nothing a reader could see\n%s", out)
	}

	// The one-revision view carries the snapshot too, which is the config
	// document; the secret table is not part of it.
	revs, stderr, err := captureOutput(t, func() error { return run([]string{"config", "log"}) })
	if err != nil {
		t.Fatalf("config log: %v (stderr: %s)", err, stderr)
	}
	if strings.Contains(string(revs), canarySecret) {
		t.Errorf("config log printed the value:\n%s", revs)
	}
}

// TestTheSupersedingRevisionOfASecretCarriesNoValue pins the value-bearing
// revision path this document must not use. A secret that lost to a newer local
// edit is recorded as lost, naming the secret and the reason, and a reader
// rolling back to that revision gets the config back unchanged.
func TestTheSupersedingRevisionOfASecretCarriesNoValue(t *testing.T) {
	initRoot(t)
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	store := config.Open(d)
	sealed, err := relevosync.SealSecret(relevosync.Keyring{Epoch: 1, Keys: map[int][]byte{1: secretFixtureKey}}, config.SecretTypesafe, []byte(canarySecret))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	doc := store.SecretsDoc()
	if err := d.LocalOrSelf().Tx(func(tx *db.Tx) error {
		return doc.SupersedeIn(tx, config.SecretTypesafe, sealed, "laptop",
			"an entry stamped 5 from laptop is older than the one applied, stamped 9 from contabo")
	}); err != nil {
		t.Fatalf("SupersedeIn: %v", err)
	}

	for _, args := range [][]string{
		{"config", "log"},
		{"config", "log", "--json"},
		{"config", "export"},
	} {
		out, stderr, err := captureOutput(t, func() error { return run(args) })
		if err != nil {
			t.Fatalf("relevo %s: %v (stderr: %s)", strings.Join(args, " "), err, stderr)
		}
		for stream, text := range map[string][]byte{"stdout": out, "stderr": stderr} {
			if strings.Contains(string(text), canarySecret) {
				t.Errorf("relevo %s printed the superseded value on %s:\n%s", strings.Join(args, " "), stream, text)
			}
			if strings.Contains(string(text), strings.Trim(string(sealed), `"{}`)) {
				t.Errorf("relevo %s printed the envelope on %s:\n%s", strings.Join(args, " "), stream, text)
			}
		}
	}
}

// TestAnArrivingSecretIsRecordedByNameAndOrigin pins the applied path through the
// CLI, which is the surface that reports what a peer sent.
func TestAnArrivingSecretIsRecordedByNameAndOrigin(t *testing.T) {
	initRoot(t)
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	store := config.Open(d)
	sealed, err := relevosync.SealSecret(relevosync.Keyring{Epoch: 1, Keys: map[int][]byte{1: secretFixtureKey}}, config.SecretTypesafe, []byte(canarySecret))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	// The key is stored before the arrival, because that is the order a real
	// machine is in: an unlocked machine, receiving an entry.
	if err := relevosync.SetKey(localOf(t, d), secretFixtureKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	doc := store.SecretsDoc()
	if err := d.LocalOrSelf().Tx(func(tx *db.Tx) error {
		refusal, err := doc.ApplyIn(tx, config.SecretTypesafe, sealed, "laptop")
		if refusal != nil || err != nil {
			t.Fatalf("ApplyIn: refusal %v, err %v", refusal, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("apply the arriving secret: %v", err)
	}

	out, stderr, err := captureOutput(t, func() error { return run([]string{"config", "log"}) })
	if err != nil {
		t.Fatalf("config log: %v (stderr: %s)", err, stderr)
	}
	text := string(out) + string(stderr)
	if !strings.Contains(text, "sync:laptop") {
		t.Errorf("the arrival is not recorded as one from laptop:\n%s", out)
	}
	if strings.Contains(text, canarySecret) {
		t.Errorf("the arrival printed the value:\n%s", out)
	}
}
