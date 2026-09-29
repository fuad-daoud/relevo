package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEditor writes an executable sh script (body after the shebang) into a
// fresh temp dir and returns its path. The plan sanctions this: it is `sh`,
// not a harness.
func writeEditor(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	return path
}

// TestRemovedVerbsNameReplacement is step 3's table test: each of the seven
// verbs P2b folded into `relevo config` exits 2 and names its replacement.
func TestRemovedVerbsNameReplacement(t *testing.T) {
	initRoot(t)

	cases := map[string]string{
		"init":       "relevo config init",
		"candidates": "relevo config",
		"policy":     "relevo config",
		"roles":      "relevo config",
		"agent":      "relevo config agents",
		"client":     "relevo config server",
		"servers":    "relevo config server list",
	}
	for verb, want := range cases {
		_, stderr, err := captureOutput(t, func() error {
			return run([]string{verb})
		})
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%s: exit = %v, want exit code 2", verb, err)
			continue
		}
		if !strings.Contains(string(stderr), "was removed") || !strings.Contains(string(stderr), want) {
			t.Errorf("%s: stderr = %q, want it to name the replacement %q", verb, stderr, want)
		}
	}
}

// TestConfigBareShowsThreeHeadings pins the bare form: one line per block.
func TestConfigBareShowsThreeHeadings(t *testing.T) {
	initRoot(t)

	// An actors section (round 2's shape) is what makes the first block read
	// "actors"; without one it keeps the legacy "roles" heading.
	for _, args := range [][]string{
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m"}]`},
		{"config", "set", "actors", `{"builder":{"agent":"plan-executor","candidates":["m"]}}`},
	} {
		if _, stderr, err := captureOutput(t, func() error { return run(args) }); err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config"})
	})
	if err != nil {
		t.Fatalf("run config: %v (stderr: %s)", err, stderr)
	}
	for _, heading := range []string{"actors", "pick", "candidates"} {
		if !strings.Contains(string(stdout), heading) {
			t.Errorf("bare config output does not name the %q block:\n%s", heading, stdout)
		}
	}
}

// TestRolesInitIsGone pins R6: the verb prints one line and exits 2.
func TestRolesInitIsGone(t *testing.T) {
	initRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "roles-init"})
	})
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("roles-init: exit = %v, want exit code 2", err)
	}
	out := string(stdout) + string(stderr)
	if !strings.Contains(out, "is gone") || !strings.Contains(out, "actors") {
		t.Errorf("roles-init output = %q, want the gone message", out)
	}
}

