package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// commandVerbs collects every verb `run` dispatches, from main.go's `case
// "..."` labels: a label may list several strings (`case "help", "-h",
// "--help"`). Reading and regexing only -- nothing executes, so the cmd/relevo
// CI rule (no harness, no network) holds.
func commandVerbs(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	caseLine := regexp.MustCompile(`(?m)^\s*case\s+(.+):\s*$`)
	quoted := regexp.MustCompile(`"([^"]+)"`)

	verbs := map[string]bool{}
	for _, line := range caseLine.FindAllStringSubmatch(string(raw), -1) {
		for _, m := range quoted.FindAllStringSubmatch(line[1], -1) {
			verbs[m[1]] = true
		}
	}
	if len(verbs) == 0 {
		t.Fatal("no case labels found in main.go")
	}
	return verbs
}

// frontmatterField returns the trimmed value of a frontmatter key, and whether
// the key was present.
func frontmatterField(front, key string) (string, bool) {
	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, key)), true
		}
	}
	return "", false
}

// TestPluginCommandFiles pins the slash-command files in
// claude-plugin/commands: each has a leading frontmatter block with a
// non-empty description and allowed-tools naming exactly its own script
// (`Bash(${CLAUDE_PLUGIN_ROOT}/scripts/<base>.sh:*)`), a guard as the first
// non-empty body line, and exactly one body line running that script.
// show.md and status.md forward the command's arguments ($ARGUMENTS); the
// other five take none. The script itself exists under
// claude-plugin/scripts, is executable, and execs exactly one relevo verb the
// CLI dispatches. The realistic failure is a verb renamed in the CLI and not
// in the plugin. Files only, so no harness, no network and no Claude Code.
func TestPluginCommandFiles(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "claude-plugin", "commands", "*.md"))
	if err != nil {
		t.Fatalf("glob claude-plugin/commands/*.md: %v", err)
	}
	if len(paths) < 2 {
		t.Fatalf("found %d command file(s), want at least 2: %v", len(paths), paths)
	}

	execRelevo := regexp.MustCompile(`(?m)^exec relevo ([a-z-]+)`)
	verbs := commandVerbs(t)
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lines := strings.Split(string(raw), "\n")

		if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
			t.Errorf("%s: must start with a `---` frontmatter block", path)
			continue
		}
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			t.Errorf("%s: frontmatter block is not closed", path)
			continue
		}
		front := strings.Join(lines[1:end], "\n")

		if desc, ok := frontmatterField(front, "description:"); !ok || desc == "" {
			t.Errorf("%s: frontmatter needs a non-empty `description:` (got %q, present=%v)", path, desc, ok)
		}

		// The command runs one script under ${CLAUDE_PLUGIN_ROOT}/scripts, and
		// allowed-tools must name that exact script: a bare Bash(relevo:*) is
		// refused by Claude Code, so the ! block would never run.
		base := strings.TrimSuffix(filepath.Base(path), ".md")
		scriptRef := "${CLAUDE_PLUGIN_ROOT}/scripts/" + base + ".sh"
		wantTools := "Bash(" + scriptRef + ":*)"
		if tools, ok := frontmatterField(front, "allowed-tools:"); !ok || tools != wantTools {
			t.Errorf("%s: allowed-tools must be exactly %q (got %q, present=%v)", path, wantTools, tools, ok)
		}

		// Exactly one body line names the script: the bang-fenced preamble
		// Claude Code runs.
		scriptLine := -1
		scriptText := ""
		for i := end + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if !strings.HasPrefix(trimmed, "${CLAUDE_PLUGIN_ROOT}/scripts/") {
				continue
			}
			if scriptLine >= 0 {
				t.Errorf("%s: more than one line starts with `${CLAUDE_PLUGIN_ROOT}/scripts/`", path)
			}
			scriptLine, scriptText = i, trimmed
		}
		if scriptLine < 0 {
			t.Errorf("%s: no line starts with `${CLAUDE_PLUGIN_ROOT}/scripts/`", path)
			continue
		}

		fields := strings.Fields(scriptText)
		if fields[0] != scriptRef {
			t.Errorf("%s: script line %q does not name %q", path, scriptText, scriptRef)
		}
		if base == "show" || base == "status" {
			if len(fields) != 2 || fields[1] != "$ARGUMENTS" {
				t.Errorf("%s: script line %q must end in $ARGUMENTS", path, scriptText)
			}
		} else if len(fields) != 1 {
			t.Errorf("%s: script line %q must take no argument", path, scriptText)
		}

		// The script exists, is executable, and execs exactly one dispatched
		// verb.
		scriptPath := filepath.Join("..", "..", "claude-plugin", "scripts", base+".sh")
		info, err := os.Stat(scriptPath)
		if err != nil {
			t.Errorf("%s: script %s: %v", path, scriptPath, err)
			continue
		}
		if !info.Mode().IsRegular() {
			t.Errorf("%s: script %s is not a regular file", path, scriptPath)
		}
		if info.Mode().Perm() == 0 {
			t.Errorf("%s: script %s has no permission bit set", path, scriptPath)
		}
		scriptRaw, err := os.ReadFile(scriptPath)
		if err != nil {
			t.Errorf("%s: read %s: %v", path, scriptPath, err)
			continue
		}
		execs := execRelevo.FindAllStringSubmatch(string(scriptRaw), -1)
		if len(execs) != 1 {
			t.Errorf("%s: script %s has %d `^exec relevo <verb>` line(s), want 1", path, scriptPath, len(execs))
		} else if !verbs[execs[0][1]] {
			t.Errorf("%s: script %s execs verb %q, which main.go does not dispatch", path, scriptPath, execs[0][1])
		}

		// The guard is the first non-empty body line, above the script line.
		guardLine, guardText := -1, ""
		for i := end + 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			guardLine, guardText = i, strings.TrimSpace(lines[i])
			break
		}
		if !strings.Contains(guardText, "relevo MCP tools are not available") {
			t.Errorf("%s: the first non-empty body line must be the guard (got %q)", path, guardText)
		} else if guardLine > scriptLine {
			t.Errorf("%s: the guard must precede the script line", path)
		}
	}
}
