package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
)

func museCandidate() candidate.Candidate {
	return candidate.Candidate{Harness: "muse", Provider: "meta", Model: "muse-spark-1.3"}
}

func installMuseDefinition(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config/muse/agents/plan-executor.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHeadlessLaunchMuseReadsDefinitionWithoutFrontmatter(t *testing.T) {
	installMuseDefinition(t, "---\nname: plan-executor\n---\n\nYou \"build\".\nSecond line.\n")
	role, _ := harness.RoleByName("builder")
	argv, err := HeadlessLaunch(museCandidate(), role, harness.TierYolo, time.Hour, "go", "/w", "/s", "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := `run.developer_prompt="You \"build\".\nSecond line.\n"`
	if !contains(argv, want) {
		t.Errorf("argv %q lacks %q", argv, want)
	}
}

func TestHeadlessLaunchMuseKeepsFrontmatterlessBody(t *testing.T) {
	installMuseDefinition(t, "<!-- c -->\n\nbody\n")
	role, _ := harness.RoleByName("builder")
	argv, err := HeadlessLaunch(museCandidate(), role, harness.TierYolo, time.Hour, "go", "/w", "/s", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := `run.developer_prompt="<!-- c -->\n\nbody\n"`; !contains(argv, want) {
		t.Errorf("argv %q lacks %q", argv, want)
	}
}

func TestHeadlessLaunchMuseMissingDefinitionNamesPathAndFix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	role, _ := harness.RoleByName("builder")
	_, err := HeadlessLaunch(museCandidate(), role, harness.TierYolo, time.Hour, "go", "/w", "/s", "", "")
	if err == nil {
		t.Fatal("want an error for a missing definition")
	}
	for _, want := range []string{".config/muse/agents/plan-executor.md", "relevo config agents --force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestHeadlessLaunchEmptyPromptSkipsDefinitionRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	role, _ := harness.RoleByName("builder")
	if _, err := HeadlessLaunch(museCandidate(), role, harness.TierYolo, 0, "", "/w", "/s", "", ""); err != nil {
		t.Errorf("validation call with an empty prompt failed: %v", err)
	}
}

func contains(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}
