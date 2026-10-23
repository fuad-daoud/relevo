package relevo

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

const skillTestDir = "/repo"

func designerRow() roles.Row {
	return roles.Row{
		Shape:       ptr("writer"),
		Candidates:  []string{testClaudeRef, testOpencodeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "designer"}, "opencode": {Agent: "designer"}},
		Skills:      []string{"design-runbooks"},
	}
}

func skillRuntime(t *testing.T, onDisk ...string) Runtime {
	t.Helper()
	rt := newRuntime(t)
	set := candidateSet(t, rolesGateCandidatesJSON)
	rt.Candidates = set
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef, testOpencodeRef}},
		"designer": designerRow(),
	})
	have := map[string]bool{}
	for _, p := range onDisk {
		have[p] = true
	}
	rt.SkillExists = func(p string) bool { return have[p] }
	return rt
}

func skillGatesFor(rt Runtime, dir string) []availability.Gate {
	return withSkillGates(rt, nil, dir)
}

func TestSkillGateEmptyDirAddsNone(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t)
	if g := skillGatesFor(rt, ""); len(g) != 0 {
		t.Errorf("gates = %+v, want none without a directory", g)
	}
}

func TestSkillGateAbsentNote(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.claude/skills/design-runbooks/SKILL.md")
	home, _ := os.UserHomeDir()
	gates := skillGatesFor(rt, skillTestDir)
	if len(gates) != 1 {
		t.Fatalf("gates = %+v, want one (opencode lacks the skill)", gates)
	}
	g := gates[0]
	want := "skill design-runbooks not found for designer on opencode: looked at /repo/.agents/skills/design-runbooks/SKILL.md, " +
		home + "/.agents/skills/design-runbooks/SKILL.md"
	if g.Kind != availability.SkillMissing || g.Role != "designer" || g.Token != testOpencodeRef || g.Note != want {
		t.Errorf("gate = %+v, want note %q", g, want)
	}
}

func TestSkillGateUserPathSatisfies(t *testing.T) {
	t.Parallel()
	home, _ := os.UserHomeDir()
	rt := skillRuntime(t, home+"/.claude/skills/design-runbooks/SKILL.md", home+"/.agents/skills/design-runbooks/SKILL.md")
	if g := skillGatesFor(rt, skillTestDir); len(g) != 0 {
		t.Errorf("gates = %+v, want none when the user path has the skill", g)
	}
}

func TestSkillGateIgnoresActorWithoutSkills(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t)
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, skillGatesFor(rt, skillTestDir), "", "builder")
	if err != nil || res.Token() != testClaudeRef {
		t.Fatalf("builder pick = %v, %v; want %s", res.Token(), err, testClaudeRef)
	}
}

func TestSkillGateWorktreeUntrackedAndTracked(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.claude/skills/design-runbooks/SKILL.md", "/repo/.agents/skills/design-runbooks/SKILL.md")
	untracked := withTrackedSkillGates(rt, nil, skillTestDir, func(string) bool { return false })
	if len(untracked) != 2 {
		t.Fatalf("gates = %+v, want one per candidate", untracked)
	}
	for _, g := range untracked {
		if !strings.Contains(g.Note, "untracked in /repo; commit it or install it per user") {
			t.Errorf("note = %q, want the untracked hint", g.Note)
		}
	}
	tracked := withTrackedSkillGates(rt, nil, skillTestDir, func(string) bool { return true })
	if len(tracked) != 0 {
		t.Errorf("gates = %+v, want none when the base tracks the skill", tracked)
	}
}

func TestSkillPickSkipsAndRefuses(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"designer": designerRow(),
	})
	gates := skillGatesFor(rt, skillTestDir)
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, gates, "", "designer")
	if err != nil || res.Token() != testOpencodeRef {
		t.Fatalf("pick = %q, %v; want %s", res.Token(), err, testOpencodeRef)
	}
	line := ExplainResolution("designer", res)
	if !strings.Contains(line, "skipped ") || !strings.Contains(line, "(skill design-runbooks not found for designer on claude: looked at /repo/.claude/skills/design-runbooks/SKILL.md") {
		t.Errorf("pick line = %q", line)
	}
	t.Log(line)

	if _, err := resolveRole(rt.RoleRegistry(), rt.Candidates, gates, testClaudeRef, "designer"); err == nil || !strings.Contains(err.Error(), "skill design-runbooks not found") {
		t.Errorf("explicit pick err = %v, want the skill note", err)
	}

	none := skillRuntime(t)
	_, err = resolveRole(none.RoleRegistry(), none.Candidates, skillGatesFor(none, skillTestDir), "", "designer")
	if !errors.Is(err, ErrAllGated) || !strings.Contains(err.Error(), "skill design-runbooks not found") {
		t.Errorf("all-skipped err = %v, want ErrAllGated with the reason", err)
	}
}
