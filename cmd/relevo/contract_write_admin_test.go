package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestContractWriteAdminShape pins every admin write result document from
// literal fixtures through its pure builder. It is the shape half of the admin
// contract: the CLI half (one document, notices on stderr) is pinned by the
// per-verb tests below.
func TestContractWriteAdminShape(t *testing.T) {
	for _, c := range outcomeDocFixtures() {
		t.Run(c.golden, func(t *testing.T) {
			assertGolden(t, c.golden, encodeDoc(t, c.doc))
		})
	}
}

// TestContractWriteAdminErrorClasses pins the admin classifier's table: every
// sentinel a caller probes maps to its catalog code and stays reachable
// through errors.Is, and an error nobody classified becomes internal.
func TestContractWriteAdminErrorClasses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want errorCode
	}{
		{"mastermind missing", fmt.Errorf("lookup: %w", mastermind.ErrNotFound), codeMastermindNotFound},
		{"mastermind name taken", fmt.Errorf("create: %w", mastermind.ErrNameTaken), codeConflict},
		{"mastermind in use", fmt.Errorf("forget: %w", mastermind.ErrInUse), codeConflict},
		{"mastermind name invalid", fmt.Errorf("rename: %w", mastermind.ErrInvalid), codeUsage},
		{"serve client missing", fmt.Errorf("revoke: %w", serve.ErrNoSuchClient), codeClientNotFound},
		{"serve client enrolled", fmt.Errorf("enroll: %w", serve.ErrAlreadyEnrolled), codeConflict},
		{"serve tls exists", fmt.Errorf("init: %w", serve.ErrTLSExists), codeConflict},
		{"store binding missing", fmt.Errorf("load: %w", store.ErrNotFound), codeBindingNotFound},
		{"client unreachable", fmt.Errorf("whoami: %w", client.ErrUnreachable), codeRemoteUnreachable},
		{"release offline", fmt.Errorf("latest: %w", release.ErrOffline), codeRemoteUnreachable},
		{"unclassified", errors.New("something nobody classified"), codeInternal},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coded := outcomeError(c.err)
			var ce *cliError
			if !errors.As(coded, &ce) {
				t.Fatalf("outcomeError(%v) is not a coded error", c.err)
			}
			if ce.code != c.want {
				t.Errorf("code = %q, want %q", ce.code, c.want)
			}
			if ce.message == "" {
				t.Error("message is empty")
			}
			if _, ok := catalog[ce.code]; !ok {
				t.Errorf("code %q is not in the catalog", ce.code)
			}
			// Every classified cause stays reachable: a caller probing the
			// library sentinel with errors.Is still finds it under the code.
			if c.want != codeInternal && !errors.Is(coded, c.err) {
				t.Errorf("errors.Is(%v) = false, want the sentinel reachable", c.err)
			}
		})
	}

	t.Run("already coded passes through", func(t *testing.T) {
		orig := fail(codeUsage, "already coded")
		got := outcomeError(orig)
		var ce *cliError
		if !errors.As(got, &ce) || ce.code != codeUsage {
			t.Errorf("outcomeError(%v) = %v, want the coded error unchanged", orig, got)
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if got := outcomeError(nil); got != nil {
			t.Errorf("outcomeError(nil) = %v, want nil", got)
		}
	})
}

// assertWriteJSONNotices runs one admin verb in --json mode and returns its
// stdout and stderr after asserting the stream rule where the verb keeps
// supplementary lines: stdout is exactly one JSON object and every notice went
// to stderr.
func assertWriteJSONNotices(t *testing.T, args ...string) (stdout, stderr []byte) {
	t.Helper()
	stdout, stderr, err := captureOutput(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
	}
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("%v: stdout = %q, want exactly one JSON object", args, stdout)
	}
	return stdout, stderr
}

// decodeAdminDoc unmarshals one admin document into T, naming the verb on a
// shape mismatch.
func decodeAdminDoc[T any](t *testing.T, stdout []byte, args ...string) T {
	t.Helper()
	var doc T
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("%v: %v (stdout: %s)", args, err, stdout)
	}
	return doc
}

