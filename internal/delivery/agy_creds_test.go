package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agyCredsNow is the pinned capture time these tests use, so captured_at is
// exact and a round-trip can be compared with ==.
var agyCredsNow = time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)

// agyEnv turns a map into the func(string) string CaptureAgyCreds reads.
func agyEnv(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

// fullAgyEnv is the complete environment agy injects into the commands it runs.
func fullAgyEnv() map[string]string {
	return map[string]string{
		agyConversationEnv: "0f0e0d0c-0b0a-4998-8877-665544332211",
		agyLSAddressEnv:    "localhost:42139",
		agyCSRFTokenEnv:    "the-csrf-token",
		agyAgentAPIExeEnv:  "/usr/local/bin/agy",
	}
}

// countingSecrets wraps a SecretStore and counts its puts, so "identical values
// do not write" is provable without a file's mtime.
type countingSecrets struct {
	SecretStore
	puts int
}

func (c *countingSecrets) SecretPut(name string, value []byte, now time.Time) error {
	c.puts++
	return c.SecretStore.SecretPut(name, value, now)
}

// TestValidConversationID pins relevo's own copy of the agy conversation id
// rule. mastermind.Detect carries the same pattern for the environment it reads.
func TestValidConversationID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id   string
		want bool
	}{
		{"0f0e0d0c-0b0a-4998-8877-665544332211", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"0f0e0d0c-0b0a-4998-8877-665544332211a", false},
		{"0f0e0d0c-0b0a-4998-8877-66554433221", false},
		{"0F0E0D0C-0B0A-4998-8877-665544332211", false},
		{"0f0e0d0c0b0a49988877665544332211", false},
		{"not-a-uuid", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := validConversationID(tc.id); got != tc.want {
			t.Errorf("validConversationID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestAgyEnvPresent pins the cheap check main.go makes before it opens the
// machine database: a complete environment is present, and every incomplete
// one is not.
func TestAgyEnvPresent(t *testing.T) {
	t.Parallel()

	if !AgyEnvPresent(agyEnv(fullAgyEnv())) {
		t.Error("AgyEnvPresent(full env) = false, want true")
	}
	for _, missing := range []string{agyConversationEnv, agyLSAddressEnv, agyCSRFTokenEnv} {
		env := fullAgyEnv()
		delete(env, missing)
		if AgyEnvPresent(agyEnv(env)) {
			t.Errorf("AgyEnvPresent without %s = true, want false", missing)
		}
	}
	if AgyEnvPresent(nil) {
		t.Error("AgyEnvPresent(nil) = true, want false")
	}
}

// TestAgyCaptureWritesRoundTrip is the happy path: a full environment stores
// the credentials secret, and ReadAgyCreds gives back exactly what was
// captured.
func TestAgyCaptureWritesRoundTrip(t *testing.T) {
	t.Parallel()

	secrets := testSecrets(t)
	env := fullAgyEnv()

	wrote, err := CaptureAgyCreds(agyEnv(env), secrets, agyCredsNow)
	if err != nil {
		t.Fatalf("CaptureAgyCreds: %v", err)
	}
	if !wrote {
		t.Fatal("a full environment must write")
	}

	if _, ok, err := secrets.SecretGet(agySecretName(env[agyConversationEnv])); err != nil || !ok {
		t.Fatalf("SecretGet = (_, %v, %v), want the captured secret", ok, err)
	}

	got, err := ReadAgyCreds(secrets, env[agyConversationEnv])
	if err != nil {
		t.Fatalf("ReadAgyCreds: %v", err)
	}
	want := AgyCreds{
		ConversationID: env[agyConversationEnv],
		LSAddress:      env[agyLSAddressEnv],
		CSRFToken:      env[agyCSRFTokenEnv],
		AgentAPIExe:    env[agyAgentAPIExeEnv],
		CapturedAt:     agyCredsNow,
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestAgyCaptureWritesNothingWithoutAllThree pins the gate: a missing or
// invalid conversation id, address or token writes nothing at all.
func TestAgyCaptureWritesNothingWithoutAllThree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{name: "no conversation id", mutate: func(m map[string]string) { delete(m, agyConversationEnv) }},
		{name: "a malformed conversation id", mutate: func(m map[string]string) { m[agyConversationEnv] = "not-a-uuid" }},
		{name: "no address", mutate: func(m map[string]string) { delete(m, agyLSAddressEnv) }},
		{name: "a non-loopback address", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "10.0.0.5:42139" }},
		{name: "an address with no port", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "localhost" }},
		{name: "an address with a non-numeric port", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "localhost:http" }},
		{name: "a loopback-looking remote host", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "127.0.0.1.evil.com:42139" }},
		{name: "no token", mutate: func(m map[string]string) { delete(m, agyCSRFTokenEnv) }},
		{name: "an empty token", mutate: func(m map[string]string) { m[agyCSRFTokenEnv] = "" }},
		{name: "a token with whitespace", mutate: func(m map[string]string) { m[agyCSRFTokenEnv] = "two words" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := fullAgyEnv()
			tc.mutate(env)
			secrets := testSecrets(t)

			wrote, err := CaptureAgyCreds(agyEnv(env), secrets, agyCredsNow)
			if err != nil {
				t.Fatalf("CaptureAgyCreds: %v", err)
			}
			if wrote {
				t.Error("wrote, want nothing written")
			}
			names, err := secrets.SecretNames()
			if err != nil {
				t.Fatalf("SecretNames: %v", err)
			}
			if len(names) != 0 {
				t.Errorf("stored %v; want nothing", names)
			}
		})
	}
}

