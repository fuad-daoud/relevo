package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
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

// TestServerRowsCarrySeatFields pins that the embedded builders view rides
// through serverRowsOf untouched: a probe reporting a reservation marshals
// the seat keys, while a pre-seat probe omits them. Pure mapping -- no
// subcommand, no harness, no network.
func TestServerRowsCarrySeatFields(t *testing.T) {
	t.Parallel()

	rows := serverRowsOf([]relevo.ServerProbe{
		{
			Name: "seat", URL: "https://seat:7777", State: "enrolled",
			QueueAware: true, Builders: &remote.BuildersView{
				Running: 2, Queued: 1, Cap: 3,
				ReservedBytes: 3 << 30, BudgetBytes: 12 << 30, K: 2,
			},
		},
		{
			Name: "plain", URL: "https://plain:7777", State: "enrolled",
			QueueAware: true, Builders: &remote.BuildersView{
				Running: 1, Queued: 0, Cap: 3,
			},
		},
	})
	if len(rows) != 2 {
		t.Fatalf("serverRowsOf = %d rows, want 2", len(rows))
	}
	seat, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal seat row: %v", err)
	}
	for _, key := range []string{`"reserved_bytes"`, `"budget_bytes"`, `"k"`} {
		if !strings.Contains(string(seat), key) {
			t.Errorf("seat row = %s, want it to carry %s", seat, key)
		}
	}
	plain, err := json.Marshal(rows[1])
	if err != nil {
		t.Fatalf("marshal pre-seat row: %v", err)
	}
	for _, key := range []string{`"reserved_bytes"`, `"budget_bytes"`, `"k"`} {
		if strings.Contains(string(plain), key) {
			t.Errorf("pre-seat row = %s, want no %s key", plain, key)
		}
	}
}
