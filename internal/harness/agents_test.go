package harness

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestAgentDocResolvesEveryTableRole(t *testing.T) {
	for _, h := range All() {
		for _, r := range h.Roles {
			doc, err := AgentDoc(r.Name, h.Kind)
			if err != nil {
				t.Errorf("AgentDoc(%q, %q): %v", r.Name, h.Kind, err)
				continue
			}
			if len(doc) == 0 {
				t.Errorf("AgentDoc(%q, %q) returned empty bytes", r.Name, h.Kind)
			}
		}
	}
}

func TestAgentDocRejectsBadArguments(t *testing.T) {
	tests := []struct{ role, kind string }{
		{"plan-executor", "nosuch"},
		{"nosuch", "claude"},
		{"", "claude"},
		{"plan-executor", ""},
		{"", ""},
		{"../../etc/passwd", "claude"},
		{"../plan-executor", "claude"},
		{"plan-executor/../plan-executor", "claude"},
		{"plan-executor.claude", "claude"},
	}
	for _, tc := range tests {
		if _, err := AgentDoc(tc.role, tc.kind); !errors.Is(err, ErrNoAgentDoc) {
			t.Errorf("AgentDoc(%q, %q) error = %v, want ErrNoAgentDoc", tc.role, tc.kind, err)
		}
	}
}

func TestPlanExecutorDefinitionsForbidWritingSubAgents(t *testing.T) {
	const oneWriter = "Exactly one agent writes to this working tree, and it is you."
	for _, kind := range []string{"claude", "opencode", "agy", "codex"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		if !strings.Contains(string(doc), oneWriter) {
			t.Errorf("plan-executor.%s.md must contain %q", kind, oneWriter)
		}
	}
}

func TestPlanExecutorGateRunsInForegroundOnEveryKind(t *testing.T) {
	const waits = "runs in the foreground: you wait for it to finish and read its exit code"
	const neverReportsPassed = "never report it as passed before it has exited"
	for _, kind := range []string{"claude", "opencode", "agy", "codex"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		for _, want := range []string{waits, neverReportsPassed} {
			if !strings.Contains(string(doc), want) {
				t.Errorf("plan-executor.%s.md must contain %q", kind, want)
			}
		}
	}
}

func TestPlanExecutorVerifiesAHalfDoneTree(t *testing.T) {
	const fragment = "do not redo the step: verify what is there against the step's text"
	for _, kind := range []string{"claude", "opencode", "agy", "codex"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		if !strings.Contains(string(doc), fragment) {
			t.Errorf("plan-executor.%s.md must contain %q", kind, fragment)
		}
	}
}

func TestOpencodeDefinitionsDeclareMode(t *testing.T) {
	cases := []struct {
		role string
		mode string
	}{
		{"architect", "primary"},
		{"plan-executor", "all"},
		{"researcher", "subagent"},
	}
	for _, tc := range cases {
		doc, err := AgentDoc(tc.role, "opencode")
		if err != nil {
			t.Fatalf("AgentDoc(%s, opencode): %v", tc.role, err)
		}
		fm := frontmatter(t, doc)
		if !strings.Contains(fm, "\nmode: "+tc.mode+"\n") {
			t.Errorf("opencode %s must declare mode: %s", tc.role, tc.mode)
		}
	}
}

func TestAgentDocCodexIsToml(t *testing.T) {
	const marker = "developer_instructions = '''\n"
	for _, role := range []string{"plan-executor", "researcher", "reviewer", "architect"} {
		doc, err := AgentDoc(role, "codex")
		if err != nil {
			t.Fatalf("AgentDoc(%s, codex): %v", role, err)
		}
		s := string(doc)
		idx := strings.Index(s, marker)
		if idx < 0 {
			t.Fatalf("%s: doc does not contain %q", role, marker)
		}
		after := s[idx+len(marker):]
		if strings.Count(after, "'''") != 1 {
			t.Errorf("%s: text after the marker must contain exactly one closing '''; got %d", role, strings.Count(after, "'''"))
		}
		switch role {
		case "plan-executor":
			checkCodexPlanExecutor(t, s)
		case "researcher":
			checkCodexResearcher(t, s)
		}
	}
}

func checkCodexPlanExecutor(t *testing.T, s string) {
	t.Helper()
	for _, want := range []string{"[agents.researcher]", `config_file = "researcher.config.toml"`, "spawn_agent"} {
		if !strings.Contains(s, want) {
			t.Errorf("plan-executor doc must contain %q", want)
		}
	}
	for _, banned := range []string{"subagent_type", "Agent tool"} {
		if strings.Contains(s, banned) {
			t.Errorf("plan-executor doc must not contain %q", banned)
		}
	}
	if di, tbl := strings.Index(s, "developer_instructions"), strings.Index(s, "[agents.researcher]"); di < 0 || tbl < 0 || di >= tbl {
		t.Errorf("plan-executor doc: developer_instructions (%d) must come before [agents.researcher] (%d)", di, tbl)
	}
}