// TestAgyCaptureSkipsIdenticalAndRewritesChangedToken pins the two halves of
// the write rule: identical values are not rewritten, and a changed token is.
func TestAgyCaptureSkipsIdenticalAndRewritesChangedToken(t *testing.T) {
	t.Parallel()

	secrets := &countingSecrets{SecretStore: testSecrets(t)}
	env := fullAgyEnv()
	conv := env[agyConversationEnv]

	if wrote, err := CaptureAgyCreds(agyEnv(env), secrets, agyCredsNow); err != nil || !wrote {
		t.Fatalf("first capture: wrote=%v err=%v", wrote, err)
	}
	if secrets.puts != 1 {
		t.Fatalf("first capture put %d times, want 1", secrets.puts)
	}

	wrote, err := CaptureAgyCreds(agyEnv(env), secrets, agyCredsNow)
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if wrote {
		t.Error("identical values must not write")
	}
	if secrets.puts != 1 {
		t.Errorf("an identical capture put %d times, want it not to write again", secrets.puts)
	}

	env[agyCSRFTokenEnv] = "a-rotated-token"
	wrote, err = CaptureAgyCreds(agyEnv(env), secrets, agyCredsNow)
	if err != nil {
		t.Fatalf("third capture: %v", err)
	}
	if !wrote {
		t.Error("a changed token must write")
	}
	got, err := ReadAgyCreds(secrets, conv)
	if err != nil {
		t.Fatalf("ReadAgyCreds: %v", err)
	}
	if got.CSRFToken != "a-rotated-token" {
		t.Errorf("csrf_token = %q, want the rotated token", got.CSRFToken)
	}
}

