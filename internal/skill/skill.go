// Package skill says where each harness kind looks for a skill and whether a
// named skill is installed there. It is pure: the file system is a callback.
package skill

import "path/filepath"

// roots are a kind's skill directories, project first. A leading "~/" is the
// user's home; any other root is under the repo.
var roots = map[string][]string{
	"claude":   {".claude/skills", "~/.claude/skills"},
	"opencode": {".agents/skills", "~/.agents/skills"},
	"agy":      {".agents/skills", "~/.agents/skills"},
	"codex":    {".agents/skills", "~/.agents/skills", "~/.codex/skills"},
	"muse":     {".agents/skills", ".claude/skills", "~/.agents/skills", "~/.claude/skills"},
}

// Servable reports whether kind loads skills at all.
func Servable(kind string) bool {
	_, ok := roots[kind]
	return ok
}

// Locations returns the ordered SKILL.md paths kind would read for name. The
// project paths are left out when repo is empty.
func Locations(kind, name, repo, home string) []string {
	var out []string
	for _, root := range roots[kind] {
		switch {
		case len(root) > 2 && root[:2] == "~/":
			out = append(out, filepath.Join(home, root[2:], name, "SKILL.md"))
		case repo != "":
			out = append(out, filepath.Join(repo, root, name, "SKILL.md"))
		}
	}
	return out
}

// Resolve returns the first location where exists is true, and every location
// it looked at. found is empty when the skill is not installed.
func Resolve(exists func(string) bool, kind, name, repo, home string) (found string, looked []string) {
	looked = Locations(kind, name, repo, home)
	for _, p := range looked {
		if exists(p) {
			return p, looked
		}
	}
	return "", looked
}
