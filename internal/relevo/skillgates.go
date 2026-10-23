package relevo

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

// withSkillGates appends the skill gates for the tree at dir. An empty dir
// means relevo cannot judge, so it adds none.
func withSkillGates(rt Runtime, gates []availability.Gate, dir string) []availability.Gate {
	return withProbedSkillGates(rt, gates, dir, nil)
}

// withTrackedSkillGates is withSkillGates for a bind that cuts a worktree: a
// project path counts only when tracked says the base commit has it.
func withTrackedSkillGates(rt Runtime, gates []availability.Gate, dir string, tracked func(string) bool) []availability.Gate {
	return withProbedSkillGates(rt, gates, dir, tracked)
}

func withProbedSkillGates(rt Runtime, gates []availability.Gate, dir string, tracked func(string) bool) []availability.Gate {
	if dir == "" {
		return gates
	}
	probe := skillProbe{Exists: rt.SkillExists, Tracked: tracked}
	if probe.Exists == nil {
		probe.Exists = osExists
	}
	home, _ := os.UserHomeDir()
	return append(gates, skillGates(rt, dir, home, probe)...)
}

func skillGates(rt Runtime, dir, home string, probe skillProbe) []availability.Gate {
	reg := rt.RoleRegistry()
	var out []availability.Gate
	for _, actor := range reg.Names() {
		role, _ := reg.Role(actor)
		if len(role.Skills) == 0 || rt.Candidates == nil {
			continue
		}
		for _, tok := range rt.Candidates.Refs() {
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
// error counts as tracked: relevo cannot judge, so it does not guess.
func trackedAtBase(ctx context.Context, rt Runtime, repo, base string) func(string) bool {
	return func(p string) bool {
		rel, err := filepath.Rel(repo, filepath.Dir(p))
		if err != nil {
			return true
		}
		ok, err := rt.Git.DirAt(ctx, repo, base, filepath.ToSlash(rel))
		return err != nil || ok
	}
}

// addSkillGates is the gate set an add resolves over. A bind that cuts a
// worktree runs in a fresh tree holding only what the base commit tracks.
func addSkillGates(ctx context.Context, rt Runtime, opts AddOptions) []availability.Gate {
	gates := availability.Gates(AvailabilityDeps(rt))
	if opts.CWD != "" {
		return withSkillGates(rt, gates, opts.CWD)
	}
	if rt.Git == nil {
		return withSkillGates(rt, gates, opts.Repo)
	}
	base := opts.Base
	if opts.Branch != "" {
		base = opts.Branch
	}
	if base == "" {
		base = "HEAD"
	}
	return withTrackedSkillGates(rt, gates, opts.Repo, trackedAtBase(ctx, rt, opts.Repo, base))
}
