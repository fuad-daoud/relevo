package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/serve"
)

// docsEnv points a test's state and config roots at fresh temp directories,
// the fixture every document case needs before it runs a verb.
func docsEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
}

// runDocs runs one verb, capturing its streams, and returns stdout. It fails
// the test on a non-nil error, so the document cases only pin the happy path.
func runDocs(t *testing.T, args ...string) []byte {
	t.Helper()
	stdout, stderr, err := captureOutput(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
	}
	return stdout
}

// setDocConfig writes the three sections every document fixture shares: one
// candidate, its policy order, and the builder actor that names it.
func setDocConfig(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`},
		{"config", "set", "policy.order.builder", `["claude/p/m"]`},
		{"config", "set", "actors", `{"builder":{"agent":"plan-executor","candidates":["claude/p/m"]}}`},
	} {
		runDocs(t, args...)
	}
}

// TestContractDocsVersionJSON pins `version --json`: the same string the line
// prints, under a named field.
func TestContractDocsVersionJSON(t *testing.T) {
	docsEnv(t)

	stdout := runDocs(t, "version", "--json")
	assertGolden(t, "version-json", normalize(stdout))

	var doc versionDoc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("version --json: %v", err)
	}
	if doc.Version != buildVersion() {
		t.Errorf("version = %q, want %q", doc.Version, buildVersion())
	}
}

// TestContractDocsConfigJSON pins `config --json`: the actors, the pick per
// role and the candidate rows, from the same sections the human block reads.
func TestContractDocsConfigJSON(t *testing.T) {
	docsEnv(t)
	setDocConfig(t)

	stdout := runDocs(t, "config", "--json")
	assertGolden(t, "config-json", normalize(stdout))

	var doc ConfigView
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("config --json: %v", err)
	}
	if len(doc.Actors) != 1 {
		t.Fatalf("actors = %v, want the builder actor", doc.Actors)
	}
	if len(doc.Candidates) != 1 || doc.Candidates[0].Ref != "claude/p/m" {
		t.Errorf("candidates = %+v, want the one configured candidate", doc.Candidates)
	}
	if doc.Candidates[0].Roles == nil {
		t.Error("candidate roles is nil, want a list")
	}
	found := false
	for _, p := range doc.Pick {
		if p.Role != "builder" {
			continue
		}
		found = true
		// The package's TestMain points HOME at a fresh temp root, which holds
		// none of the builder's shipped agent files, so its only candidate
		// carries the synthesised roles_missing gate. Per readjson.go's rule a
		// pick then has no candidate and the refusal words instead: exactly one
		// of the two fields is ever set.
		if (p.Candidate == "") == (p.Why == "") {
			t.Errorf("builder pick = %+v, want exactly one of candidate or why", p)
		}
	}
	if !found {
		t.Errorf("pick = %+v, want a row for builder", doc.Pick)
	}
}

// TestContractDocsPickRule covers pickCandidate's own rule without a machine
// fixture: the first entry that is on and ungated wins, an off entry is
// skipped, and a gated token yields no pick.
func TestContractDocsPickRule(t *testing.T) {
	actor := roles.Actor{Candidates: []roles.Entry{
		{Candidate: "claude/p/off", Off: true},
		{Candidate: "claude/p/gated"},
		{Candidate: "claude/p/free"},
	}}

	if got := pickCandidate(actor, nil, nil); got != "claude/p/gated" {
		t.Errorf("pick with no gate = %q, want the first on entry", got)
	}
	if got := pickCandidate(actor, nil, []availability.Gate{{Token: "claude/p/gated"}}); got != "claude/p/free" {
		t.Errorf("pick past a gate = %q, want the next ungated entry", got)
	}
	if got := pickCandidate(actor, nil, []availability.Gate{
		{Token: "claude/p/gated"}, {Token: "claude/p/free"},
	}); got != "" {
		t.Errorf("pick when every on entry is gated = %q, want none", got)
	}
}

// TestContractDocsServerListJSON pins `config server list --json`. The fixture
// carries no client key, so every server probes "no key" and the verb dials
// nothing at all.
func TestContractDocsServerListJSON(t *testing.T) {
	docsEnv(t)

	runDocs(t, "config", "set", "servers", `{"alpha":{"url":"https://alpha.example.test","ca":"system"}}`)

	stdout := runDocs(t, "config", "server", "list", "--json")
	assertGolden(t, "config-server-list-json", normalize(stdout))

	var rows []ServerRow
	if err := json.Unmarshal(stdout, &rows); err != nil {
		t.Fatalf("config server list --json: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("rows = %+v, want the one configured server", rows)
	}
	if rows[0].State != "no key" {
		t.Errorf("state = %q, want %q (the fixture carries no client key, so nothing dials)", rows[0].State, "no key")
	}
}

// TestContractDocsSecretListJSON pins `config secret list --json`: the names
// only, sorted, and never a value.
func TestContractDocsSecretListJSON(t *testing.T) {
	docsEnv(t)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	if _, _, err := runWithStdin(t, "ts-key-123\n", "config", "secret", "set", "typesafe"); err != nil {
		t.Fatalf("secret set typesafe: %v", err)
	}
	if _, _, err := runWithStdin(t, string(pem), "config", "secret", "set", "client.key"); err != nil {
		t.Fatalf("secret set client.key: %v", err)
	}

	stdout := runDocs(t, "config", "secret", "list", "--json")
	assertGolden(t, "config-secret-list-json", normalize(stdout))

	var names []string
	if err := json.Unmarshal(stdout, &names); err != nil {
		t.Fatalf("config secret list --json: %v", err)
	}
	if !slices.Equal(names, []string{"client.key", "typesafe"}) {
		t.Errorf("names = %v, want both names", names)
	}
	for _, v := range []string{"ts-key-123", "PRIVATE KEY"} {
		if strings.Contains(string(stdout), v) {
			t.Errorf("secret list carries the value %q:\n%s", v, stdout)
		}
	}
}

// TestContractDocsServerKeyJSON pins `config server key --json`. The key id
// and the enrollment line are generated, so the test replaces them with
// placeholders before the golden compare; the golden pins the shape, and the
// unmarshalled document pins that both fields are filled.
func TestContractDocsServerKeyJSON(t *testing.T) {
	docsEnv(t)

	stdout := runDocs(t, "config", "server", "key", "--json")

	var doc serverKeyDoc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("config server key --json: %v", err)
	}
	if !strings.HasPrefix(doc.ID, "SHA256:") {
		t.Errorf("id = %q, want a SHA256: client id", doc.ID)
	}
	if !strings.HasPrefix(doc.EnrollLine, "ed25519 ") {
		t.Errorf("enroll_line = %q, want an ed25519 line", doc.EnrollLine)
	}

	got := bytes.ReplaceAll(stdout, []byte(doc.ID), []byte("<CLIENT_ID>"))
	got = bytes.ReplaceAll(got, []byte(doc.EnrollLine), []byte("<ENROLL_LINE>"))
	assertGolden(t, "config-server-key-json", normalize(got))
}

// TestContractDocsServeClientsJSON pins `serve clients --json`. The enrolled
// id and public key are generated per run, so the test replaces them with
// placeholders; the timestamp is normalized by the shared helper, and the
// unmarshalled document pins that the client is the one just enrolled.
func TestContractDocsServeClientsJSON(t *testing.T) {
	docsEnv(t)
	if _, err := runServeInit(t); err != nil {
		t.Fatalf("serve init: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	line := remote.MarshalPublic(kp.Public, "alice")
	if _, _, err := captureOutput(t, func() error {
		return run([]string{"serve", "enroll", "--label", "alice", "--key", line})
	}); err != nil {
		t.Fatalf("serve enroll: %v", err)
	}

	stdout := runDocs(t, "serve", "clients", "--json")

	var list []serve.Client
	if err := json.Unmarshal(stdout, &list); err != nil {
		t.Fatalf("serve clients --json: %v", err)
	}
	if len(list) != 1 || list[0].Label != "alice" {
		t.Fatalf("clients = %+v, want the one enrolled client", list)
	}

	got := bytes.ReplaceAll(stdout, []byte(string(list[0].ID)), []byte("<CLIENT_ID>"))
	got = bytes.ReplaceAll(got, []byte(list[0].PubKey), []byte("<PUBKEY>"))
	assertGolden(t, "serve-clients-json", normalize(got))
}

// TestContractDocsServeFingerprintJSON pins `serve fingerprint --json`. The
// fingerprint depends on the certificate `serve init` generated, so the test
// replaces it with a placeholder and separately checks the human line prints
// the same value.
func TestContractDocsServeFingerprintJSON(t *testing.T) {
	docsEnv(t)
	if _, err := runServeInit(t); err != nil {
		t.Fatalf("serve init: %v", err)
	}

	stdout := runDocs(t, "serve", "fingerprint", "--json")

	var doc fingerprintDoc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("serve fingerprint --json: %v", err)
	}
	if doc.Fingerprint == "" {
		t.Fatal("fingerprint is empty")
	}
	human := strings.TrimSpace(string(runDocs(t, "serve", "fingerprint")))
	if human != doc.Fingerprint {
		t.Errorf("human line %q != document %q", human, doc.Fingerprint)
	}

	got := bytes.ReplaceAll(stdout, []byte(doc.Fingerprint), []byte("<FINGERPRINT>"))
	assertGolden(t, "serve-fingerprint-json", normalize(got))
}

// TestContractDocsGateJSON pins `gate --json` for this machine's ledger: one
// row per live entry, with an open-ended gate carrying no `until`.
func TestContractDocsGateJSON(t *testing.T) {
	docsEnv(t)
	for _, args := range [][]string{
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]},{"harness":"claude","provider":"q","model":"n","roles":["builder"]}]`},
		{"gate", "claude/p/m", "--for", "1h", "--reason", "flaky"},
		{"gate", "claude/q/n"},
	} {
		runDocs(t, args...)
	}

	stdout := runDocs(t, "gate", "--json")
	assertGolden(t, "gate-json", normalize(stdout))

	var rows []GateRow
	if err := json.Unmarshal(stdout, &rows); err != nil {
		t.Fatalf("gate --json: %v", err)
	}
	withUntil, without := 0, 0
	for _, r := range rows {
		if r.Until != "" {
			withUntil++
		} else {
			without++
		}
	}
	if withUntil == 0 || without == 0 {
		t.Errorf("rows = %+v, want one with an until and one without", rows)
	}
}