func TestConfigSetGetUnsetRoundTrip(t *testing.T) {
	initRoot(t)

	// A whole section replaced.
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`})
	}); err != nil {
		t.Fatalf("set candidates: %v (stderr: %s)", err, stderr)
	}
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("get candidates: %v", err)
	}
	if !strings.Contains(string(stdout), `"harness": "claude"`) {
		t.Errorf("get candidates = %q, want the stored body", stdout)
	}

	// A nested key, with the intermediate object created.
	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "policy.order.builder", `["claude/p/m"]`})
	}); err != nil {
		t.Fatalf("set policy.order.builder: %v", err)
	}
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "policy.order.builder"})
	})
	if err != nil {
		t.Fatalf("get policy.order.builder: %v", err)
	}
	if !strings.Contains(string(stdout), "claude/p/m") {
		t.Errorf("get policy.order.builder = %q, want the stored token", stdout)
	}

	// Unset the key, then the section.
	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "unset", "policy.order.builder"})
	}); err != nil {
		t.Fatalf("unset policy.order.builder: %v", err)
	}
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "policy.order.builder"})
	})
	if err == nil || !strings.Contains(err.Error(), "policy.order.builder: not set") {
		t.Errorf("get after unset = %v, want policy.order.builder: not set", err)
	}

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "unset", "candidates"})
	}); err != nil {
		t.Fatalf("unset candidates: %v", err)
	}
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err == nil || !strings.Contains(err.Error(), "candidates: not set") {
		t.Errorf("get after unset = %v, want candidates: not set", err)
	}
}

func TestConfigSetRefusesInvalidPolicy(t *testing.T) {
	initRoot(t)

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "policy", `{"order":{"builder":["claude/p/m"]}}`})
	}); err != nil {
		t.Fatalf("set valid policy: %v", err)
	}

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "policy", `{"max_switches":-1}`})
	}); err == nil {
		t.Fatal("set of an invalid policy: want error, got nil")
	}

	// Nothing changed: the invalid policy was refused, so the refused key is
	// absent and the section is still there. (Round 2's migration may have
	// rewritten the stored policy's shape first, since it carried an order.)
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "policy"})
	})
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if strings.Contains(string(stdout), "max_switches") {
		t.Errorf("get policy = %q, want the refused key absent", stdout)
	}
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "policy.max_switches"})
	})
	if err == nil {
		t.Error("get policy.max_switches after a refused set: want not set, got nil")
	}
}

func TestConfigExportImportRoundTrip(t *testing.T) {
	initRoot(t)

	for _, args := range [][]string{
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`},
		{"config", "set", "policy.order.builder", `["claude/p/m"]`},
	} {
		if _, _, err := captureOutput(t, func() error { return run(args) }); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	first, _, err := captureOutput(t, func() error {
		return run([]string{"config", "export"})
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, stderr, err := runWithStdin(t, string(first), "config", "import", "-"); err != nil {
		t.Fatalf("import: %v (stderr: %s)", err, stderr)
	}
	second, _, err := captureOutput(t, func() error {
		return run([]string{"config", "export"})
	})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("export/import is not identity:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestConfigEditSavesChanges(t *testing.T) {
	initRoot(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", writeEditor(t, `printf '%s' '{"candidates":[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]}' > "$1"`))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "edit"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "saved (config version") {
		t.Errorf("edit stdout = %q, want the saved line", stdout)
	}

	out, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("get after edit: %v", err)
	}
	if !strings.Contains(string(out), `"harness": "claude"`) {
		t.Errorf("get after edit = %q, want the edited value", out)
	}
}

func TestConfigEditEmptyFileAborts(t *testing.T) {
	initRoot(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", writeEditor(t, `: > "$1"`))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "edit"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "aborted; nothing changed") {
		t.Errorf("edit stdout = %q, want the abort line", stdout)
	}

	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err == nil {
		t.Error("get candidates after an aborted edit: want not set, got nil")
	}
}

func TestConfigEditRetriesInvalidJSON(t *testing.T) {
	initRoot(t)
	t.Setenv("VISUAL", "")

	count := filepath.Join(t.TempDir(), "runs")
	body := "n=$(cat '" + count + "' 2>/dev/null || echo 0)\n" +
		"n=$((n + 1))\n" +
		"echo \"$n\" > '" + count + "'\n" +
		"if [ \"$n\" -eq 1 ]; then\n" +
		"  printf '%s' '{not json' > \"$1\"\n" +
		"else\n" +
		"  printf '%s' '{\"candidates\":[{\"harness\":\"claude\",\"provider\":\"p\",\"model\":\"m\",\"roles\":[\"builder\"]}]}' > \"$1\"\n" +
		"fi\n"
	t.Setenv("EDITOR", writeEditor(t, body))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "edit"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stderr), "relevo config edit:") {
		t.Errorf("edit stderr = %q, want the validation error", stderr)
	}
	if !strings.Contains(string(stdout), "saved (config version") {
		t.Errorf("edit stdout = %q, want the saved line", stdout)
	}

	out, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("get after edit: %v", err)
	}
	if !strings.Contains(string(out), `"harness": "claude"`) {
		t.Errorf("get after edit = %q, want the second-run value", out)
	}
}

func TestConfigServerKeyIsStable(t *testing.T) {
	initRoot(t)

	first, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "server", "key"})
	})
	if err != nil {
		t.Fatalf("server key: %v (stderr: %s)", err, stderr)
	}
	second, _, err := captureOutput(t, func() error {
		return run([]string{"config", "server", "key"})
	})
	if err != nil {
		t.Fatalf("second server key: %v", err)
	}

	id1 := strings.SplitN(string(first), "\n", 2)[0]
	id2 := strings.SplitN(string(second), "\n", 2)[0]
	if !strings.HasPrefix(id1, "client id ") {
		t.Fatalf("server key first line = %q, want a client id", id1)
	}
	if id1 != id2 {
		t.Errorf("server key id changed between runs: %q then %q", id1, id2)
	}
}

