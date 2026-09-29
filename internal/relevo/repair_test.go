package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRepairPlanNamesEverything pins the repair plan's text (#132 part 2): the
// builder gets nothing but this file, so it must name the failed round, the
// gate command, the original plan and the gate log, and carry every tail line
// the closing tick captured.
func TestRepairPlanNamesEverything(t *testing.T) {
	t.Parallel()

	b := store.Binding{Name: "webshop", Gate: "make check"}
	tail := []string{"ok  \tgithub.com/example/pkg\t0.01s", "FAIL\tgithub.com/example/pkg2\t0.02s", "exit status 2"}
	planPath := "/state/webshop/001-plan.md"
	gateLogPath := "/state/webshop/001-gate.log"

	got := repairPlan(b, 1, planPath, gateLogPath, tail)

	for _, want := range []string{
		"# Repair round 2 for webshop: round 1's gate failed",
		"Round 1's acceptance check (`make check`) did NOT pass",
		"Fix ONLY",
		planPath,
		gateLogPath,
		"last 3 lines",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("repair plan missing %q:\n%s", want, got)
		}
	}
	for _, line := range tail {
		if !strings.Contains(got, line) {
			t.Errorf("repair plan missing tail line %q:\n%s", line, got)
		}
	}

	// The tail sits inside a fenced block, so a line that looks like markdown
	// cannot restyle the plan around it.
	if strings.Count(got, "```") != 2 {
		t.Errorf("repair plan must fence the tail exactly once:\n%s", got)
	}
}

// TestGateSignatureIgnoresNoise pins the stall bound's comparison (#132 part
// 2): two gate logs that differ only in timestamps, durations, a 7-digit
// integer, a 10-hex sha, a /tmp path and line order must hash equal; a real
// word difference must not; and an unreadable log is "".
func TestGateSignatureIgnoresNoise(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a := filepath.Join(dir, "a.log")
	other := filepath.Join(dir, "b.log")
	word := filepath.Join(dir, "c.log")

	bodyA := strings.Join([]string{
		"2026-09-21T14:29:00Z ok  \tgithub.com/example/pkg\t0.01s",
		"14:29:01 running tests 1234567",
		"sha deadbeef01",
		"output at /tmp/relevo-a/001-gate.log",
		"FAIL github.com/example/pkg2 0.02s",
	}, "\n")
	bodyOther := strings.Join([]string{
		"14:29:59 running tests 7654321",
		"2026-09-21T14:31:07Z ok  \tgithub.com/example/pkg\t9.99s",
		"FAIL github.com/example/pkg2 3.5s",
		"output at /tmp/relevo-b/002-gate.log",
		"sha 0f1e2d3c4b",
	}, "\n")
	bodyWord := strings.Join([]string{
		"2026-09-21T14:29:00Z ok  \tgithub.com/example/pkg\t0.01s",
		"14:29:01 running tests 1234567",
		"sha deadbeef01",
		"output at /tmp/relevo-a/001-gate.log",
		"PASS github.com/example/pkg2 0.02s",
	}, "\n")

	for path, body := range map[string]string{a: bodyA, other: bodyOther, word: bodyWord} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sigA, sigOther := gateSignature(os.ReadFile, a), gateSignature(os.ReadFile, other)
	if sigA == "" {
		t.Fatalf("gateSignature(%s) = \"\", want a hash", a)
	}
	if sigA != sigOther {
		t.Errorf("logs differing only in noise hashed differently: %q vs %q", sigA, sigOther)
	}
	if sigA == gateSignature(os.ReadFile, word) {
		t.Errorf("logs differing in a real word hashed equal: %q", sigA)
	}
	if got := gateSignature(os.ReadFile, filepath.Join(dir, "missing.log")); got != "" {
		t.Errorf("gateSignature(missing) = %q, want \"\"", got)
	}
}
