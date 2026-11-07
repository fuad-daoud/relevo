package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedActorOverTwoCandidates writes a resolving document: two candidates and a
// builder actor naming both by name and by harness/provider/model token. The
// actor rides on a shipped agent, so no agents section is needed.
func seedActorOverTwoCandidates(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m"},{"harness":"agy","provider":"q","model":"n"}]`},
		{"config", "set", "actors", `{"builder":{"agent":"plan-executor","candidates":["m","agy/q/n"]}}`},
	} {
		if _, stderr, err := captureOutput(t, func() error { return run(args) }); err != nil {
			t.Fatalf("seed %v: %v (stderr: %s)", args, err, stderr)
		}
	}
}

// configWriteCounts is the version and revision count a refused write must
// leave exactly as it was: the internal/config analogue is
// assertWriteLeftTheStoreAlone.
func configWriteCounts(t *testing.T) (int64, int) {
	t.Helper()
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	version, err := rt.Config.Version()
	if err != nil {
		t.Fatalf("Config.Version: %v", err)
	}
	rows, err := rt.Config.Log(0)
	if err != nil {
		t.Fatalf("Config.Log: %v", err)
	}
	return version, len(rows)
}

func TestConfigSetCandidatesDroppingAnActorCandidateRefuses(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)
	beforeVersion, beforeRevs := configWriteCounts(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m"}]`})
	})
	ce := requireCLIError(t, err, codeConflict, "")
	for _, want := range []string{"builder", "agy/q/n", "--force"} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message = %q, want it to name %q", ce.message, want)
		}
	}

	afterVersion, afterRevs := configWriteCounts(t)
	if afterVersion != beforeVersion || afterRevs != beforeRevs {
		t.Errorf("counts = (v%d, %d revisions), want (v%d, %d): the refused write stored something",
			afterVersion, afterRevs, beforeVersion, beforeRevs)
	}
}

func TestConfigSetCandidatesDroppingWithForceSucceeds(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m"}]`, "--force"})
	}); err != nil {
		t.Fatalf("config set --force: %v (stderr: %s)", err, stderr)
	}

	body, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("config get candidates: %v (stderr: %s)", err, stderr)
	}
	if strings.Contains(string(body), "agy/q/n") {
		t.Errorf("stored candidates = %s, want the dropped token gone", body)
	}
}

func TestConfigImportDroppingAnActorCandidateRefuses(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)
	beforeVersion, beforeRevs := configWriteCounts(t)

	doc := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(doc, []byte(`{"candidates":[{"harness":"claude","provider":"p","model":"m"}]}`), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "import", doc})
	})
	ce := requireCLIError(t, err, codeConflict, "")
	for _, want := range []string{"builder", "agy/q/n", "--force"} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message = %q, want it to name %q", ce.message, want)
		}
	}

	afterVersion, afterRevs := configWriteCounts(t)
	if afterVersion != beforeVersion || afterRevs != beforeRevs {
		t.Errorf("counts = (v%d, %d revisions), want (v%d, %d): the refused import stored something",
			afterVersion, afterRevs, beforeVersion, beforeRevs)
	}
}

func TestConfigImportDroppingWithForceSucceeds(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)

	doc := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(doc, []byte(`{"candidates":[{"harness":"claude","provider":"p","model":"m"}]}`), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "import", doc, "--force"})
	}); err != nil {
		t.Fatalf("config import --force: %v (stderr: %s)", err, stderr)
	}

	body, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("config get candidates: %v (stderr: %s)", err, stderr)
	}
	if strings.Contains(string(body), "agy/q/n") {
		t.Errorf("stored candidates = %s, want the dropped token gone", body)
	}
}

func TestConfigUnsetCandidatesDroppingAnActorCandidateRefuses(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)
	beforeVersion, beforeRevs := configWriteCounts(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "unset", "candidates"})
	})
	ce := requireCLIError(t, err, codeConflict, "")
	for _, want := range []string{"builder", "m", "--force"} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message = %q, want it to name %q", ce.message, want)
		}
	}

	afterVersion, afterRevs := configWriteCounts(t)
	if afterVersion != beforeVersion || afterRevs != beforeRevs {
		t.Errorf("counts = (v%d, %d revisions), want (v%d, %d): the refused write stored something",
			afterVersion, afterRevs, beforeVersion, beforeRevs)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "unset", "candidates", "--force"})
	}); err != nil {
		t.Fatalf("config unset --force: %v (stderr: %s)", err, stderr)
	}
	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	}); err == nil {
		t.Error("candidates section still readable after unset --force")
	}
}

