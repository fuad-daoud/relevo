package roles

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// TestParseActorsScopeRoundTrip pins the scope field's shape: it parses, it
// encodes, and an actor without one marshals without the key.
func TestParseActorsScopeRoundTrip(t *testing.T) {
	body := []byte(`{"builder":{"agent":"plan-executor","scope":{"paths":["@docs"],"comments":true}}}`)
	actors, _, err := ParseActors(body)
	if err != nil {
		t.Fatalf("ParseActors: %v", err)
	}
	want := &pathscope.Scope{Paths: []string{"@docs"}, Comments: true}
	if got := actors["builder"].Scope; !reflect.DeepEqual(got, want) {
		t.Fatalf("scope = %+v, want %+v", got, want)
	}

	out, err := EncodeActors(actors)
	if err != nil {
		t.Fatalf("EncodeActors: %v", err)
	}
	if !strings.Contains(string(out), `"scope"`) {
		t.Errorf("scope must be encoded:\n%s", out)
	}
	back, _, err := ParseActors(out)
	if err != nil {
		t.Fatalf("ParseActors(round trip): %v", err)
	}
	if !reflect.DeepEqual(back, actors) {
		t.Errorf("round trip = %+v, want %+v", back, actors)
	}

	none, err := EncodeActors(map[string]Actor{"builder": {Agent: "plan-executor"}})
	if err != nil {
		t.Fatalf("EncodeActors: %v", err)
	}
	if strings.Contains(string(none), "scope") {
		t.Errorf("an actor with no scope must marshal without the key:\n%s", none)
	}
}

// TestParseActorsScopeUnknownSet pins that an unknown @set is refused where
// the actor section is validated.
func TestParseActorsScopeUnknownSet(t *testing.T) {
	body := []byte(`{"builder":{"agent":"plan-executor","scope":{"paths":["@nope"]}}}`)
	_, _, err := ParseActors(body)
	if err == nil {
		t.Fatal("ParseActors(@nope) = nil, want an error")
	}
	if !errors.Is(err, ErrBadActors) {
		t.Errorf("err = %v, want ErrBadActors", err)
	}
}

// TestFromActorsCarriesScope pins the writer path: a scope on a writer actor
// lands on the Row, and Build carries it onto the Role.
func TestFromActorsCarriesScope(t *testing.T) {
	scope := &pathscope.Scope{Paths: []string{"@docs"}, Comments: true}
	actors := map[string]Actor{
		"builder": {Agent: "plan-executor", Scope: scope},
	}
	f, _, err := FromActors(map[string]AgentEntry{}, actors)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}
	row := f.Rows["builder"]
	if !reflect.DeepEqual(row.Scope, scope) {
		t.Fatalf("Row.Scope = %+v, want %+v", row.Scope, scope)
	}

	reg, err := Build(f, nil, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	role, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(builder) not found")
	}
	if !reflect.DeepEqual(role.Scope, scope) {
		t.Fatalf("Role.Scope = %+v, want %+v", role.Scope, scope)
	}
	// Role returns a copy: mutating it must not change the registry.
	role.Scope.Paths[0] = "mutated"
	again, _ := reg.Role("builder")
	if again.Scope.Paths[0] != "@docs" {
		t.Errorf("Role copy is not deep: %v", again.Scope.Paths)
	}
}

// TestFromActorsRefusesReaderScope pins the actors path's reader refusal.
func TestFromActorsRefusesReaderScope(t *testing.T) {
	actors := map[string]Actor{
		"reviewer": {Agent: "reviewer", Scope: &pathscope.Scope{Paths: []string{"@docs"}}},
	}
	_, _, err := FromActors(map[string]AgentEntry{}, actors)
	if err == nil {
		t.Fatal("FromActors(reader scope) = nil, want an error")
	}
	if !errors.Is(err, ErrBadRoles) {
		t.Errorf("err = %v, want ErrBadRoles", err)
	}
	if !strings.Contains(err.Error(), "scope is only for a writer") {
		t.Errorf("err = %q, want it to say scope is only for a writer", err)
	}
}

// TestRolesFileRefusesReaderScope pins the roles.json path's reader refusal,
// for a builtin reader and a new reader row.
func TestRolesFileRefusesReaderScope(t *testing.T) {
	for _, body := range []string{
		`{"reviewer":{"scope":{"paths":["@docs"]}}}`,
		`{"newread":{"shape":"reader","scope":{"paths":["@docs"]}}}`,
	} {
		f, _, err := Parse("roles.json", []byte(body))
		if err == nil {
			t.Fatalf("Parse(%s) = %+v, want an error", body, f)
		}
		if !errors.Is(err, ErrBadRoles) {
			t.Errorf("Parse(%s) err = %v, want ErrBadRoles", body, err)
		}
	}
}

// TestRolesFileAcceptsWriterScope pins that a writer row's scope validates
// through the roles.json path too.
func TestRolesFileAcceptsWriterScope(t *testing.T) {
	body := `{"newwriter":{"shape":"writer","scope":{"paths":["docs/**"],"comments":true}}}`
	if _, _, err := Parse("roles.json", []byte(body)); err != nil {
		t.Fatalf("Parse(writer scope) = %v, want nil", err)
	}
}
