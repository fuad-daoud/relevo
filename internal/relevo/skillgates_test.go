package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
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
	return withSkillGates(rt, nil, dir, nil)
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
	res, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, skillGatesFor(rt, skillTestDir), "", "builder")
	if err != nil || res.Token() != testClaudeRef {
		t.Fatalf("builder pick = %v, %v; want %s", res.Token(), err, testClaudeRef)
	}
}

func TestSkillGateWorktreeUntrackedAndTracked(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.claude/skills/design-runbooks/SKILL.md", "/repo/.agents/skills/design-runbooks/SKILL.md")
	untracked := withSkillGates(rt, nil, skillTestDir, func(string) bool { return false })
	if len(untracked) != 2 {
		t.Fatalf("gates = %+v, want one per candidate", untracked)
	}
	for _, g := range untracked {
		if !strings.Contains(g.Note, "untracked in /repo; commit it or install it per user") {
			t.Errorf("note = %q, want the untracked hint", g.Note)
		}
	}
	tracked := withSkillGates(rt, nil, skillTestDir, func(string) bool { return true })
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
	res, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, gates, "", "designer")
	if err != nil || res.Token() != testOpencodeRef {
		t.Fatalf("pick = %q, %v; want %s", res.Token(), err, testOpencodeRef)
	}
	line := resolve.ExplainResolution("designer", res)
	if !strings.Contains(line, "skipped ") || !strings.Contains(line, "(skill design-runbooks not found for designer on claude: looked at /repo/.claude/skills/design-runbooks/SKILL.md") {
		t.Errorf("pick line = %q", line)
	}
	t.Log(line)

	if _, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, gates, testClaudeRef, "designer"); !errors.Is(err, resolve.ErrCandidateCannotRun) || !strings.Contains(err.Error(), "skill design-runbooks not found") {
		t.Errorf("explicit pick err = %v, want resolve.ErrCandidateCannotRun with the skill note", err)
	}

	none := skillRuntime(t)
	_, err = resolve.ResolveRole(none.RoleRegistry(), none.Candidates, skillGatesFor(none, skillTestDir), "", "designer")
	if !errors.Is(err, resolve.ErrAllGated) || !strings.Contains(err.Error(), "skill design-runbooks not found") {
		t.Errorf("all-skipped err = %v, want resolve.ErrAllGated with the reason", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "install the missing skill where relevo doctor says") || strings.Contains(msg, "--candidate") || strings.Contains(msg, "gate --clear") {
		t.Errorf("all-skipped err = %q, want only the install hint", msg)
	}
}

const skillBlindNote = "skill design-runbooks not found for designer on claude"

func designerBindOpts(candidate string) BindOptions {
	return BindOptions{Name: "site", Role: "designer", Candidate: candidate, MasterMindID: testMasterMindName, CWD: skillTestDir}
}

func TestBindDesignerSkipsSkillBlindCandidate(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	b, res, err := BindResolved(context.Background(), rt, designerBindOpts(""))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.BuilderCandidate != testOpencodeRef || res.Token() != testOpencodeRef {
		t.Errorf("builder = %q, pick = %q; want %s", b.BuilderCandidate, res.Token(), testOpencodeRef)
	}
	line := resolve.ExplainResolution("designer", res)
	if !strings.Contains(line, "skipped ") || !strings.Contains(line, "("+skillBlindNote+": looked at") {
		t.Errorf("pick line = %q", line)
	}
	t.Log(line)
}

func TestBindDesignerRefusesSkillBlindCandidate(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	_, err := Bind(context.Background(), rt, designerBindOpts(testClaudeRef))
	if err == nil || !strings.Contains(err.Error(), skillBlindNote) {
		t.Errorf("err = %v, want the skill note", err)
	}
}

func TestBindBuilderPicksFirstRankedWithoutSkills(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	opts := designerBindOpts("")
	opts.Role = ""
	b, err := Bind(context.Background(), rt, opts)
	if err != nil || b.BuilderCandidate != testClaudeRef {
		t.Errorf("builder = %q, %v; want %s", b.BuilderCandidate, err, testClaudeRef)
	}
}

func TestResumeDesignerRepicksOverSkillGates(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	seed, err := Bind(context.Background(), rt, designerBindOpts(testOpencodeRef))
	if err != nil {
		t.Fatalf("seed Bind: %v", err)
	}
	seed.BuilderCandidate = ""
	if err := rt.Store.Save(seed); err != nil {
		t.Fatal(err)
	}
	opts := designerBindOpts("")
	opts.Resume, opts.Rebind = true, true
	b, res, err := BindResolved(context.Background(), rt, opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if b.BuilderCandidate != testOpencodeRef {
		t.Errorf("builder = %q, want %s", b.BuilderCandidate, testOpencodeRef)
	}
	if line := resolve.ExplainResolution("designer", res); !strings.Contains(line, "("+skillBlindNote+": looked at") {
		t.Errorf("pick line = %q", line)
	}
}

func TestRepickForRoundSkipsSkillBlindCandidate(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	b := inflightBinding("site", "gone/test/m", func(b *store.Binding) {
		b.Role, b.CWD, b.Builder = "designer", skillTestDir, store.Endpoint{Mode: store.ModeHeadless}
	})
	next, res, _, err := repickForRound(rt, b, false, false)
	if err != nil || res == nil || next.BuilderCandidate != testOpencodeRef {
		t.Fatalf("repick = %q, %v; want %s", next.BuilderCandidate, err, testOpencodeRef)
	}
}

func TestSwitchAndSendGatesCarrySkillGates(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	b := inflightBinding("site", testOpencodeRef, func(b *store.Binding) { b.Role, b.CWD = "designer", skillTestDir })
	found := false
	for _, g := range switchGates(rt, b) {
		found = found || (g.Kind == availability.SkillMissing && g.Token == testClaudeRef)
	}
	if !found {
		t.Error("switchGates carries no skill gate for the blind candidate")
	}
	if _, err := resolveSendBuilder(rt, "designer", testOpencodeRef, testClaudeRef, skillTestDir); err == nil || !strings.Contains(err.Error(), skillBlindNote) {
		t.Errorf("send --builder err = %v, want the skill note", err)
	}
}

func TestPolicyViewOverPickGatesMarksTheSkillAwarePick(t *testing.T) {
	t.Parallel()
	rt := skillRuntime(t, "/repo/.agents/skills/design-runbooks/SKILL.md")
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"designer": designerRow(),
	})
	got := FormatPolicyFor(rt.RoleRegistry(), nil, rt.Candidates, policy.Policy{}, PickGates(rt, skillTestDir), nil, availability.History{}, baseTime, time.UTC)
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "would pick") && !strings.Contains(line, rt.Candidates.NameOf(testOpencodeRef)) {
			t.Fatalf("view picks %q, want the opencode candidate:\n%s", line, got)
		}
	}
	if !strings.Contains(got, "would pick") || !strings.Contains(got, skillBlindNote) {
		t.Errorf("view = %q, want the opencode pick and the claude skill note", got)
	}
}
