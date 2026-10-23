package main

import (
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// skillNeeds is a pure function over the registry: no subcommand runs, so the
// test needs neither a harness binary nor the network.
func TestSkillNeedsOneRowPerActorKindSkill(t *testing.T) {
	set, _, err := candidate.Parse("test", []byte(`[
	  {"harness":"claude","provider":"anthropic","model":"sonnet"},
	  {"harness":"codex","provider":"openai","model":"gpt"}]`))
	if err != nil {
		t.Fatalf("candidate.Parse: %v", err)
	}
	f := &roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"claude/anthropic/sonnet"}},
		"designer": {
			Shape:       strp("writer"),
			Definitions: map[string]roles.DefRow{"claude": {Agent: "designer"}, "codex": {Agent: "designer"}},
			Candidates:  []string{"claude/anthropic/sonnet", "codex/openai/gpt", "claude/anthropic/sonnet#high"},
			Off:         []string{"codex/openai/gpt"},
			Skills:      []string{"design-runbooks"},
		},
	}}
	reg, err := roles.Build(f, set, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []doctor.SkillNeed{
		{Actor: "designer", Kind: "claude", Skill: "design-runbooks"},
		{Actor: "designer", Kind: "codex", Skill: "design-runbooks"},
	}
	if got := skillNeeds(reg, set); !reflect.DeepEqual(got, want) {
		t.Errorf("skillNeeds = %+v, want %+v", got, want)
	}
}

func strp(s string) *string { return &s }
