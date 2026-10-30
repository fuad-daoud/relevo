package doctor

import (
	"os"
	"sort"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// skillChecks builds the skill needs from the registry and checks them in the
// working directory.
func skillChecks(env Env, reg *roles.Registry, set *candidate.Set) []Check {
	repo, _ := os.Getwd()
	return SkillChecks(env, repo, skillNeeds(reg, set))
}

// skillNeeds returns one need per actor, harness kind and skill, over the
// kinds of the actor's ranked candidates (off ones included), sorted.
func skillNeeds(reg *roles.Registry, set *candidate.Set) []SkillNeed {
	var needs []SkillNeed
	for _, name := range reg.Names() {
		role, ok := reg.Role(name)
		if !ok || len(role.Skills) == 0 {
			continue
		}
		kinds := map[string]bool{}
		for _, r := range role.Ranked {
			if c, err := set.ResolveToken(r.Token); err == nil {
				kinds[c.Harness] = true
			}
		}
		for kind := range kinds {
			for _, s := range role.Skills {
				needs = append(needs, SkillNeed{Actor: name, Kind: kind, Skill: s})
			}
		}
	}
	sort.Slice(needs, func(i, j int) bool {
		a, b := needs[i], needs[j]
		if a.Actor != b.Actor {
			return a.Actor < b.Actor
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Skill < b.Skill
	})
	return needs
}