func TestConfigServerKeyEnrollLinePrintsOnlyTheLine(t *testing.T) {
	initRoot(t)

	plain, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "server", "key"})
	})
	if err != nil {
		t.Fatalf("server key: %v (stderr: %s)", err, stderr)
	}
	line, _, err := captureOutput(t, func() error {
		return run([]string{"config", "server", "key", "--enroll-line"})
	})
	if err != nil {
		t.Fatalf("server key --enroll-line: %v", err)
	}

	if !strings.HasSuffix(string(line), "\n") || strings.Count(string(line), "\n") != 1 {
		t.Errorf("--enroll-line output = %q, want exactly one line ending in a newline", line)
	}
	if !strings.HasPrefix(string(line), "ed25519 ") {
		t.Errorf("--enroll-line output = %q, want an ed25519 line", line)
	}
	want := strings.Split(string(plain), "\n")[1] + "\n"
	if string(line) != want {
		t.Errorf("--enroll-line output = %q, want the plain output's second line %q", line, want)
	}

	t.Run("generates the key when run first", func(t *testing.T) {
		initRoot(t)

		out, _, err := captureOutput(t, func() error {
			return run([]string{"config", "server", "key", "--enroll-line"})
		})
		if err != nil {
			t.Fatalf("server key --enroll-line first: %v", err)
		}
		if !strings.HasPrefix(string(out), "ed25519 ") {
			t.Errorf("--enroll-line first = %q, want an ed25519 line", out)
		}
	})
}

func TestConfigSecretSetListHidesValue(t *testing.T) {
	initRoot(t)

	if _, stderr, err := runWithStdin(t, "  ts-key-123  \n", "config", "secret", "set", "typesafe"); err != nil {
		t.Fatalf("secret set: %v (stderr: %s)", err, stderr)
	}

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "secret", "list"})
	})
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if !strings.Contains(string(stdout), "typesafe") {
		t.Errorf("secret list = %q, want the name", stdout)
	}
	if strings.Contains(string(stdout), "ts-key-123") {
		t.Errorf("secret list = %q, must never print the value", stdout)
	}
}

func TestConfigSecretUnknownNameExits2(t *testing.T) {
	initRoot(t)

	_, stderr, err := runWithStdin(t, "x", "config", "secret", "set", "bogus")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("secret set bogus: exit = %v, want exit code 2", err)
	}
	if !strings.Contains(string(stderr), "typesafe") || !strings.Contains(string(stderr), "client.key") {
		t.Errorf("stderr = %q, want both allowed names", stderr)
	}
}

// revCandidateA and revCandidateB differ in one model name, so a revision
// between them carries exactly one change: candidates[0].model.
const (
	revCandidateA = `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`
	revCandidateB = `[{"harness":"claude","provider":"p","model":"m2","roles":["builder"]}]`
)

func TestConfigLogListsRevisions(t *testing.T) {
	initRoot(t)

	for _, args := range [][]string{
		{"config", "set", "candidates", revCandidateA},
		{"config", "set", "policy.order.builder", `["claude/p/m"]`},
	} {
		if _, stderr, err := captureOutput(t, func() error { return run(args) }); err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "log"})
	})
	if err != nil {
		t.Fatalf("config log: %v (stderr: %s)", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("config log printed %d lines, want 3 (the third is round 2's migration):\n%s", len(lines), stdout)
	}
	if !strings.Contains(lines[0], "#3") || !strings.Contains(lines[0], "cli") ||
		!strings.Contains(lines[0], "config set policy.order.builder") {
		t.Errorf("newest line = %q, want #3, cli and its message", lines[0])
	}
	if !strings.Contains(lines[1], "#2") || !strings.Contains(lines[1], "migration") {
		t.Errorf("middle line = %q, want #2 and the migration source", lines[1])
	}
	if !strings.Contains(lines[2], "#1") || !strings.Contains(lines[2], "config set candidates") {
		t.Errorf("oldest line = %q, want #1 and its message", lines[2])
	}
}