// TestContractDocsGateServeJSON pins `gate --serve --json` on a serve root with
// no gates: the empty ledger is `[]`, never null.
func TestContractDocsGateServeJSON(t *testing.T) {
	docsEnv(t)
	setDocConfig(t)
	if _, err := runServeInit(t); err != nil {
		t.Fatalf("serve init: %v", err)
	}

	stdout := runDocs(t, "gate", "--serve", "--json")
	assertGolden(t, "gate-serve-json", normalize(stdout))

	var rows []GateRow
	if err := json.Unmarshal(stdout, &rows); err != nil {
		t.Fatalf("gate --serve --json: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want the empty serve ledger", rows)
	}
}

// TestContractDocsDoctorShape covers `doctor --json` without a golden: its
// rows are PATH- and machine-dependent, so the contract is the shape and the
// severity vocabulary rather than one machine's output.
func TestContractDocsDoctorShape(t *testing.T) {
	// The pure mapping first: one grouped check, one global check with a fix
	// and a probe failure, and the two counts.
	doc := doctorDocOf(doctor.Report{
		UsableBuilder:  true,
		BuilderRefusal: "every candidate serving builder is gated",
		Checks: []doctor.Check{
			{Group: "claude", Name: "binary", Severity: doctor.SevWarn, Detail: "not on PATH"},
			{Name: "daemon", Severity: doctor.SevFail, Detail: "not running", Fix: "relevo daemon", ProbeFailed: true},
		},
	})
	if doc.Failures != 1 || doc.Warnings != 1 {
		t.Errorf("counts = %d failures, %d warnings, want 1 and 1", doc.Failures, doc.Warnings)
	}
	if doc.BuilderRefusal == "" || !doc.UsableBuilder {
		t.Errorf("verdict inputs not carried: %+v", doc)
	}
	if len(doc.Checks) != 2 {
		t.Fatalf("checks = %+v, want two", doc.Checks)
	}
	if got := doc.Checks[0]; got.Group != "claude" || got.Name != "binary" || got.Severity != "warn" || got.Detail != "not on PATH" {
		t.Errorf("grouped row = %+v", got)
	}
	if got := doc.Checks[1]; got.Group != "" || got.Severity != "FAIL" || got.Fix != "relevo daemon" || !got.ProbeFailed {
		t.Errorf("global row = %+v", got)
	}

	// Then the CLI: a real run writes one JSON object and exits 0 or 1.
	docsEnv(t)
	stdout, _, err := captureOutput(t, func() error { return run([]string{"doctor", "--json"}) })
	if err != nil {
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 1 {
			t.Fatalf("doctor --json: %v, want nil or the failing-checks exit 1", err)
		}
	}

	var runDoc DoctorDoc
	if err := json.Unmarshal(stdout, &runDoc); err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	if len(runDoc.Checks) == 0 {
		t.Error("doctor --json carried no checks")
	}
	for _, c := range runDoc.Checks {
		switch c.Severity {
		case "ok", "warn", "FAIL", "info":
		default:
			t.Errorf("check %q severity = %q, want a known word", c.Name, c.Severity)
		}
		if c.Name == "" || c.Detail == "" {
			t.Errorf("check %+v is missing its name or detail", c)
		}
	}
}

