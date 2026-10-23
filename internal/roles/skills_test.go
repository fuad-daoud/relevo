package roles

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/policy"
)

func TestEffectiveSkills(t *testing.T) {
	if got := EffectiveSkills("designer", nil); !reflect.DeepEqual(got, []string{"design-runbooks"}) {
		t.Errorf("designer default = %v", got)
	}
	if got := EffectiveSkills("designer", []string{"other-skill"}); !reflect.DeepEqual(got, []string{"other-skill"}) {
		t.Errorf("designer override = %v", got)
	}
	if got := EffectiveSkills("plan-executor", nil); got != nil {
		t.Errorf("plan-executor = %v, want none", got)
	}
	if got := EffectiveSkills("custom", []string{"a"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("custom declared = %v", got)
	}
	got := EffectiveSkills("designer", nil)
	got[0] = "mutated"
	if again := EffectiveSkills("designer", nil); again[0] != "design-runbooks" {
		t.Error("EffectiveSkills shares the shipped slice")
	}
}

func TestParseActorsSkillsErrors(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"bad name", `{"d": {"agent": "designer", "skills": ["Bad Name"]}}`, "d.skills[0]"},
		{"duplicate", `{"d": {"agent": "designer", "skills": ["a", "a"]}}`, "d.skills[1]: duplicate"},
	}
	for _, tt := range tests {
		_, _, err := ParseActors([]byte(tt.body))
		if err == nil || !errors.Is(err, ErrBadActors) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want ErrBadActors containing %q", tt.name, err, tt.want)
		}
	}
	if _, _, err := ParseActors([]byte(`{"d": {"agent": "designer", "skills": ["a.b-c_d"]}}`)); err != nil {
		t.Errorf("valid skill refused: %v", err)
	}
}

func TestFromActorsCarriesSkills(t *testing.T) {
	actors := map[string]Actor{
		"designer": {Agent: "designer"},
		"other":    {Agent: "designer", Skills: []string{"other-skill"}},
		"builder":  {Agent: "plan-executor"},
	}
	f, _, err := FromActors(nil, actors)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}
	if got := f.Rows["designer"].Skills; !reflect.DeepEqual(got, []string{"design-runbooks"}) {
		t.Errorf("designer row skills = %v", got)
	}
	if got := f.Rows["other"].Skills; !reflect.DeepEqual(got, []string{"other-skill"}) {
		t.Errorf("other row skills = %v", got)
	}
	if got := f.Rows["builder"].Skills; got != nil {
		t.Errorf("builder row skills = %v", got)
	}
}

func TestRowSkillsValidated(t *testing.T) {
	_, _, err := Parse("roles.json", []byte(`{"builder": {"skills": ["A"]}}`))
	if err == nil || !errors.Is(err, ErrBadRoles) || !strings.Contains(err.Error(), "skills") {
		t.Errorf("err = %v, want a bad skills row", err)
	}
}

func TestRoleSkillsCopyIsIndependent(t *testing.T) {
	f := &File{Rows: map[string]Row{"builder": {Skills: []string{"design-runbooks"}}}}
	reg, err := Build(f, emptySet(t), policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	first, _ := reg.Role("builder")
	if !reflect.DeepEqual(first.Skills, []string{"design-runbooks"}) {
		t.Fatalf("Skills = %v", first.Skills)
	}
	first.Skills[0] = "mutated"
	second, _ := reg.Role("builder")
	if second.Skills[0] != "design-runbooks" {
		t.Error("Role copy shares the registry's skills slice")
	}
}
