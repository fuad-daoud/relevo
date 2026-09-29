package main

import (
	"errors"
	"strings"
	"testing"
)

// TestClientUsageOnNoArgs pins the no-args usage line; CI launches no harness
// binary, so this test must never reach the server subcommands that do (it
// doesn't -- `relevo config server` alone dispatches nothing).
func TestClientUsageOnNoArgs(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"config", "server"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "usage: relevo config server") {
		t.Errorf("expected usage on stderr, got %q", string(stderr))
	}
}

// TestClientAddServerFlagExclusivityExits2 pins that --fingerprint and --ca
// refuse each other before anything is saved or any server is contacted:
// the exclusivity check runs before ValidateEntry and before any network
// call, so this never reaches a harness or the wire either.
func TestClientAddServerFlagExclusivityExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"config", "server", "add", "zen", "https://zen:7777", "--fingerprint", "sha256:aa", "--ca", "system"})
	})

	ce := requireCLIError(t, runErr, codeUsage, "relevo help")
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr before report, got %q", string(stderr))
	}
	if !strings.Contains(ce.message, "mutually exclusive") {
		t.Errorf("expected the mutual-exclusion message, got %q", ce.message)
	}
}
