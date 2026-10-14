package doctor

import "testing"

// TestDoctorReportsEditedAgentFiles pins the row a native agent's kept hand
// edit can only be seen in: the files are named, with the command that puts
// the document's copy back.
func TestDoctorReportsEditedAgentFiles(t *testing.T) {
	t.Parallel()
	in := AgentFilesCheckInput{
		Carried: 2,
		Edited: []EditedFile{
			{Path: "/home/u/.claude/agents/my-native.md", Hash: "abc"},
			{Path: "/home/u/.codex/my-native.config.toml", Hash: "def"},
		},
	}
	in.Rendered = []EditedFile{{Path: "/home/u/.claude/agents/my-executor.md"}}
	check := AgentFilesCheck(in)
	if check.Name != "agent-files" {
		t.Errorf("Name = %q, want agent-files", check.Name)
	}
	if check.Severity != SevWarn {
		t.Errorf("Severity = %v, want a warning", check.Severity)
	}
	// Both halves are named: the embedded native definitions the applier kept
	// and the rendered one the manifest no longer vouches for.
	for _, path := range []string{
		"/home/u/.claude/agents/my-native.md",
		"/home/u/.codex/my-native.config.toml",
		"/home/u/.claude/agents/my-executor.md",
	} {
		if !contains(check.Detail, path) {
			t.Errorf("Detail %q does not name %s", check.Detail, path)
		}
	}
	if check.Fix != "relevo config agents --force" {
		t.Errorf("Fix = %q, want the force line", check.Fix)
	}
}

// TestAgentFilesCheckIsQuietWhenNothingDrifted pins the other two cells: no
// files at all, and files none of which has been edited.
func TestAgentFilesCheckIsQuietWhenNothingDrifted(t *testing.T) {
	t.Parallel()
	if check := AgentFilesCheck(AgentFilesCheckInput{}); check.Severity != SevOK {
		t.Errorf("an empty input = %v, want ok", check.Severity)
	}
	carried := AgentFilesCheck(AgentFilesCheckInput{Carried: 3})
	if carried.Severity != SevOK || carried.Fix != "" {
		t.Errorf("three unedited files = %v (fix %q), want ok with no fix", carried.Severity, carried.Fix)
	}
}
