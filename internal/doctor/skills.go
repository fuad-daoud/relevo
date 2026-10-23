package doctor

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/skill"
)

// SkillNeed is one skill an actor needs on one harness kind.
type SkillNeed struct{ Actor, Kind, Skill string }

// SkillChecks reports one row per need: OK with the path found, or FAIL naming
// every path looked at. repo may be empty, which leaves the project paths out.
func SkillChecks(env Env, repo string, needs []SkillNeed) []Check {
	if len(needs) == 0 {
		return nil
	}
	home, err := env.HomePath("")
	if err != nil {
		return []Check{{
			Name: "skills", Severity: SevWarn,
			Detail:      fmt.Sprintf("could not resolve home directory: %v", err),
			ProbeFailed: true,
		}}
	}
	exists := func(p string) bool { return env.Stat(p) == nil }
	out := make([]Check, 0, len(needs))
	for _, n := range needs {
		out = append(out, skillCheck(exists, n, repo, home))
	}
	return out
}

func skillCheck(exists func(string) bool, n SkillNeed, repo, home string) Check {
	c := Check{Group: n.Kind, Name: "skill " + n.Skill}
	if !skill.Servable(n.Kind) {
		c.Severity = SevFail
		c.Detail = fmt.Sprintf("%s cannot load skills (needed by %s)", n.Kind, n.Actor)
		c.Fix = fmt.Sprintf("remove %s from %s's candidates or drop the skill", n.Kind, n.Actor)
		return c
	}
	found, looked := skill.Resolve(exists, n.Kind, n.Skill, repo, home)
	if found != "" {
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("%s for %s: %s", n.Skill, n.Actor, found)
		return c
	}
	c.Severity = SevFail
	c.Detail = fmt.Sprintf("skill %s for %s on %s not found: looked at %s",
		n.Skill, n.Actor, n.Kind, strings.Join(looked, ", "))
	c.Fix = "install the skill at " + strings.Join(looked, " or ")
	return c
}