// seedAdminServer records one server through the human path, so a later rm has
// something to remove.
func seedAdminServer(t *testing.T, name, url string) {
	t.Helper()
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "server", "add", name, url, "--ca", "system"})
	}); err != nil {
		t.Fatalf("config server add %s: %v (stderr: %s)", name, err, stderr)
	}
}

// TestContractWriteAdminJSON is the CLI half of the admin contract: every verb
// a fixture can reach end to end prints one document under --json, with
// supplementary lines on stderr where the verb has them.
func TestContractWriteAdminJSON(t *testing.T) {
	t.Run("config set", func(t *testing.T) {
		docsEnv(t)
		stdout := assertWriteJSON(t, "config", "set", "policy.max_switches", `2`, "--json")
		doc := decodeAdminDoc[ConfigWriteDoc](t, stdout, "config set")
		if doc.Section != "policy" || doc.Path != "policy.max_switches" || doc.Version == 0 {
			t.Errorf("doc = %+v, want the policy.max_switches write at a nonzero version", doc)
		}
	})

	t.Run("config unset", func(t *testing.T) {
		docsEnv(t)
		if _, _, err := captureOutput(t, func() error {
			return run([]string{"config", "set", "policy.max_switches", `2`})
		}); err != nil {
			t.Fatalf("config set: %v", err)
		}
		stdout := assertWriteJSON(t, "config", "unset", "policy.max_switches", "--json")
		doc := decodeAdminDoc[ConfigWriteDoc](t, stdout, "config unset")
		if doc.Section != "policy" || doc.Path != "policy.max_switches" || doc.Version == 0 {
			t.Errorf("doc = %+v, want the removed policy path at a nonzero version", doc)
		}
	})

	t.Run("config import", func(t *testing.T) {
		docsEnv(t)
		path := filepath.Join(t.TempDir(), "config.json")
		body := `{"candidates":[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		stdout := assertWriteJSON(t, "config", "import", path, "--json")
		doc := decodeAdminDoc[ConfigImportDoc](t, stdout, "config import")
		if !slices.Contains(doc.Sections, "candidates") || doc.Version == 0 {
			t.Errorf("doc = %+v, want the imported candidates section", doc)
		}
		if doc.Warnings == nil {
			t.Error("warnings is nil, want a list")
		}
	})

	t.Run("config init", func(t *testing.T) {
		initRoot(t)
		bin := t.TempDir()
		stubBinary(t, bin, "claude")
		t.Setenv("PATH", bin)
		stdout, stderr := assertWriteJSONNotices(t, "config", "init", "--json")
		doc := decodeAdminDoc[ConfigImportDoc](t, stdout, "config init")
		if !slices.Equal(doc.Sections, []string{"candidates", "actors", "policy"}) {
			t.Errorf("sections = %v, want candidates/actors/policy", doc.Sections)
		}
		if doc.Version == 0 {
			t.Error("version is zero, want the config version the write left behind")
		}
		if !strings.Contains(string(stderr), "wrote candidates") {
			t.Errorf("stderr = %q, want the wrote-candidates notice", stderr)
		}
	})

	t.Run("config agents", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
		stdout, stderr := assertWriteJSONNotices(t, "config", "agents", "--kind", "claude", "--dry-run", "--json")
		doc := decodeAdminDoc[ConfigAgentsDoc](t, stdout, "config agents")
		if len(doc.Installed) == 0 {
			t.Fatalf("doc = %+v, want the installed definitions", doc)
		}
		if doc.Installed[0].Path == "" || doc.Installed[0].Outcome == "" {
			t.Errorf("row = %+v, want a path and an outcome", doc.Installed[0])
		}
		if !strings.Contains(string(stderr), "would write") {
			t.Errorf("stderr = %q, want the would-write notice", stderr)
		}
	})

	t.Run("config server add", func(t *testing.T) {
		docsEnv(t)
		stdout, stderr := assertWriteJSONNotices(t,
			"config", "server", "add", "zen", "https://zen.example.test:7777", "--ca", "system", "--json")
		doc := decodeAdminDoc[ConfigServerAddDoc](t, stdout, "config server add")
		if doc.Name != "zen" || doc.URL != "https://zen.example.test:7777" {
			t.Errorf("doc = %+v, want the zen entry", doc)
		}
		if !strings.Contains(string(stderr), "client id") {
			t.Errorf("stderr = %q, want the generated key's client-id notice", stderr)
		}
	})

	t.Run("config server rm", func(t *testing.T) {
		docsEnv(t)
		seedAdminServer(t, "zen", "https://zen.example.test:7777")
		stdout := assertWriteJSON(t, "config", "server", "rm", "zen", "--json")
		doc := decodeAdminDoc[ConfigServerRmDoc](t, stdout, "config server rm")
		if doc.Name != "zen" || !doc.Removed {
			t.Errorf("doc = %+v, want zen removed", doc)
		}
	})

	t.Run("config secret set", func(t *testing.T) {
		docsEnv(t)
		stdout, stderr, err := runWithStdin(t, "s3cret\n", "config", "secret", "set", "typesafe", "--json")
		if err != nil {
			t.Fatalf("config secret set --json: %v (stderr: %s)", err, stderr)
		}
		doc := decodeAdminDoc[ConfigSecretDoc](t, stdout, "config secret set")
		if doc.Name != "typesafe" {
			t.Errorf("doc = %+v, want typesafe", doc)
		}
		if strings.Contains(string(stdout), "s3cret") {
			t.Errorf("stdout carries the value: %s", stdout)
		}
	})

	t.Run("config secret rm", func(t *testing.T) {
		docsEnv(t)
		if _, _, err := runWithStdin(t, "s3cret\n", "config", "secret", "set", "typesafe"); err != nil {
			t.Fatalf("config secret set: %v", err)
		}
		stdout, stderr, err := runWithStdin(t, "", "config", "secret", "rm", "typesafe", "--json")
		if err != nil {
			t.Fatalf("config secret rm --json: %v (stderr: %s)", err, stderr)
		}
		doc := decodeAdminDoc[ConfigSecretDoc](t, stdout, "config secret rm")
		if doc.Name != "typesafe" {
			t.Errorf("doc = %+v, want typesafe", doc)
		}
	})

	t.Run("mastermind init", func(t *testing.T) {
		docsEnv(t)
		stdout, stderr := assertWriteJSONNotices(t, "mastermind", "init", "--kind", "claude", "--session", "sess-1", "--json")
		doc := decodeAdminDoc[MasterMindDoc](t, stdout, "mastermind init")
		if doc.ID == "" || doc.Name == "" || doc.State != string(mastermind.InitCreated) {
			t.Errorf("doc = %+v, want a created record", doc)
		}
		if !strings.Contains(string(stderr), "export RELEVO_MASTERMIND=") {
			t.Errorf("stderr = %q, want the export notice", stderr)
		}
	})

	t.Run("mastermind enable", func(t *testing.T) {
		docsEnv(t)
		stdout, stderr := assertWriteJSONNotices(t, "mastermind", "enable", "--kind", "claude", "--session", "sess-e", "--json")
		doc := decodeAdminDoc[MasterMindDoc](t, stdout, "mastermind enable")
		if doc.ID == "" || doc.State == "" {
			t.Errorf("doc = %+v, want a registered consent", doc)
		}
		if !strings.Contains(string(stderr), "export RELEVO_MASTERMIND=") {
			t.Errorf("stderr = %q, want the export notice", stderr)
		}
	})

	t.Run("mastermind disable", func(t *testing.T) {
		docsEnv(t)
		if _, _, err := captureOutput(t, func() error {
			return run([]string{"mastermind", "enable", "--kind", "claude", "--session", "sess-d"})
		}); err != nil {
			t.Fatalf("mastermind enable: %v", err)
		}
		stdout := assertWriteJSON(t, "mastermind", "disable", "--kind", "claude", "--session", "sess-d", "--json")
		doc := decodeAdminDoc[MasterMindDoc](t, stdout, "mastermind disable")
		if doc.State != "forgotten" || doc.ID == "" {
			t.Errorf("doc = %+v, want the record forgotten", doc)
		}
	})

	t.Run("mastermind reset", func(t *testing.T) {
		docsEnv(t)
		stdout := assertWriteJSON(t, "mastermind", "reset", "--kind", "claude", "--session", "sess-r", "--json")
		doc := decodeAdminDoc[MasterMindDoc](t, stdout, "mastermind reset")
		if doc.State != "reset" {
			t.Errorf("doc = %+v, want state reset", doc)
		}
	})

	t.Run("mastermind rename", func(t *testing.T) {
		docsEnv(t)
		created := decodeAdminDoc[MasterMindDoc](t, initMastermindJSON(t, "sess-rename"), "mastermind init")
		stdout := assertWriteJSON(t, "mastermind", "rename", created.ID, "reviewer-2", "--json")
		doc := decodeAdminDoc[MasterMindRenameDoc](t, stdout, "mastermind rename")
		if doc.ID != created.ID || doc.Name != "reviewer-2" || doc.OldName != created.Name {
			t.Errorf("doc = %+v, want %s renamed from %q", doc, created.ID, created.Name)
		}
	})

	t.Run("mastermind forget", func(t *testing.T) {
		docsEnv(t)
		created := decodeAdminDoc[MasterMindDoc](t, initMastermindJSON(t, "sess-forget"), "mastermind init")
		stdout := assertWriteJSON(t, "mastermind", "forget", created.ID, "--json")
		doc := decodeAdminDoc[MasterMindDoc](t, stdout, "mastermind forget")
		if doc.ID != created.ID || doc.State != "forgotten" {
			t.Errorf("doc = %+v, want %s forgotten", doc, created.ID)
		}
	})

	t.Run("serve init", func(t *testing.T) {
		docsEnv(t)
		stdout, stderr := assertWriteJSONNotices(t, "serve", "init", "--json")
		doc := decodeAdminDoc[ServeInitDoc](t, stdout, "serve init")
		if doc.Root == "" || doc.Fingerprint == "" || !doc.Created {
			t.Errorf("doc = %+v, want a fresh initialised root", doc)
		}
		if !strings.Contains(string(stderr), "clients:") {
			t.Errorf("stderr = %q, want the enroll hint", stderr)
		}

		// The second run reports created false, the fresh run's twin.
		docsAgain := decodeAdminDoc[ServeInitDoc](t, assertWriteJSON(t, "serve", "init", "--json"), "serve init again")
		if docsAgain.Created || docsAgain.Fingerprint != doc.Fingerprint {
			t.Errorf("second doc = %+v, want the same fingerprint and created false", docsAgain)
		}
	})

	t.Run("serve enroll and revoke", func(t *testing.T) {
		docsEnv(t)
		if _, err := runServeInit(t); err != nil {
			t.Fatalf("serve init: %v", err)
		}

		kp, err := remote.Generate()
		if err != nil {
			t.Fatalf("remote.Generate: %v", err)
		}
		line := remote.MarshalPublic(kp.Public, "alice")
		stdout := assertWriteJSON(t, "serve", "enroll", "--label", "alice", "--key", line, "--json")
		enrolled := decodeAdminDoc[ServeEnrollDoc](t, stdout, "serve enroll")
		if enrolled.Label != "alice" || enrolled.ID == "" {
			t.Fatalf("enrolled = %+v, want alice with an id", enrolled)
		}

		revoked := decodeAdminDoc[ServeRevokeDoc](t,
			assertWriteJSON(t, "serve", "revoke", enrolled.ID, "--json"), "serve revoke")
		if revoked.ID != enrolled.ID {
			t.Errorf("revoked = %+v, want id %s", revoked, enrolled.ID)
		}
	})

	t.Run("serve gc", func(t *testing.T) {
		docsEnv(t)
		if _, err := runServeInit(t); err != nil {
			t.Fatalf("serve init: %v", err)
		}
		stdout := assertWriteJSON(t, "serve", "gc", "--abandoned", "1h", "--json")
		doc := decodeAdminDoc[ServeGCDoc](t, stdout, "serve gc")
		if doc.DryRun {
			t.Errorf("doc = %+v, want a real sweep", doc)
		}
		if doc.Results == nil {
			t.Error("results is nil, want a list")
		}
	})

	t.Run("update check", func(t *testing.T) {
		docsEnv(t)
		stdout := assertWriteJSON(t, "update", "--check", "--json")
		doc := decodeAdminDoc[updateDoc](t, stdout, "update --check")
		if doc.Running == "" || doc.Exe == "" || doc.Action == "" || doc.Message == "" {
			t.Errorf("doc = %+v, want the check block as a document", doc)
		}
	})
}

// initMastermindJSON registers one record in --json mode and returns the
// document's raw bytes.
func initMastermindJSON(t *testing.T, session string) []byte {
	t.Helper()
	stdout, _ := assertWriteJSONNotices(t, "mastermind", "init", "--kind", "claude", "--session", session, "--json")
	return stdout
}

// TestContractWriteAdminHuman pins the human default where it is not empty and
// the unchanged-empty default where it is: no admin verb's human rendering
// moved.
func TestContractWriteAdminHuman(t *testing.T) {
	docsEnv(t)

	if _, stderr, err := runWithStdin(t, "s3cret\n", "config", "secret", "set", "typesafe"); err != nil {
		t.Fatalf("config secret set: %v (stderr: %s)", err, stderr)
	}
	assertHumanUnchanged(t, "stored secret typesafe", "config", "secret", "set", "typesafe")
	assertHumanUnchanged(t, "removed secret typesafe", "config", "secret", "rm", "typesafe")

	seedAdminServer(t, "zen", "https://zen.example.test:7777")
	assertHumanUnchanged(t, "removed server zen", "config", "server", "rm", "zen")

	assertHumanUnchanged(t, "MasterMind ", "mastermind", "init", "--kind", "claude", "--session", "sess-human")
	assertHumanUnchanged(t, "export RELEVO_MASTERMIND=", "mastermind", "init", "--kind", "claude", "--session", "sess-human-2")

	if _, err := runServeInit(t); err != nil {
		t.Fatalf("serve init: %v", err)
	}
	assertHumanUnchanged(t, "fingerprint", "serve", "init")

	// update --check keeps its four-line block.
	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"update", "--check"}) })
	if err != nil {
		t.Fatalf("update --check: %v (stderr: %s)", err, stderr)
	}
	for _, line := range []string{"running  ", "exe      ", "action   ", "         "} {
		if !strings.Contains(string(stdout), line) {
			t.Errorf("update --check stdout = %q, want the %q line", stdout, line)
		}
	}

	// config set and serve revoke print nothing when they succeed; that is
	// their unchanged human default.
	quiet, stderr, err := captureOutput(t, func() error { return run([]string{"config", "set", "policy.order.builder", `["claude/p/m"]`}) })
	if err != nil {
		t.Fatalf("config set: %v (stderr: %s)", err, stderr)
	}
	if len(quiet) != 0 {
		t.Errorf("config set human stdout = %q, want empty", quiet)
	}
}

// TestContractWriteAdminForcedFailures is the forced-failure table for the
// admin verbs a fixture can reach: each names its catalog code, its exit and
// the next command the frame prints, and nothing may reach stdout or stderr
// before report.
func TestContractWriteAdminForcedFailures(t *testing.T) {
	docsEnv(t)

	cases := []struct {
		name string
		args []string
		code errorCode
		next string
	}{
		{"config set bad args", []string{"config", "set", "only-one"}, codeUsage, "relevo help"},
		{"config unset bad args", []string{"config", "unset"}, codeUsage, "relevo help"},
		{"config import bad args", []string{"config", "import"}, codeUsage, "relevo help"},
		{"config agents bad kind", []string{"config", "agents", "--kind", "unknown-kind"}, codeUsage, "relevo help"},
		{"config server add bad args", []string{"config", "server", "add", "zen"}, codeUsage, "relevo help"},
		{"config server rm unknown", []string{"config", "server", "rm", "nosuch"}, codeServerNotFound, "relevo config server list"},
		{"config secret set unknown", []string{"config", "secret", "set", "bogus"}, codeUsage, "relevo help"},
		{"config secret rm unknown", []string{"config", "secret", "rm", "bogus"}, codeUsage, "relevo help"},
		{"mastermind init hook kind", []string{"mastermind", "init", "--hook", "bogus", "--json"}, codeUsage, "relevo help"},
		{"mastermind enable half pair", []string{"mastermind", "enable", "--kind", "claude"}, codeUsage, "relevo help"},
		{"mastermind disable half pair", []string{"mastermind", "disable", "--kind", "claude"}, codeUsage, "relevo help"},
		{"mastermind reset extra arg", []string{"mastermind", "reset", "extra"}, codeUsage, "relevo help"},
		{"mastermind rename bad args", []string{"mastermind", "rename", "only-one"}, codeUsage, "relevo help"},
		{"mastermind rename unknown", []string{"mastermind", "rename", "pl_nosuchrecord", "new-name"}, codeMastermindNotFound, "relevo mastermind list"},
		{"mastermind forget unknown", []string{"mastermind", "forget", "pl_nosuchrecord"}, codeMastermindNotFound, "relevo mastermind list"},
		{"serve enroll bad args", []string{"serve", "enroll", "--key", "ed25519 AAAA alice"}, codeUsage, "relevo help"},
		{"serve revoke bad args", []string{"serve", "revoke"}, codeUsage, "relevo help"},
		{"serve gc bad args", []string{"serve", "gc"}, codeUsage, "relevo help"},
		{"serve unbind bad args", []string{"serve", "unbind", "some-binding"}, codeUsage, "relevo help"},
		{"update bad to", []string{"update", "--to", "bogus"}, codeUsage, "relevo help"},
		{"update local build", []string{"update", "--to", "v9.9.9"}, codeRefused, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			docsEnv(t)
			stdout, stderr, runErr := captureOutput(t, func() error { return run(c.args) })
			ce := requireCLIError(t, runErr, c.code, c.next)

			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != catalogExit(c.code) {
				t.Errorf("exit = %v, want %d (code %s)", runErr, catalogExit(c.code), c.code)
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want empty before report", stderr)
			}
			if ce.message == "" {
				t.Error("message is empty")
			}
		})
	}
}

// TestContractWriteAdminStateFailures forces the state-conflict and unknown-id
// classes a fresh fixture cannot reach in one call: config init twice, and a
// serve root that is initialised but holds no such client.
func TestContractWriteAdminStateFailures(t *testing.T) {
	// serve init's only usage failure is the flag package's own: --json is
	// accepted, and an unknown flag exits 2 with the flag package's usage on
	// stderr (the one pre-existing stderr line no conversion owns).
	t.Run("serve init bad flag", func(t *testing.T) {
		docsEnv(t)
		stdout, stderr, runErr := captureOutput(t, func() error {
			return run([]string{"serve", "init", "--bogus", "--json"})
		})
		ce := requireCLIError(t, runErr, codeUsage, "relevo help")
		if ce.message == "" {
			t.Error("message is empty")
		}
		if len(stdout) != 0 {
			t.Errorf("stdout = %q, want empty before report", stdout)
		}
		if !strings.Contains(string(stderr), "flag provided but not defined") {
			t.Errorf("stderr = %q, want the flag package's own usage", stderr)
		}
	})

	t.Run("config init conflict", func(t *testing.T) {
		initRoot(t)
		bin := t.TempDir()
		stubBinary(t, bin, "claude")
		t.Setenv("PATH", bin)
		if _, stderr, err := captureOutput(t, func() error { return run([]string{"config", "init"}) }); err != nil {
			t.Fatalf("first config init: %v (stderr: %s)", err, stderr)
		}
		stdout, _, runErr := captureOutput(t, func() error { return run([]string{"config", "init", "--json"}) })
		requireCLIError(t, runErr, codeConflict, "")
		if len(stdout) != 0 {
			t.Errorf("stdout = %q, want empty before report", stdout)
		}
	})

	t.Run("serve revoke unknown client", func(t *testing.T) {
		docsEnv(t)
		state := t.TempDir()
		if _, err := runServeInit(t, "--state", state); err != nil {
			t.Fatalf("serve init: %v", err)
		}
		stdout, _, runErr := captureOutput(t, func() error {
			return run([]string{"serve", "revoke", "SHA256:nosuch", "--state", state})
		})
		requireCLIError(t, runErr, codeClientNotFound, "relevo serve clients")
		if len(stdout) != 0 {
			t.Errorf("stdout = %q, want empty before report", stdout)
		}
	})

	t.Run("serve unbind unknown owner", func(t *testing.T) {
		docsEnv(t)
		state := t.TempDir()
		if _, err := runServeInit(t, "--state", state); err != nil {
			t.Fatalf("serve init: %v", err)
		}
		stdout, _, runErr := captureOutput(t, func() error {
			return run([]string{"serve", "unbind", "--owner", "nosuch", "webshop", "--state", state})
		})
		requireCLIError(t, runErr, codeClientNotFound, "relevo serve clients")
		if len(stdout) != 0 {
			t.Errorf("stdout = %q, want empty before report", stdout)
		}
	})

	// The write serve verbs refuse an uninitialised root with not_available and
	// the command that fixes it, the same shape the read verbs already have.
	for _, c := range []struct {
		name string
		args []string
	}{
		{"serve revoke uninitialised", []string{"serve", "revoke", "SHA256:x"}},
		{"serve gc uninitialised", []string{"serve", "gc", "--abandoned", "1h"}},
		{"serve unbind uninitialised", []string{"serve", "unbind", "--owner", "nosuch", "webshelf"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := append(append([]string{}, c.args...), "--state", t.TempDir())
			stdout, _, runErr := captureOutput(t, func() error { return run(args) })
			requireCLIError(t, runErr, codeNotAvailable, "relevo serve init")
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
		})
	}
}

// TestContractWriteAdminOffline is the round's CI rule made explicit for the
// admin verbs a test reaches: `config server add` is only exercised with
// --ca/--insecure (no --fingerprint, so the WhoAmI dial never runs), and
// `update --check` never fetches because this test binary is a local build.
// Both suites would fail loudly (a dial to a bogus host, a fetch of the real
// release feed) rather than pass, so the rule is carried by construction.
func TestContractWriteAdminOffline(t *testing.T) {
	docsEnv(t)

	// --ca: no fingerprint to pin the transport to, so the verb records the
	// entry and returns before any WhoAmI. The generated client key's line is
	// a notice on stderr under --json.
	stdout, stderr := assertWriteJSONNotices(t, "config", "server", "add", "offline", "https://offline.example.test:7777", "--ca", "system", "--json")
	doc := decodeAdminDoc[ConfigServerAddDoc](t, stdout, "config server add --ca")
	if doc.Name != "offline" {
		t.Errorf("doc = %+v, want the recorded entry", doc)
	}
	if !strings.Contains(string(stderr), "client id") {
		t.Errorf("stderr = %q, want the generated key's client-id notice", stderr)
	}

	// --check: the fetch is skipped for every kind, so this is a no-network
	// path by construction.
	stdout = assertWriteJSON(t, "update", "--check", "--json")
	check := decodeAdminDoc[updateDoc](t, stdout, "update --check")
	if check.Action == "" {
		t.Errorf("check = %+v, want an action word", check)
	}
}
