package doctor

import (
	"errors"
	"strings"
	"testing"
)

type skillEnv struct {
	*fakeEnv
	present map[string]bool
}

func (e *skillEnv) Stat(p string) error {
	if e.present[p] {
		return nil
	}
	return errors.New("missing")
}

func newSkillEnv(paths ...string) *skillEnv {
	e := &skillEnv{fakeEnv: &fakeEnv{homeDir: "/h"}, present: map[string]bool{}}
	for _, p := range paths {
		e.present[p] = true
	}
	return e
}

var designerClaude = []SkillNeed{{Actor: "designer", Kind: "claude", Skill: "design-runbooks"}}

func TestSkillChecksFoundAtProjectPath(t *testing.T) {
	p := "/r/.claude/skills/design-runbooks/SKILL.md"
	got := SkillChecks(newSkillEnv(p), "/r", designerClaude)
	if len(got) != 1 || got[0].Severity != SevOK || !strings.Contains(got[0].Detail, p) {
		t.Fatalf("got %+v", got)
	}
}

func TestSkillChecksFoundAtUserPathOnly(t *testing.T) {
	p := "/h/.claude/skills/design-runbooks/SKILL.md"
	got := SkillChecks(newSkillEnv(p), "/r", designerClaude)
	if len(got) != 1 || got[0].Severity != SevOK || !strings.Contains(got[0].Detail, p) {
		t.Fatalf("got %+v", got)
	}
}

func TestSkillChecksAbsentFailsListingEveryPath(t *testing.T) {
	got := SkillChecks(newSkillEnv(), "/r", designerClaude)
	want := "skill design-runbooks for designer on claude not found: looked at " +
		"/r/.claude/skills/design-runbooks/SKILL.md, /h/.claude/skills/design-runbooks/SKILL.md"
	if len(got) != 1 || got[0].Severity != SevFail || got[0].Detail != want {
		t.Fatalf("got %+v, want detail %q", got, want)
	}
	wantFix := "install the skill at /r/.claude/skills/design-runbooks/SKILL.md or /h/.claude/skills/design-runbooks/SKILL.md"
	if got[0].Fix != wantFix {
		t.Errorf("fix = %q, want %q", got[0].Fix, wantFix)
	}
}

func TestSkillChecksEmptyRepoStillChecksUserPaths(t *testing.T) {
	got := SkillChecks(newSkillEnv(), "", designerClaude)
	if len(got) != 1 || got[0].Severity != SevFail || strings.Contains(got[0].Detail, "/r/") {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].Detail, "/h/.claude/skills/design-runbooks/SKILL.md") {
		t.Errorf("user path missing from %q", got[0].Detail)
	}
}

func TestSkillChecksKindThatCannotLoadSkills(t *testing.T) {
	needs := []SkillNeed{{Actor: "designer", Kind: "nope", Skill: "design-runbooks"}}
	got := SkillChecks(newSkillEnv(), "/r", needs)
	if len(got) != 1 || got[0].Severity != SevFail || !strings.Contains(got[0].Detail, "nope cannot load skills") {
		t.Fatalf("got %+v", got)
	}
}

func TestSkillChecksHomePathFailure(t *testing.T) {
	env := newSkillEnv()
	env.homeErr = errors.New("no home")
	got := SkillChecks(env, "/r", designerClaude)
	if len(got) != 1 || got[0].Severity != SevWarn || !got[0].ProbeFailed ||
		!strings.Contains(got[0].Detail, "could not resolve home directory") {
		t.Fatalf("got %+v", got)
	}
}

func TestSkillChecksNoNeedsNoRows(t *testing.T) {
	if got := SkillChecks(newSkillEnv(), "/r", nil); got != nil {
		t.Errorf("got %+v", got)
	}
}