// invalidCandidatesWithOverlongName is the seeded two candidates plus one
// entry whose 25-character name breaks the 24-character limit, so the write
// carries an unparseable list that still names everything the actor holds.
const invalidCandidatesWithOverlongName = `[{"harness":"claude","provider":"p","model":"m"},{"harness":"agy","provider":"q","model":"n"},{"name":"inkling-2-small-free-high","harness":"opencode","provider":"opencode","model":"inkling-2-small-free#high"}]`

func TestConfigSetCandidatesWithAnUnparseableNameRefusesAsInvalid(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)
	beforeVersion, beforeRevs := configWriteCounts(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", invalidCandidatesWithOverlongName})
	})
	ce := requireCLIError(t, err, codeConfigInvalid, "")
	if !strings.Contains(ce.message, "inkling-2-small-free-high") {
		t.Errorf("message = %q, want it to name the candidate", ce.message)
	}
	if strings.Contains(ce.message, "pass --force to drop it") {
		t.Errorf("message = %q, want no drop wording for an unparseable list", ce.message)
	}

	afterVersion, afterRevs := configWriteCounts(t)
	if afterVersion != beforeVersion || afterRevs != beforeRevs {
		t.Errorf("counts = (v%d, %d revisions), want (v%d, %d): the refused write stored something",
			afterVersion, afterRevs, beforeVersion, beforeRevs)
	}
}

func TestConfigSetCandidatesWithAnUnparseableNameForceStillRefusesAsInvalid(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)
	beforeVersion, beforeRevs := configWriteCounts(t)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", invalidCandidatesWithOverlongName, "--force"})
	})
	ce := requireCLIError(t, err, codeConfigInvalid, "")
	if !strings.Contains(ce.message, "inkling-2-small-free-high") {
		t.Errorf("message = %q, want it to name the candidate", ce.message)
	}

	afterVersion, afterRevs := configWriteCounts(t)
	if afterVersion != beforeVersion || afterRevs != beforeRevs {
		t.Errorf("counts = (v%d, %d revisions), want (v%d, %d): the refused write stored something",
			afterVersion, afterRevs, beforeVersion, beforeRevs)
	}
}

func TestConfigImportWithAnUnparseableNameRefusesAsInvalid(t *testing.T) {
	initRoot(t)
	seedActorOverTwoCandidates(t)
	beforeVersion, beforeRevs := configWriteCounts(t)

	doc := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(doc, []byte(`{"candidates":`+invalidCandidatesWithOverlongName+`}`), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "import", doc})
	})
	ce := requireCLIError(t, err, codeConfigInvalid, "")
	if !strings.Contains(ce.message, "inkling-2-small-free-high") {
		t.Errorf("message = %q, want it to name the candidate", ce.message)
	}
	if strings.Contains(ce.message, "pass --force to drop it") {
		t.Errorf("message = %q, want no drop wording for an unparseable list", ce.message)
	}

	afterVersion, afterRevs := configWriteCounts(t)
	if afterVersion != beforeVersion || afterRevs != beforeRevs {
		t.Errorf("counts = (v%d, %d revisions), want (v%d, %d): the refused import stored something",
			afterVersion, afterRevs, beforeVersion, beforeRevs)
	}
}

// TestConfigSetActorsNamingAnUnknownCandidateIsAllowed pins that provisioning
// order is legitimate: an actor may be written before its candidate exists, so
// the reverse direction is never guarded.
func TestConfigSetActorsNamingAnUnknownCandidateIsAllowed(t *testing.T) {
	initRoot(t)

	for _, args := range [][]string{
		{"config", "set", "actors", `{"builder":{"agent":"plan-executor","candidates":["later"]}}`},
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m"},{"harness":"claude","provider":"p","model":"later"}]`},
	} {
		if _, stderr, err := captureOutput(t, func() error { return run(args) }); err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
	}
}
