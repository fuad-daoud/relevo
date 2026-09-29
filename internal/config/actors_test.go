package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/roles"
)

func TestLoadPrefersActors(t *testing.T) {
	t.Parallel()

	s := openStore(t)

	if _, err := s.Put(Candidates, []byte(
		`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`)); err != nil {
		t.Fatalf("Put(candidates): %v", err)
	}
	// The roles section names a different candidate; actors must win.
	if _, err := s.Put(Roles, []byte(
		`{"builder":{"candidates":["claude/p/other"]}}`)); err != nil {
		t.Fatalf("Put(roles): %v", err)
	}
	if _, err := s.Put(Agents, []byte(
		`{"my-exec":{"shape":"writer","native":{"claude":{"agent":"my-exec"}}}}`)); err != nil {
		t.Fatalf("Put(agents): %v", err)
	}
	if _, err := s.Put(Actors, []byte(
		`{"builder":{"agent":"plan-executor","candidates":["claude/p/m"]}}`)); err != nil {
		t.Fatalf("Put(actors): %v", err)
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if L.Registry.Source() != roles.SourceActors {
		t.Errorf("Registry source = %q, want %q", L.Registry.Source(), roles.SourceActors)
	}
	role, ok := L.Registry.Role("builder")
	if !ok {
		t.Fatal("Role(builder) not found")
	}
	if !reflect.DeepEqual(role.Candidates, []string{"claude/p/m"}) {
		t.Errorf("builder candidates = %v, want the actors section's [claude/p/m]", role.Candidates)
	}

	if !contains(L.Warnings, "config: actors is set, so the roles section is ignored") {
		t.Errorf("Warnings = %v, want the roles-ignored warning", L.Warnings)
	}
	if !contains(L.Warnings, "agent my-exec is not used by any actor") {
		t.Errorf("Warnings = %v, want the unused-agent warning", L.Warnings)
	}
	if len(L.Agents) != 1 || len(L.Actors) != 1 {
		t.Errorf("Agents = %v, Actors = %v, want both kept", L.Agents, L.Actors)
	}
}

func TestValidateActorsAgents(t *testing.T) {
	t.Parallel()

	if _, err := Validate(Agents, []byte(
		`{"my-exec":{"shape":"writer","native":{"claude":{"agent":"my-exec"}}}}`)); err != nil {
		t.Errorf("Validate(agents, good) = %v, want nil", err)
	}
	if _, err := Validate(Agents, []byte(`{"my-exec":{}}`)); err == nil {
		t.Error("Validate(agents, neither source nor native) = nil, want an error")
	}
	if _, err := Validate(Actors, []byte(
		`{"builder":{"agent":"plan-executor","candidates":["sonnet"]}}`)); err != nil {
		t.Errorf("Validate(actors, good) = %v, want nil", err)
	}
	if _, err := Validate(Actors, []byte(`{"builder":{}}`)); err == nil {
		t.Error("Validate(actors, missing agent) = nil, want an error")
	}
}

func TestExportIncludesActors(t *testing.T) {
	t.Parallel()

	out, err := EncodeDoc(Doc{
		Agents: json.RawMessage(`{"my-exec":{"shape":"writer","native":{"claude":{"agent":"my-exec"}}}}`),
		Actors: json.RawMessage(`{"builder":{"agent":"plan-executor","candidates":["sonnet"]}}`),
	})
	if err != nil {
		t.Fatalf("EncodeDoc: %v", err)
	}
	s := string(out)
	ia, ic := strings.Index(s, `"agents"`), strings.Index(s, `"actors"`)
	if ia < 0 || ic < 0 {
		t.Fatalf("EncodeDoc = %s, want both agents and actors", s)
	}
	if ia > ic {
		t.Errorf("agents must come before actors:\n%s", s)
	}
	if !strings.HasPrefix(strings.TrimSpace(s), "{\n  \"agents\"") {
		t.Errorf("agents must be the first section present:\n%s", s)
	}
}
