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

// TestServiceUnitsCarryTheGoBinOnPath pins the daemon's PATH: CI installs
// golangci-lint to %h/go/bin, so `make lint` inside a unit never misses it.
// The daemon unit is a template whose PATH the renderer assembles, so its
// line must say @PATH@ and the renderer's fixed-list pin carries the go bin
// instead; the serve unit carries that fixed list itself.
func TestServiceUnitsCarryTheGoBinOnPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"relevo.service", "relevo-serve.service"} {
		path := filepath.Join("..", "..", "dist", name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		count, line := 0, ""
		for _, l := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(l, "Environment=PATH=") {
				count++
				line = l
			}
		}
		if count != 1 {
			t.Errorf("%s must set exactly one Environment=PATH= line, got %d:\n%s", path, count, body)
		}
		if name == "relevo.service" {
			if line != "Environment=PATH=@PATH@" {
				t.Errorf("%s must leave the PATH to the renderer, got %q", path, line)
			}
			continue
		}
		if !strings.Contains(line, "%h/go/bin") {
			t.Errorf("%s does not name %s on PATH:\n%s", path, "%h/go/bin", body)
		}
	}
}
