package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serveUnitWantsTheDaemon reports whether a unit body pulls the daemon in and
// starts after it: a Wants=relevo.service line and an After= line naming
// relevo.service. It is pure, so the check can be shown to fail without them.
func serveUnitWantsTheDaemon(body string) bool {
	wants, after := false, false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "Wants=relevo.service" {
			wants = true
		}
		if rest, ok := strings.CutPrefix(line, "After="); ok && hasArg(strings.Fields(rest), "relevo.service") {
			after = true
		}
	}
	return wants && after
}

// TestServeUnitWantsTheDaemon pins the serve unit's dependency on the daemon:
// `relevo serve` and its admin verbs dial the local owner like any client, so
// dist/relevo-serve.service must want relevo.service and order itself after it.
// The unit's own auto-start stays the fallback, and the MasterMind deploys the
// unit on the server host.
func TestServeUnitWantsTheDaemon(t *testing.T) {
	path := filepath.Join("..", "..", "dist", "relevo-serve.service")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !serveUnitWantsTheDaemon(string(body)) {
		t.Errorf("%s must set Wants=relevo.service and name relevo.service in After=:\n%s", path, body)
	}

	// The check must fail without either half, or it pins nothing.
	if serveUnitWantsTheDaemon("[Unit]\nDescription=x\nAfter=default.target\n") {
		t.Error("serveUnitWantsTheDaemon passed without the daemon dependency")
	}
	if serveUnitWantsTheDaemon("[Unit]\nWants=relevo.service\nAfter=default.target\n") {
		t.Error("serveUnitWantsTheDaemon passed without the daemon ordering")
	}
}
