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

// TestServiceUnitsCarryTheGoBinOnPath pins the daemon's PATH: the shipped unit
// templates name %h/go/bin, where CI installs golangci-lint, so `make lint`
// inside the unit is not silently skipped. Each template must carry exactly one
// Environment=PATH= line and that line must name %h/go/bin.
func TestServiceUnitsCarryTheGoBinOnPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"relevo.service", "relevo-serve.service"} {
		path := filepath.Join("..", "..", "dist", name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		count, found := 0, false
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "Environment=PATH=") {
				count++
				if strings.Contains(line, "%h/go/bin") {
					found = true
				}
			}
		}
		if count != 1 {
			t.Errorf("%s must set exactly one Environment=PATH= line, got %d:\n%s", path, count, body)
		}
		if !found {
			t.Errorf("%s does not name %s on PATH:\n%s", path, "%h/go/bin", body)
		}
	}
}