// TestContractDocsErrorCodes is the forced-failure table for the converted
// sites whose failure a fixture can reach: each names its catalog code, its
// exit and the next command the frame prints.
func TestContractDocsErrorCodes(t *testing.T) {
	docsEnv(t)

	cases := []struct {
		name string
		args []string
		code errorCode
		next string
	}{
		// config: export and get misuses, a get miss, a log misuse and a
		// missing revision, and --probe with --json.
		{"config export args", []string{"config", "export", "extra"}, codeUsage, "relevo help"},
		{"config get no path", []string{"config", "get"}, codeUsage, "relevo help"},
		{"config get miss", []string{"config", "get", "candidates.missing"}, codeConfigPathNotSet, "relevo config export"},
		{"config log args", []string{"config", "log", "extra"}, codeUsage, "relevo help"},
		{"config log no revision", []string{"config", "log", "--rev", "99"}, codeRevisionNotFound, "relevo config log"},
		{"config probe json", []string{"config", "--probe", "--json"}, codeUsage, "relevo help"},
		// config server key and config secret set/rm misuses.
		{"config server key args", []string{"config", "server", "key", "extra"}, codeUsage, "relevo help"},
		{"config secret set unknown", []string{"config", "secret", "set", "bogus"}, codeUsage, "relevo help"},
		{"config secret rm unknown", []string{"config", "secret", "rm", "bogus"}, codeUsage, "relevo help"},
		// doctor and version misuses.
		{"doctor unknown flag", []string{"doctor", "--bogus"}, codeUsage, "relevo help"},
		{"version args", []string{"version", "extra"}, codeUsage, "relevo help"},
		// gate: the list misuses, and the interim write-document refusals.
		{"gate misuse", []string{"gate", "one", "two"}, codeUsage, "relevo help"},
		{"gate serve misuse", []string{"gate", "--serve", "one", "two"}, codeUsage, "relevo help"},
		{"gate token json", []string{"gate", "claude/p/m", "--json"}, codeNotAvailable, "relevo gate"},
		{"gate clear json", []string{"gate", "--clear", "p", "--json"}, codeNotAvailable, "relevo gate"},
		{"gate serve token json", []string{"gate", "--serve", "claude/p/m", "--json"}, codeNotAvailable, "relevo gate"},
		{"gate serve clear json", []string{"gate", "--serve", "--clear", "p", "--json"}, codeNotAvailable, "relevo gate"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, _, runErr := captureOutput(t, func() error { return run(c.args) })
			requireCLIError(t, runErr, c.code, c.next)

			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != catalogExit(c.code) {
				t.Errorf("exit = %v, want %d (code %s)", runErr, catalogExit(c.code), c.code)
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
		})
	}

	// The serve verbs refuse an uninitialised root with not_available and the
	// command that fixes it.
	for _, c := range []struct {
		name string
		args []string
	}{
		{"serve clients uninitialised", []string{"serve", "clients", "--state", t.TempDir()}},
		{"serve fingerprint uninitialised", []string{"serve", "fingerprint", "--state", t.TempDir()}},
		{"serve status uninitialised", []string{"serve", "status", "--state", t.TempDir()}},
		{"gate serve uninitialised", []string{"gate", "--serve", "--state", t.TempDir()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, _, runErr := captureOutput(t, func() error { return run(c.args) })
			requireCLIError(t, runErr, codeNotAvailable, "relevo serve init")
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
		})
	}
}

