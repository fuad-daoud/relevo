package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
)

func captureOutput(t *testing.T, fn func() error) (stdout []byte, stderr []byte, err error) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr

	rOut, wOut, errOut := os.Pipe()
	if errOut != nil {
		t.Fatalf("pipe: %v", errOut)
	}
	rErr, wErr, errPipe := os.Pipe()
	if errPipe != nil {
		t.Fatalf("pipe: %v", errPipe)
	}

	os.Stdout = wOut
	os.Stderr = wErr

	runErr := fn()

	wOut.Close()
	wErr.Close()

	os.Stdout = origStdout
	os.Stderr = origStderr

	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)

	return outBytes, errBytes, runErr
}

func TestAgentInstallDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"config", "agents", "--kind", "claude", "--dry-run"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}

	wantLines := []string{
		"would write  ~/.claude/agents/plan-executor.md",
		"would write  ~/.claude/agents/researcher.md",
		"would write  ~/.claude/agents/reviewer.md",
		"would write  ~/.claude/agents/architect.md",
	}
	gotLines := strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("expected %d lines, got %d (%v)", len(wantLines), len(gotLines), gotLines)
	}
	for i, want := range wantLines {
		if gotLines[i] != want {
			t.Errorf("line %d: want %q, got %q", i, want, gotLines[i])
		}
	}

	if _, err := os.Stat(filepath.Join(home, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected .claude to not exist, got err: %v", err)
	}
}

func TestAgentInstallWritesThenKeeps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"config", "agents", "--kind", "agy"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}

	roles := []string{"plan-executor", "researcher", "reviewer", "architect"}
	var wantLines []string
	for _, role := range roles {
		wantLines = append(wantLines, "wrote  ~/.gemini/config/agents/"+role+".md")
	}
	gotLines := strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("expected %d lines, got %d (%v)", len(wantLines), len(gotLines), gotLines)
	}
	for i, want := range wantLines {
		if gotLines[i] != want {
			t.Errorf("line %d: want %q, got %q", i, want, gotLines[i])
		}
	}

	for _, role := range roles {
		p := filepath.Join(home, ".gemini/config/agents", role+".md")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", p, err)
		}
		expectedDoc, err := harness.AgentDoc(role, "agy")
		if err != nil {
			t.Fatalf("AgentDoc(%s, agy): %v", role, err)
		}
		if !bytes.Equal(data, expectedDoc) {
			t.Errorf("file %s content mismatch", p)
		}
	}

	// Run again: four kept (identical) ~/... lines and nil error
	stdout2, stderr2, runErr2 := captureOutput(t, func() error {
		return run([]string{"config", "agents", "--kind", "agy"})
	})

	if runErr2 != nil {
		t.Fatalf("second run unexpected error: %v", runErr2)
	}
	if len(stderr2) != 0 {
		t.Errorf("second run expected empty stderr, got %q", string(stderr2))
	}

	var wantLines2 []string
	for _, role := range roles {
		wantLines2 = append(wantLines2, "kept (identical)  ~/.gemini/config/agents/"+role+".md")
	}
	gotLines2 := strings.Split(strings.TrimSuffix(string(stdout2), "\n"), "\n")
	if len(gotLines2) != len(wantLines2) {
		t.Fatalf("second run expected %d lines, got %d (%v)", len(wantLines2), len(gotLines2), gotLines2)
	}
	for i, want := range wantLines2 {
		if gotLines2[i] != want {
			t.Errorf("second run line %d: want %q, got %q", i, want, gotLines2[i])
		}
	}
}

func TestAgentInstallUnknownKindExits2(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"config", "agents", "--kind", "unknown-kind"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected empty stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "unknown-kind") {
		t.Errorf("expected stderr to contain 'unknown-kind', got %q", string(stderr))
	}
}

func TestAgentInstallUnknownRoleExits2(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"config", "agents", "--kind", "claude", "--agent", "nope"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected empty stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "nope") || !strings.Contains(string(stderr), "plan-executor") {
		t.Errorf("expected stderr to contain 'nope' and 'plan-executor', got %q", string(stderr))
	}
}

func TestAgentInstallWriteFailureExits1(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	agentsDir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	researcherFile := filepath.Join(agentsDir, "researcher.md")
	if err := os.WriteFile(researcherFile, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(agentsDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		os.Chmod(agentsDir, 0o755)
	})

	stdout, _, runErr := captureOutput(t, func() error {
		return run([]string{"config", "agents", "--kind", "claude"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 1 {
		t.Fatalf("expected exit code 1, got %v", runErr)
	}

	outStr := string(stdout)
	lines := strings.Split(strings.TrimSuffix(outStr, "\n"), "\n")
	if len(lines) != 4 {
		t.Errorf("expected loop not to stop at first error (4 lines), got %d (%v)", len(lines), lines)
	}
	hasPlanExecutorError := false
	for _, line := range lines {
		if strings.HasPrefix(line, "error  ~/.claude/agents/plan-executor.md:") {
			hasPlanExecutorError = true
		}
	}
	if !hasPlanExecutorError {
		t.Errorf("expected stdout to have line starting with 'error  ~/.claude/agents/plan-executor.md:', got %q", outStr)
	}
	wantResearcher := "kept (differs; --force to overwrite)  ~/.claude/agents/researcher.md"
	hasResearcherLine := false
	for _, line := range lines {
		if line == wantResearcher {
			hasResearcherLine = true
		}
	}
	if !hasResearcherLine {
		t.Errorf("expected stdout to have line %q, got %q", wantResearcher, outStr)
	}
}
