package roles

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// readerSource is a minimal valid agentsrc single source named my-reader.
const readerSource = "---\nname: my-reader\ndescription: reads the change\nshape: reader\noutput: findings\nrequires: [my-scout]\n---\nbody\n"

// testSet parses an in-memory candidates.json body.
func testSet(t *testing.T, body string) *candidate.Set {
	t.Helper()
	set, _, err := candidate.Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("candidate.Parse: %v", err)
	}
	return set
}

// ptrStr returns a pointer to s, for a Row's optional string fields.
func ptrStr(s string) *string { return &s }

// TestToRolesFileShipped pins §4.1's shipped path: a builder actor on
// plan-executor builds the registry today's builtin builder builds, with the
// same candidates, off list and tier. Spec is compared for every kind, and
// the whole Role (Ranked included) must match.
func TestToRolesFileShipped(t *testing.T) {
	set := testSet(t, `[
	  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
	  {"harness":"claude","provider":"test","model":"b","roles":["builder"]}
	]`)
	actors := map[string]Actor{
		"builder": {
			Agent: "plan-executor",
			Candidates: []Entry{
				{Candidate: "claude/test/a"},
				{Candidate: "claude/test/b", Off: true},
			},
			Tier: "edit",
		},
	}

	f, warnings, err := FromActors(map[string]AgentEntry{}, actors)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	got, err := Build(f, set, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want, err := Build(&File{Rows: map[string]Row{
		"builder": {
			Candidates: []string{"claude/test/a", "claude/test/b"},
			Off:        []string{"claude/test/b"},
			Tier:       ptrStr("edit"),
		},
	}}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("Build(want): %v", err)
	}

	for _, h := range harness.All() {
		gs, err := got.Spec("builder", h.Kind)
		if err != nil {
			t.Fatalf("Spec(builder, %s): %v", h.Kind, err)
		}
		ws, err := want.Spec("builder", h.Kind)
		if err != nil {
			t.Fatalf("want Spec(builder, %s): %v", h.Kind, err)
		}
		if !reflect.DeepEqual(gs, ws) {
			t.Errorf("Spec(builder, %s) = %+v, want %+v", h.Kind, gs, ws)
		}
	}

	gotRole, _ := got.Role("builder")
	wantRole, _ := want.Role("builder")
	if !reflect.DeepEqual(gotRole, wantRole) {
		t.Errorf("Role(builder) = %+v, want %+v", gotRole, wantRole)
	}
}

// TestToRolesFileCustomAndNative pins §4.1's custom paths: a source agent's
// definitions come from its rendered kinds, a native agent's map is copied,
// and an agents entry no actor uses is warned about.
func TestToRolesFileCustomAndNative(t *testing.T) {
	agents := map[string]AgentEntry{
		"my-reader": {Source: readerSource},
		"my-exec":   {Shape: "writer", Native: map[string]DefRow{"claude": {Agent: "my-exec", Requires: []string{"my-scout"}}}},
		"unused":    {Shape: "reader", Native: map[string]DefRow{"claude": {Agent: "unused"}}},
	}
	actors := map[string]Actor{
		"designer": {Agent: "my-reader", Candidates: []Entry{{Candidate: "claude/test/a"}}},
		"helper":   {Agent: "my-exec", Candidates: []Entry{{Candidate: "claude/test/a"}}},
	}

	f, warnings, err := FromActors(agents, actors)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}

	// The source agent: shape from the source, a definition for each rendered
	// kind, the source's requires.
	src, err := agentsrc.Parse([]byte(readerSource))
	if err != nil {
		t.Fatalf("agentsrc.Parse: %v", err)
	}
	designer := f.Rows["designer"]
	if designer.Shape == nil || *designer.Shape != "reader" {
		t.Errorf("designer.shape = %v, want reader", designer.Shape)
	}
	wantDefs := make(map[string]DefRow)
	for _, kind := range agentsrc.RenderedKinds(src) {
		wantDefs[kind] = DefRow{Agent: "my-reader", Requires: []string{"my-scout"}}
	}
	if !reflect.DeepEqual(designer.Definitions, wantDefs) {
		t.Errorf("designer.definitions = %+v, want %+v", designer.Definitions, wantDefs)
	}

	// The native agent: its own shape and a copy of its map.
	helper := f.Rows["helper"]
	if helper.Shape == nil || *helper.Shape != "writer" {
		t.Errorf("helper.shape = %v, want writer", helper.Shape)
	}
	wantNative := map[string]DefRow{"claude": {Agent: "my-exec", Requires: []string{"my-scout"}}}
	if !reflect.DeepEqual(helper.Definitions, wantNative) {
		t.Errorf("helper.definitions = %+v, want %+v", helper.Definitions, wantNative)
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0], "agent unused is not used by any actor") {
		t.Errorf("warnings = %v, want the unused-agent warning", warnings)
	}
}

// TestToRolesFileErrors pins §4.1's errors: an unknown agent, check on a
// reader, and a builtin actor name whose agent has the wrong shape. Every one
// wraps ErrBadRoles.
func TestToRolesFileErrors(t *testing.T) {
	tests := []struct {
		name          string
		agents        map[string]AgentEntry
		actors        map[string]Actor
		wantSubstring string
	}{
		{
			name:          "unknown agent",
			actors:        map[string]Actor{"builder": {Agent: "nope"}},
			wantSubstring: `actor builder: agent "nope" is not shipped and not in agents`,
		},
		{
			name:          "check on a reader",
			agents:        map[string]AgentEntry{"my-reader": {Source: readerSource}},
			actors:        map[string]Actor{"designer": {Agent: "my-reader", Check: actorBoolPtr(true)}},
			wantSubstring: "actor designer: check is only for a writer agent",
		},
		{
			name:          "reviewer on a writer agent",
			actors:        map[string]Actor{"reviewer": {Agent: "plan-executor"}},
			wantSubstring: "actor reviewer must run a reader agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := FromActors(tt.agents, tt.actors)
			if err == nil {
				t.Fatalf("FromActors = nil error, want one containing %q", tt.wantSubstring)
			}
			if !errors.Is(err, ErrBadRoles) {
				t.Errorf("err = %v, want it to wrap ErrBadRoles", err)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Errorf("err = %q, want substring %q", err.Error(), tt.wantSubstring)
			}
		})
	}
}

// TestToRolesFileOffPinsTheCandidateList pins §4.1's candidates rule: the raw
// entries in order, with the off entries also listed in Off.
func TestToRolesFileOffPinsTheCandidateList(t *testing.T) {
	actors := map[string]Actor{
		"builder": {
			Agent: "plan-executor",
			Candidates: []Entry{
				{Candidate: "a"},
				{Candidate: "b", Off: true},
			},
		},
	}
	f, _, err := FromActors(nil, actors)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}
	row := f.Rows["builder"]
	if !reflect.DeepEqual(row.Candidates, []string{"a", "b"}) {
		t.Errorf("candidates = %v, want [a b]", row.Candidates)
	}
	if !reflect.DeepEqual(row.Off, []string{"b"}) {
		t.Errorf("off = %v, want [b]", row.Off)
	}
}

// actorBoolPtr returns a pointer to b, for an Actor's optional check.
func actorBoolPtr(b bool) *bool { return &b }