// TestContractDocsInternalSites forces the sites whose runtime build fails:
// with no HOME and no XDG_CONFIG_HOME the config root cannot resolve, so the
// verb's own newRuntime step is the one that fails and reports internal.
func TestContractDocsInternalSites(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	for _, c := range []struct {
		name string
		args []string
	}{
		{"config bare", []string{"config"}},
		{"config server list", []string{"config", "server", "list"}},
		{"doctor", []string{"doctor"}},
		{"mastermind guide", []string{"mastermind", "guide"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, _, runErr := captureOutput(t, func() error { return run(c.args) })
			requireCLIError(t, runErr, codeInternal, "")
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
		})
	}
}

// TestContractDocsErrorFrame pins one failure trace per converted verb in both
// renderings: the human line plus its next command, and the JSON envelope with
// the same code and next.
func TestContractDocsErrorFrame(t *testing.T) {
	docsEnv(t)

	cases := []struct {
		verb string
		args []string
		code errorCode
		next string
	}{
		{"config get", []string{"config", "get", "candidates.missing"}, codeConfigPathNotSet, "relevo config export"},
		{"config log", []string{"config", "log", "--rev", "99"}, codeRevisionNotFound, "relevo config log"},
		{"config secret", []string{"config", "secret", "set", "bogus"}, codeUsage, "relevo help"},
		{"config probe", []string{"config", "--probe", "--json"}, codeUsage, "relevo help"},
		{"doctor", []string{"doctor", "--bogus"}, codeUsage, "relevo help"},
		{"version", []string{"version", "extra"}, codeUsage, "relevo help"},
		{"gate", []string{"gate", "one", "two"}, codeUsage, "relevo help"},
		{"serve clients", []string{"serve", "clients", "--state", t.TempDir()}, codeNotAvailable, "relevo serve init"},
		{"serve fingerprint", []string{"serve", "fingerprint", "--state", t.TempDir()}, codeNotAvailable, "relevo serve init"},
		{"serve status", []string{"serve", "status", "--state", t.TempDir()}, codeNotAvailable, "relevo serve init"},
		{"gate serve", []string{"gate", "--serve", "--state", t.TempDir()}, codeNotAvailable, "relevo serve init"},
	}

	for _, c := range cases {
		t.Run(c.verb, func(t *testing.T) {
			_, _, runErr := captureOutput(t, func() error { return run(c.args) })

			var human bytes.Buffer
			if code := report(&human, runErr, false); code != catalogExit(c.code) {
				t.Errorf("human exit = %d, want %d", code, catalogExit(c.code))
			}
			wantPrefix := "relevo: " + string(c.code) + ": "
			if !strings.HasPrefix(human.String(), wantPrefix) {
				t.Errorf("human line = %q, want the %q prefix", human.String(), wantPrefix)
			}
			if c.next != "" && !strings.Contains(human.String(), "\n  next: "+c.next+"\n") {
				t.Errorf("human line = %q, want the next line %q", human.String(), c.next)
			}

			var jsonBuf bytes.Buffer
			if code := report(&jsonBuf, runErr, true); code != catalogExit(c.code) {
				t.Errorf("json exit = %d, want %d", code, catalogExit(c.code))
			}
			var env errorEnvelope
			if err := json.Unmarshal(jsonBuf.Bytes(), &env); err != nil {
				t.Fatalf("json envelope %q: %v", jsonBuf.String(), err)
			}
			if env.Error.Code != c.code {
				t.Errorf("json code = %q, want %q", env.Error.Code, c.code)
			}
			if env.Error.Next != c.next {
				t.Errorf("json next = %q, want %q", env.Error.Next, c.next)
			}
			if env.Error.Message == "" {
				t.Error("json message is empty")
			}
		})
	}

	// The runtime-build failures render the same way, with the catalog's empty
	// next hint: no HOME and no XDG_CONFIG_HOME is what newRuntime cannot
	// resolve.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, args := range [][]string{
		{"config"},
		{"doctor"},
		{"mastermind", "guide"},
	} {
		_, _, runErr := captureOutput(t, func() error { return run(args) })
		var human bytes.Buffer
		if code := report(&human, runErr, false); code != 1 {
			t.Errorf("%v: human exit = %d, want 1", args, code)
		}
		if !strings.HasPrefix(human.String(), "relevo: internal: ") {
			t.Errorf("%v: human line = %q, want the internal prefix", args, human.String())
		}
		if strings.Contains(human.String(), "\n  next:") {
			t.Errorf("%v: human line = %q, want no next line for internal", args, human.String())
		}
	}
}

// TestContractDocsNoHarnessOrNetwork makes the round's CI rule explicit: the
// server-list fixture carries no client key, so ProbeServers takes its "no key"
// arm and never builds a RemoteClient -- the only dial path these documents
// have. A fixture that somehow stored a key would fail here.
func TestContractDocsNoHarnessOrNetwork(t *testing.T) {
	docsEnv(t)

	runDocs(t, "config", "set", "servers", `{"alpha":{"url":"https://alpha.example.test","ca":"system"}}`)
	stdout := runDocs(t, "config", "server", "list", "--json")
	if !strings.Contains(string(stdout), `"state": "no key"`) {
		t.Errorf("server list = %s, want the no-key probe", stdout)
	}

	// And the secret really is absent: config secret list names nothing.
	names := strings.TrimSpace(string(runDocs(t, "config", "secret", "list", "--json")))
	if names != "[]" {
		t.Errorf("secret list = %s, want an empty list (no client key was written)", names)
	}
}
