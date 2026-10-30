package resolve

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

const skillTestDir = "/repo"

// skillCandidatesJSON is a claude candidate that can serve both roles and an
// opencode one that can serve builder only; the file's rows, not these roles,
// decide who serves what in file mode.
const skillCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`

// fileRegistry builds the registry from in-memory rows over set.
func fileRegistry(t *testing.T, set *candidate.Set, rows map[string]roles.Row) *roles.Registry {
	t.Helper()
	reg, err := roles.Build(&roles.File{Rows: rows}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return reg
}

// skillDesignerRow is the custom writer row the skill tests add: a claude
// definition and an opencode one, and the skill the gates check for.
func skillDesignerRow() roles.Row {
	shape := "writer"
	return roles.Row{
		Shape:       &shape,
		Candidates:  []string{testClaudeRef, testOpencodeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "designer"}, "opencode": {Agent: "designer"}},
		Skills:      []string{"design-runbooks"},
	}
}

// skillDeps builds the Deps a skill gate test reads: the set, the registry
// with a builder row and a designer row carrying the skill, and the on-disk
// skill files the probe sees.
func skillDeps(t *testing.T, onDisk ...string) Deps {
	t.Helper()
	set := candidateSet(t, skillCandidatesJSON)
	reg := fileRegistry(t, set, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef, testOpencodeRef}},
		"designer": skillDesignerRow(),
	})
	have := map[string]bool{}
	for _, p := range onDisk {
		have[p] = true
	}
	return Deps{Deps: availability.Deps{
		Candidates:   set,
		Now:          func() time.Time { return baseTime },
		RoleRegistry: func() *roles.Registry { return reg },
	},
		SkillExists: func(p string) bool { return have[p] },
	}
}

func skillGatesFor(d Deps, dir string) []availability.Gate {
	return WithSkillGates(d, nil, dir, nil)
}

func TestSkillGateEmptyDirAddsNone(t *testing.T) {
	t.Parallel()
	d := skillDeps(t)
	if g := skillGatesFor(d, ""); len(g) != 0 {
		t.Errorf("gates = %+v, want none without a directory", g)
	}
}

func TestSkillGateAbsentNote(t *testing.T) {
	t.Parallel()
	d := skillDeps(t, "/repo/.claude/skills/design-runbooks/SKILL.md")
	home, _ := os.UserHomeDir()
	gates := skillGatesFor(d, skillTestDir)
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
	d := skillDeps(t, home+"/.claude/skills/design-runbooks/SKILL.md", home+"/.agents/skills/design-runbooks/SKILL.md")
	if g := skillGatesFor(d, skillTestDir); len(g) != 0 {
		t.Errorf("gates = %+v, want none when the user path has the skill", g)
	}
}

func TestSkillGateIgnoresActorWithoutSkills(t *testing.T) {
	t.Parallel()
	d := skillDeps(t)
	res, err := ResolveRole(d.RoleRegistry(), d.Candidates, skillGatesFor(d, skillTestDir), "", "builder")
	if err != nil || res.Token() != testClaudeRef {
		t.Fatalf("builder pick = %v, %v; want %s", res.Token(), err, testClaudeRef)
	}
}

func TestSkillGateWorktreeUntrackedAndTracked(t *testing.T) {
	t.Parallel()
	d := skillDeps(t, "/repo/.claude/skills/design-runbooks/SKILL.md", "/repo/.agents/skills/design-runbooks/SKILL.md")
	untracked := WithSkillGates(d, nil, skillTestDir, func(string) bool { return false })
	if len(untracked) != 2 {
		t.Fatalf("gates = %+v, want one per candidate", untracked)
	}
	for _, g := range untracked {
		if !strings.Contains(g.Note, "untracked in /repo; commit it or install it per user") {
			t.Errorf("note = %q, want the untracked hint", g.Note)
		}
	}
	tracked := WithSkillGates(d, nil, skillTestDir, func(string) bool { return true })
	if len(tracked) != 0 {
		t.Errorf("gates = %+v, want none when the base tracks the skill", tracked)
	}
}

func TestSkillPickSkipsAndRefuses(t *testing.T) {
	t.Parallel()
	d := skillDeps(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	d.RoleRegistry = func() *roles.Registry {
		return fileRegistry(t, d.Candidates, map[string]roles.Row{
			"designer": skillDesignerRow(),
		})
	}
	gates := skillGatesFor(d, skillTestDir)
	res, err := ResolveRole(d.RoleRegistry(), d.Candidates, gates, "", "designer")
	if err != nil || res.Token() != testOpencodeRef {
		t.Fatalf("pick = %q, %v; want %s", res.Token(), err, testOpencodeRef)
	}
	line := ExplainResolution("designer", res)
	if !strings.Contains(line, "skipped ") || !strings.Contains(line, "(skill design-runbooks not found for designer on claude: looked at /repo/.claude/skills/design-runbooks/SKILL.md") {
		t.Errorf("pick line = %q", line)
	}
	t.Log(line)

	if _, err := ResolveRole(d.RoleRegistry(), d.Candidates, gates, testClaudeRef, "designer"); !errors.Is(err, ErrCandidateCannotRun) || !strings.Contains(err.Error(), "skill design-runbooks not found") {
		t.Errorf("explicit pick err = %v, want ErrCandidateCannotRun with the skill note", err)
	}

	none := skillDeps(t)
	_, err = ResolveRole(none.RoleRegistry(), none.Candidates, skillGatesFor(none, skillTestDir), "", "designer")
	if !errors.Is(err, ErrAllGated) || !strings.Contains(err.Error(), "skill design-runbooks not found") {
		t.Errorf("all-skipped err = %v, want ErrAllGated with the reason", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "install the missing skill where relevo doctor says") || strings.Contains(msg, "--candidate") || strings.Contains(msg, "gate --clear") {
		t.Errorf("all-skipped err = %q, want only the install hint", msg)
	}
}
