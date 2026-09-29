package agentsrc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// agyTools is the tool allowlist agy needs; the scratch worktree, not the tool
// list, is what keeps a reader off the binding's tree.
const agyTools = "  - view_file\n" +
	"  - grep_search\n" +
	"  - find_by_name\n" +
	"  - list_dir\n" +
	"  - run_command\n" +
	"  - write_to_file\n" +
	"  - replace_file_content\n" +
	"  - multi_replace_file_content\n"

// Render returns the bytes of the native file for one harness kind. It
// validates s first and never returns partial bytes.
func Render(s Source, kind string) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if !kindRendered(s, kind) {
		return nil, fmt.Errorf("agent %s: kind %s is not in its kinds: %w", s.Name, kind, ErrBadSource)
	}
	desc := yamlScalar(s.Description)
	switch kind {
	case "claude":
		return []byte("---\n" +
			"name: " + s.Name + "\n" +
			"description: " + desc + "\n" +
			"---\n\n" + s.Body), nil
	case "opencode":
		return []byte("---\n" +
			"name: " + s.Name + "\n" +
			"description: " + desc + "\n" +
			"mode: all\n" +
			"---\n\n" + s.Body), nil
	case "agy":
		return []byte("---\n" +
			"name: " + s.Name + "\n" +
			"description: " + desc + "\n" +
			"mainAgent: true\n" +
			"subagent: false\n" +
			"model: inherit\n" +
			"commandExecutionPolicy: auto\n" +
			"tools:\n" +
			agyTools +
			"---\n\n" + s.Body), nil
	case "codex":
		return renderCodex(s), nil
	}
	// Unreachable: kindRendered accepts only a known kind.
	return nil, fmt.Errorf("agent %s: kind %s is not in its kinds: %w", s.Name, kind, ErrBadSource)
}

// kindRendered reports whether kind is in the source's rendered kinds.
func kindRendered(s Source, kind string) bool {
	for _, k := range RenderedKinds(s) {
		if k == kind {
			return true
		}
	}
	return false
}

func renderCodex(s Source) []byte {
	var b strings.Builder
	b.WriteString("# relevo agent " + s.Name + " for codex, rendered by relevo from its source.\n")
	b.WriteString("# Edit the source (relevo config agents), not this file: relevo rewrites it.\n")
	b.WriteString("\ndeveloper_instructions = '''\n")
	b.WriteString(s.Body)
	b.WriteString("'''")
	if len(s.Requires) == 0 {
		b.WriteString("\n")
		return []byte(b.String())
	}
	for _, r := range s.Requires {
		b.WriteString("\n\n[agents." + r + "]\n")
		b.WriteString("config_file = \"" + r + ".config.toml\"\n")
		b.WriteString("description = \"relevo agent " + r + "\"")
	}
	b.WriteString("\n")
	return []byte(b.String())
}

// yamlScalar returns s unchanged when it is a safe YAML plain scalar, and
// otherwise a double-quoted JSON string, which YAML reads correctly.
func yamlScalar(s string) string {
	if isPlainScalar(s) {
		return s
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

func isPlainScalar(s string) bool {
	if s == "" {
		return false
	}
	if s != strings.TrimSpace(s) {
		return false
	}
	if strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", []rune(s)[0]) {
		return false
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") {
		return false
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "null", "~":
		return false
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return false
	}
	return true
}
