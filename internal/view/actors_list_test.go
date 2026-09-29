package view

// A2 round 2's view tests: an actors-built registry reports file mode, and
// `relevo config`'s actors block renders shipped, off, custom and native
// agents in plain text.

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// TestActorsRegistryIsFileMode pins R2: a registry built from the actors
// section is file mode, names actors in its list text, and is neither the
// roles.json nor the legacy source.
func TestActorsRegistryIsFileMode(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, `[{"harness":"claude","provider":"test","model":"a"}]`)

	rf, _, err := roles.FromActors(nil, map[string]roles.Actor{
		"builder": {Agent: "plan-executor", Candidates: []roles.Entry{{Candidate: "a"}}},
	})
	if err != nil {
		t.Fatalf("roles.FromActors: %v", err)
	}
	reg, err := roles.Build(rf, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	if !reg.FileMode() {
		t.Error("FileMode() = false, want true for an actors registry")
	}
	if reg.Source() != roles.SourceActors {
		t.Errorf("Source() = %q, want %q", reg.Source(), roles.SourceActors)
	}
	if got, want := reg.ListText("builder"), "actors builder.candidates"; got != want {
		t.Errorf("ListText(builder) = %q, want %q", got, want)
	}

	// A roles.json registry is file mode too, and names its file.
	fileReg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/a"}},
	})
	if !fileReg.FileMode() {
		t.Error("FileMode() = false, want true for a roles.json registry")
	}
	if got, want := fileReg.ListText("builder"), "roles.json builder.candidates"; got != want {
		t.Errorf("ListText(builder) = %q, want %q", got, want)
	}

	// The legacy derivation is not file mode, and names the order.
	legacy, _ := roles.Build(nil, set, policy.Policy{})
	if legacy.FileMode() {
		t.Error("FileMode() = true, want false for the legacy derivation")
	}
	if got, want := legacy.ListText("builder"), "order.builder"; got != want {
		t.Errorf("ListText(builder) = %q, want %q", got, want)
	}
}

// uiDesignerSource is a valid custom agent source for the actors block test.
const uiDesignerSource = `---
name: ui-designer
description: designs screens
shape: reader
output: design
kinds: [claude, agy]
---
Design the screens.
`

// TestFormatActors pins R7: the shipped, off, custom and native entries, and
// that no escape code ever appears.
func TestFormatActors(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"a"},
	  {"harness":"claude","provider":"test","model":"b"},
	  {"harness":"claude","provider":"test","model":"c"}
	]`)

	actorsSection := map[string]roles.Actor{
		"builder": {
			Agent:      "plan-executor",
			Candidates: []roles.Entry{{Candidate: "a"}, {Candidate: "c", Off: true}},
			Tier:       "yolo",
			Check:      ptr(true),
		},
		"reviewer": {Agent: "reviewer", Candidates: []roles.Entry{{Candidate: "b"}}, Tier: "yolo"},
		"designer": {Agent: "ui-designer", Candidates: []roles.Entry{{Candidate: "a"}}},
	}
	agentsSection := map[string]roles.AgentEntry{
		"ui-designer": {Source: uiDesignerSource},
		"my-exec": {
			Shape:  "writer",
			Native: map[string]roles.DefRow{"claude": {Agent: "my-exec"}},
		},
	}

	rf, _, err := roles.FromActors(agentsSection, actorsSection)
	if err != nil {
		t.Fatalf("roles.FromActors: %v", err)
	}
	reg, err := roles.Build(rf, set, policy.Policy{MaxTier: "yolo"})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	L := config.Loaded{Candidates: set, Actors: actorsSection, Agents: agentsSection}
	got := FormatActors(L, reg)

	want := "builder  plan-executor  writer  shipped  tier yolo  check on\n" +
		"  candidates  a, c (off)\n" +
		"designer  ui-designer  reader  custom  tier -\n" +
		"  candidates  a\n" +
		"reviewer  reviewer  reader  shipped  tier yolo\n" +
		"  candidates  b\n" +
		"\nagents\n" +
		"  my-exec  writer  native  kinds claude\n" +
		"  ui-designer  reader  output design  kinds claude, agy\n"
	if got != want {
		t.Errorf("FormatActors =\n%q\nwant:\n%q", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FormatActors must print no escape codes:\n%q", got)
	}
}
