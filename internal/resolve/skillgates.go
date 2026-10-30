package resolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/skill"
)

// skillProbe is how skill gates look at the disk. Tracked, when set, judges a
// project path by what a fresh tree cut from the base commit would hold.
type skillProbe struct {
	Exists  func(string) bool
	Tracked func(string) bool
}

func osExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// WithSkillGates appends the skill gates for the tree at dir. An empty dir
// means resolve cannot judge, so it adds none. A non-nil tracked is for a
// bind that cuts a worktree: a project path counts only when tracked says the
// base commit has it; nil judges the disk alone.
func WithSkillGates(d Deps, gates []availability.Gate, dir string, tracked func(string) bool) []availability.Gate {
	if dir == "" {
		return gates
	}
	probe := skillProbe{Exists: d.SkillExists, Tracked: tracked}
	if probe.Exists == nil {
		probe.Exists = osExists
	}
	home, _ := os.UserHomeDir()
	return append(gates, skillGates(d, dir, home, probe)...)
}

// PickGates is the gate set a bind in dir resolves over, for a view that must
// show the same pick.
func PickGates(d Deps, dir string) []availability.Gate {
	return WithSkillGates(d, availability.Gates(d.Deps), dir, nil)
}

func skillGates(d Deps, dir, home string, probe skillProbe) []availability.Gate {
	reg := d.RoleRegistry()
	var out []availability.Gate
	for _, actor := range reg.Names() {
		role, _ := reg.Role(actor)
		if len(role.Skills) == 0 || d.Candidates == nil {
			continue
		}
		for _, tok := range d.Candidates.Refs() {
			ref, err := candidate.ParseRef(tok)
			if err != nil || !reg.Serves(actor, ref) {
				continue
			}
			for _, name := range role.Skills {
				if note := missingSkillNote(probe, actor, ref.Harness, name, dir, home); note != "" {
					out = append(out, availability.Gate{
						Token: tok, Kind: availability.SkillMissing, Role: actor,
						Since: time.Now(), Note: note, Source: "relevo",
					})
				}
			}
		}
	}
	return out
}

// missingSkillNote is the reason skill name is not visible to kind, or "".
func missingSkillNote(probe skillProbe, actor, kind, name, dir, home string) string {
	if !skill.Servable(kind) {
		return fmt.Sprintf("%s cannot load skills", kind)
	}
	untracked := false
	exists := func(p string) bool {
		if !probe.Exists(p) {
			return false
		}
		if probe.Tracked == nil || !strings.HasPrefix(p, dir+string(filepath.Separator)) || probe.Tracked(p) {
			return true
		}
		untracked = true
		return false
	}
	found, looked := skill.Resolve(exists, kind, name, dir, home)
	if found != "" {
		return ""
	}
	note := fmt.Sprintf("skill %s not found for %s on %s: looked at %s", name, actor, kind, strings.Join(looked, ", "))
	if untracked {
		note += fmt.Sprintf("; untracked in %s; commit it or install it per user", dir)
	}
	return note
}

// trackedAtBase reports whether a project path is in the tree at base. A git
// error counts as tracked: resolve cannot judge, so it does not guess.
func trackedAtBase(ctx context.Context, d Deps, repo, base string) func(string) bool {
	return func(p string) bool {
		rel, err := filepath.Rel(repo, filepath.Dir(p))
		if err != nil {
			return true
		}
		ok, err := d.GitDirAt(ctx, repo, base, filepath.ToSlash(rel))
		return err != nil || ok
	}
}

// AddSkillGates is the gate set an add resolves over. A bind that cuts a
// worktree runs in a fresh tree holding only what the base commit tracks.
func AddSkillGates(ctx context.Context, d Deps, cwd, repo, branch, base string) []availability.Gate {
	gates := availability.Gates(d.Deps)
	if cwd != "" {
		return WithSkillGates(d, gates, cwd, nil)
	}
	if d.GitDirAt == nil {
		return WithSkillGates(d, gates, repo, nil)
	}
	if branch != "" {
		base = branch
	}
	if base == "" {
		base = "HEAD"
	}
	return WithSkillGates(d, gates, repo, trackedAtBase(ctx, d, repo, base))
}
