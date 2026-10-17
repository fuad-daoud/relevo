package roles

import (
	"reflect"
	"testing"
)

func TestKnownFields(t *testing.T) {
	wantAgents := []string{"native", "shape", "source"}
	if got := KnownFields("agents"); !reflect.DeepEqual(got, wantAgents) {
		t.Fatalf("KnownFields(agents) = %v, want %v", got, wantAgents)
	}

	wantActors := []string{"agent", "candidates", "check", "outputs", "placement", "scope", "tier"}
	if got := KnownFields("actors"); !reflect.DeepEqual(got, wantActors) {
		t.Fatalf("KnownFields(actors) = %v, want %v", got, wantActors)
	}

	if got := KnownFields("policy"); got != nil {
		t.Fatalf("KnownFields(policy) = %v, want nil", got)
	}
}

func TestUnknownKeysAgents(t *testing.T) {
	body := []byte(`{
		"morphic-reviewer": {
			"shape": "reader",
			"native": {
				"claude": {
					"agent": "morphic-reviewer",
					"requires": ["helper"],
					"extra": 123
				}
			},
			"outcomes": ["verdict"],
			"role": "reviewer"
		}
	}`)

	want := []string{
		"morphic-reviewer.native.claude.extra",
		"morphic-reviewer.outcomes",
		"morphic-reviewer.role",
	}
	got := UnknownKeys("agents", body)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UnknownKeys(agents) = %v, want %v", got, want)
	}

	// Clean body produces empty/nil unknown keys
	clean := []byte(`{
		"morphic-reviewer": {
			"shape": "reader",
			"native": {
				"claude": {
					"agent": "morphic-reviewer",
					"requires": ["helper"]
				}
			}
		}
	}`)
	if gotClean := UnknownKeys("agents", clean); len(gotClean) != 0 {
		t.Fatalf("UnknownKeys(agents, clean) = %v, want empty", gotClean)
	}
}

func TestUnknownKeysActors(t *testing.T) {
	body := []byte(`{
		"reviewer": {
			"agent": "reviewer",
			"role": "reviewer",
			"scope": {
				"paths": ["@docs"],
				"comments": true,
				"extra_scope": "bad"
			},
			"outputs": {
				"verdict": {"one-of": ["pass", "changes"]},
				"findings": "artifact"
			},
			"candidates": [
				"opus",
				{"candidate": "sonnet", "off": true}
			]
		}
	}`)

	want := []string{
		"reviewer.role",
		"reviewer.scope.extra_scope",
	}
	got := UnknownKeys("actors", body)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UnknownKeys(actors) = %v, want %v", got, want)
	}

	// Clean body produces empty/nil unknown keys
	clean := []byte(`{
		"reviewer": {
			"agent": "reviewer",
			"scope": {
				"paths": ["@docs"],
				"comments": true
			},
			"outputs": {
				"verdict": {"one-of": ["pass", "changes"]},
				"findings": "artifact"
			},
			"candidates": [
				"opus",
				{"candidate": "sonnet", "off": true}
			]
		}
	}`)
	if gotClean := UnknownKeys("actors", clean); len(gotClean) != 0 {
		t.Fatalf("UnknownKeys(actors, clean) = %v, want empty", gotClean)
	}
}

func TestUnknownKeysOtherSections(t *testing.T) {
	if got := UnknownKeys("policy", []byte(`{"foo": "bar"}`)); got != nil {
		t.Fatalf("UnknownKeys(policy) = %v, want nil", got)
	}
}

func TestParseAgentsAndActorsAcceptStrayKeys(t *testing.T) {
	agentsBody := []byte(`{
		"morphic-reviewer": {
			"shape": "reader",
			"native": {
				"claude": {
					"agent": "morphic-reviewer"
				}
			},
			"outcomes": ["verdict"],
			"role": "reviewer"
		}
	}`)
	agents, _, err := ParseAgents(agentsBody)
	if err != nil {
		t.Fatalf("ParseAgents with stray keys failed: %v", err)
	}
	if _, ok := agents["morphic-reviewer"]; !ok {
		t.Fatalf("ParseAgents did not return morphic-reviewer")
	}

	actorsBody := []byte(`{
		"custom-actor": {
			"agent": "reviewer",
			"role": "reviewer",
			"unknown_field": 42
		}
	}`)
	actors, _, err := ParseActors(actorsBody)
	if err != nil {
		t.Fatalf("ParseActors with stray keys failed: %v", err)
	}
	if _, ok := actors["custom-actor"]; !ok {
		t.Fatalf("ParseActors did not return custom-actor")
	}
}
