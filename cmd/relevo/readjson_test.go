package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// TestConfigViewCarriesPlacement pins that `relevo config --json` carries an
// actor's stored placement: ConfigView.Actors is the stored map, so the field
// needs no separate shape, and an actor that sets none omits the key.
func TestConfigViewCarriesPlacement(t *testing.T) {
	L := config.Loaded{Actors: map[string]roles.Actor{
		"builder": {
			Agent:      "plan-executor",
			Candidates: []roles.Entry{{Candidate: "sonnet"}},
			Placement:  []string{"zen", "local"},
		},
		"reviewer": {Agent: "reviewer", Candidates: []roles.Entry{{Candidate: "sonnet"}}},
	}}

	body, err := json.Marshal(configViewOf(L, nil, nil, nil, nil))
	if err != nil {
		t.Fatalf("marshal ConfigView: %v", err)
	}
	if !strings.Contains(string(body), `"placement":["zen","local"]`) {
		t.Errorf("ConfigView JSON = %s, want the builder placement", body)
	}
	if n := strings.Count(string(body), `"placement"`); n != 1 {
		t.Errorf("ConfigView JSON = %s, want placement only on the actor that sets one (%d found)", body, n)
	}
}