func checkCodexResearcher(t *testing.T, s string) {
	t.Helper()
	for _, want := range []string{`model = "gpt-5.6-luna"`, `model_reasoning_effort = "medium"`} {
		if !strings.Contains(s, want) {
			t.Errorf("researcher doc must contain %q", want)
		}
	}
}

func TestAgyDefinitionsFrontmatter(t *testing.T) {
	forbidden := regexp.MustCompile(`(?m)^\s*-\s*(write_to_file|replace_file_content|create_file|delete_file|notebook_edit|invoke_subagent|send_command_input|multi_replace_file_content|sed_file|manage_subagents|define_subagent)\s*$`)
	for _, role := range []string{"plan-executor", "researcher", "reviewer"} {
		doc, err := AgentDoc(role, "agy")
		if err != nil {
			t.Fatalf("AgentDoc(%s, agy): %v", role, err)
		}
		fm := frontmatter(t, doc)
		if !strings.Contains(fm, "\nname: "+role+"\n") {
			t.Errorf("%s: frontmatter must carry name: %s", role, role)
		}
		if !strings.Contains(fm, "\nmodel: inherit\n") {
			t.Errorf("%s: frontmatter must pin model: inherit", role)
		}
		if role == "plan-executor" {
			checkAgyPlanExecutor(t, fm)
			continue
		}
		if !strings.Contains(fm, "\ntools:\n") {
			t.Errorf("%s must carry a tools allowlist", role)
		}
		if m := forbidden.FindString(fm); m != "" {
			t.Errorf("%s allowlist contains a writing tool: %q", role, strings.TrimSpace(m))
		}
	}
}

func checkAgyPlanExecutor(t *testing.T, fm string) {
	t.Helper()
	if !strings.Contains(fm, "\nsubagent: false\n") {
		t.Errorf("plan-executor must be subagent: false")
	}
	if !strings.Contains(fm, "\ntools:\n") {
		t.Errorf("plan-executor must carry a tools allowlist")
	}
	for _, name := range []string{"write_to_file", "replace_file_content", "run_command"} {
		m := regexp.MustCompile(`(?m)^\s*-\s*` + name + `\s*$`)
		if !m.MatchString(fm) {
			t.Errorf("plan-executor allowlist must include %s; without it the builder cannot build", name)
		}
	}
	// An idle root agent on agy is an exit relevo treats as a failed builder,
	// so plan-executor never dispatches a sub-agent there.
	for _, name := range []string{"invoke_subagent", "manage_subagents"} {
		m := regexp.MustCompile(`(?m)^\s*-\s*` + name + `\s*$`)
		if m.MatchString(fm) {
			t.Errorf("plan-executor allowlist must not include %s", name)
		}
	}
}

func frontmatter(t *testing.T, doc []byte) string {
	t.Helper()
	s := string(doc)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatal("definition does not open with a --- fence")
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatal("definition frontmatter never closes")
	}
	return "\n" + rest[:end] + "\n"
}

// Tool names agy 1.2.1 resolves for a definition in ~/.gemini/config/agents;
// an unknown name stops the agent from starting. Extend only from a live agy
// run, never from the stream-json init event's tools array.
var agyKnownTools = map[string]bool{
	"view_file":                  true,
	"grep_search":                true,
	"find_by_name":               true,
	"list_dir":                   true,
	"run_command":                true,
	"write_to_file":              true,
	"replace_file_content":       true,
	"multi_replace_file_content": true,
	"invoke_subagent":            true,
	"manage_subagents":           true,
	"define_subagent":            true,
	"send_message":               true,
	"manage_task":                true,
	"read_url_content":           true,
	"search_web":                 true,
	"schedule":                   true,
	"generate_image":             true,
	"ask_question":               true,
}

func agyAllowlist(fm string) []string {
	itemRe := regexp.MustCompile(`^\s*-\s*([a-z_]+)\s*$`)
	var names []string
	inList := false
	for _, line := range strings.Split(fm, "\n") {
		if strings.TrimSpace(line) == "tools:" {
			inList = true
			continue
		}
		if !inList {
			continue
		}
		m := itemRe.FindStringSubmatch(line)
		if m == nil {
			inList = false
			continue
		}
		names = append(names, m[1])
	}
	return names
}

func TestAgyAllowlistsResolve(t *testing.T) {
	for _, role := range []string{"plan-executor", "researcher", "reviewer"} {
		doc, err := AgentDoc(role, "agy")
		if err != nil {
			t.Fatalf("AgentDoc(%s, agy): %v", role, err)
		}
		fm := frontmatter(t, doc)
		names := agyAllowlist(fm)
		if len(names) == 0 {
			t.Errorf("%s: no tools: list names collected from frontmatter", role)
		}
		for _, name := range names {
			if !agyKnownTools[name] {
				t.Errorf("%s allowlist names %q, which agy 1.2.1 does not resolve; the agent would not start", role, name)
			}
		}
	}
}