func TestConfigLogRevShowsChanges(t *testing.T) {
	initRoot(t)

	for _, body := range []string{revCandidateA, revCandidateB} {
		if _, stderr, err := captureOutput(t, func() error {
			return run([]string{"config", "set", "candidates", body})
		}); err != nil {
			t.Fatalf("set candidates: %v (stderr: %s)", err, stderr)
		}
	}

	for _, args := range [][]string{
		{"config", "log"},
		{"config", "log", "--rev", "2"},
	} {
		stdout, stderr, err := captureOutput(t, func() error { return run(args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
		t.Logf("relevo %s:\n%s", strings.Join(args, " "), stdout)
	}
}

func TestConfigLogJSON(t *testing.T) {
	initRoot(t)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", revCandidateA})
	}); err != nil {
		t.Fatalf("set: %v (stderr: %s)", err, stderr)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "log", "--json"})
	})
	if err != nil {
		t.Fatalf("log --json: %v (stderr: %s)", err, stderr)
	}
	var list []struct {
		Rev      int64           `json:"rev"`
		Source   string          `json:"source"`
		Message  string          `json:"message"`
		Version  int64           `json:"version"`
		Changes  json.RawMessage `json:"changes"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		t.Fatalf("log --json is not JSON: %v\n%s", err, stdout)
	}
	if len(list) != 2 {
		t.Fatalf("log --json = %+v, want two revisions: the write then round 2's migration", list)
	}
	if list[0].Rev != 2 || list[0].Source != "migration" {
		t.Errorf("newest = %+v, want the migration revision #2", list[0])
	}
	if list[1].Rev != 1 || list[1].Source != "cli" || list[1].Message != "config set candidates" {
		t.Fatalf("oldest = %+v, want one cli revision for config set candidates", list[1])
	}
	if list[0].Snapshot != nil || list[1].Snapshot != nil {
		t.Errorf("list JSON carries a snapshot, want it omitted")
	}

	stdout, stderr, err = captureOutput(t, func() error {
		return run([]string{"config", "log", "--rev", "1", "--json"})
	})
	if err != nil {
		t.Fatalf("log --rev 1 --json: %v (stderr: %s)", err, stderr)
	}
	var one struct {
		Rev      int64           `json:"rev"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(stdout, &one); err != nil {
		t.Fatalf("log --rev --json is not JSON: %v\n%s", err, stdout)
	}
	if one.Rev != 1 || len(one.Snapshot) == 0 {
		t.Errorf("single JSON = %+v, want rev 1 carrying a snapshot", one)
	}
}

func TestConfigRollbackYes(t *testing.T) {
	initRoot(t)

	for _, body := range []string{revCandidateA, revCandidateB} {
		if _, stderr, err := captureOutput(t, func() error {
			return run([]string{"config", "set", "candidates", body})
		}); err != nil {
			t.Fatalf("set candidates: %v (stderr: %s)", err, stderr)
		}
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "rollback", "1", "--yes"})
	})
	if err != nil {
		t.Fatalf("rollback: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "rolled back to #1 as #4") {
		t.Errorf("rollback output = %q, want the rolled-back line", stdout)
	}

	out, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("get after rollback: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(out), `"model": "m"`) || strings.Contains(string(out), "m2") {
		t.Errorf("candidates after rollback = %q, want revision 1's body", out)
	}

	logOut, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "log"})
	})
	if err != nil {
		t.Fatalf("log: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(logOut), "rollback") || !strings.Contains(string(logOut), "rollback to #1") {
		t.Errorf("log =\n%s\nwant the rollback revision present", logOut)
	}
	// The rollback restored the pre-actors config, so the very next runtime
	// migrates it again as a new revision (round 2 R4).
	first := strings.SplitN(strings.TrimSuffix(string(logOut), "\n"), "\n", 2)[0]
	if !strings.Contains(first, "migration") {
		t.Errorf("newest log line = %q, want the re-migration after the rollback", first)
	}
}

func TestConfigRollbackRefusesWithoutTerminal(t *testing.T) {
	initRoot(t)

	for _, body := range []string{revCandidateA, revCandidateB} {
		if _, stderr, err := captureOutput(t, func() error {
			return run([]string{"config", "set", "candidates", body})
		}); err != nil {
			t.Fatalf("set candidates: %v (stderr: %s)", err, stderr)
		}
	}

	// stdinStat is bound to the process's own os.Stdin, so point the seam at a
	// regular file: not a character device, hence not a terminal.
	original := stdinStat
	stdinStat = func() (os.FileInfo, error) {
		f, err := os.CreateTemp(t.TempDir(), "stdin-*")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		defer f.Close()
		return f.Stat()
	}
	t.Cleanup(func() { stdinStat = original })

	_, stderr, err := runWithStdin(t, "", "config", "rollback", "1")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("rollback without a terminal: exit = %v, want exit code 2 (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stderr), "config rollback needs a terminal to confirm; pass --yes") {
		t.Errorf("stderr = %q, want the refusal line", stderr)
	}
}