// TestAgyCapturePrunesOldSiblings pins the prune: a write drops credentials
// whose capture is older than a week, and keeps last night's.
func TestAgyCapturePrunesOldSiblings(t *testing.T) {
	t.Parallel()

	secrets := testSecrets(t)
	env := fullAgyEnv()

	oldConv := "11111111-2222-3333-4444-555555555555"
	youngConv := "99999999-8888-7777-6666-555555555555"
	writeSibling := func(conv string, capturedAt time.Time) {
		t.Helper()
		raw, err := json.Marshal(AgyCreds{
			ConversationID: conv,
			LSAddress:      "localhost:1",
			CSRFToken:      "t",
			CapturedAt:     capturedAt,
		})
		if err != nil {
			t.Fatalf("marshal sibling: %v", err)
		}
		if err := secrets.SecretPut(agySecretName(conv), raw, capturedAt); err != nil {
			t.Fatalf("write sibling: %v", err)
		}
	}

	writeSibling(oldConv, agyCredsNow.Add(-10*24*time.Hour))
	writeSibling(youngConv, agyCredsNow.Add(-24*time.Hour))

	if wrote, err := CaptureAgyCreds(agyEnv(env), secrets, agyCredsNow); err != nil || !wrote {
		t.Fatalf("capture: wrote=%v err=%v", wrote, err)
	}

	if _, ok, err := secrets.SecretGet(agySecretName(oldConv)); err != nil || ok {
		t.Errorf("10-day-old sibling = (_, %v, %v), want it pruned", ok, err)
	}
	if _, ok, err := secrets.SecretGet(agySecretName(youngConv)); err != nil || !ok {
		t.Errorf("1-day-old sibling = (_, %v, %v), want it kept", ok, err)
	}
	if _, ok, err := secrets.SecretGet(agySecretName(env[agyConversationEnv])); err != nil || !ok {
		t.Errorf("the capture itself is gone: ok=%v err=%v", ok, err)
	}
}

// TestAgyCredsNeverPrintsTheToken pins the redaction: every fmt spelling of an
// AgyCreds, including %#v, renders the token as <redacted>.
func TestAgyCredsNeverPrintsTheToken(t *testing.T) {
	t.Parallel()

	creds := AgyCreds{
		ConversationID: "0f0e0d0c-0b0a-4998-8877-665544332211",
		LSAddress:      "localhost:42139",
		CSRFToken:      "the-csrf-token",
		AgentAPIExe:    "/usr/local/bin/agy",
		CapturedAt:     agyCredsNow,
	}

	rendered := fmt.Sprintf("%v %+v %#v", creds, creds, creds)
	if strings.Contains(rendered, creds.CSRFToken) {
		t.Errorf("a rendered AgyCreds contains the token: %s", rendered)
	}
	if !strings.Contains(rendered, redactedToken) {
		t.Errorf("a rendered AgyCreds does not say %s: %s", redactedToken, rendered)
	}
}

// TestReadAgyCredsMissingSecretIsNotExist pins the error a caller keys on: a
// conversation with no stored credentials reports os.ErrNotExist.
func TestReadAgyCredsMissingSecretIsNotExist(t *testing.T) {
	t.Parallel()

	_, err := ReadAgyCreds(testSecrets(t), "0f0e0d0c-0b0a-4998-8877-665544332211")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadAgyCreds on a missing secret = %v, want os.ErrNotExist", err)
	}
}

// TestAgyCredsImportAdoptsFiles is the pre-database import: a present
// masterminds/.agy/<conversation>.json is put to the secret agy/<conversation>
// and removed, and the emptied .agy directory goes with it.
func TestAgyCredsImportAdoptsFiles(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "masterminds", ".agy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	conv := "0f0e0d0c-0b0a-4998-8877-665544332211"
	raw, err := json.Marshal(AgyCreds{
		ConversationID: conv,
		LSAddress:      "localhost:42139",
		CSRFToken:      "the-csrf-token",
		CapturedAt:     agyCredsNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, conv+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}

	secrets := testSecrets(t)
	if err := ImportAgyCreds(secrets, dir); err != nil {
		t.Fatalf("ImportAgyCreds: %v", err)
	}

	got, err := ReadAgyCreds(secrets, conv)
	if err != nil {
		t.Fatalf("ReadAgyCreds after the import: %v", err)
	}
	if got.CSRFToken != "the-csrf-token" || !got.CapturedAt.Equal(agyCredsNow) {
		t.Errorf("imported credentials = %+v", got)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Errorf("the imported file is still there: %v", serr)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Errorf("the emptied .agy directory is still there: %v", serr)
	}
}
