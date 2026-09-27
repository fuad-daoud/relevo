package relevo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fuad-daoud/relevo/internal/delivery"
)

// routeDoc is the part of the status JSON the plan's §4 requires: the two new
// route fields, and the absence of the pane-era fields.
type routeDoc struct {
	Bindings []struct {
		Name                string `json:"name"`
		MasterMindRoute     string `json:"mastermind_route"`
		MasterMindRouteLive bool   `json:"mastermind_route_live"`
		MasterMindID        string `json:"mastermind_id"`
	} `json:"bindings"`
}

// TestStatusJSONHasRouteFields is the plan's required case for §3.6: a
// tools-mode mastermind (no live claim, no deliverer) reads mastermind_route=pull
// with mastermind_route_live false, and a live channel claim reads channel with
// mastermind_route_live true.
func TestStatusJSONHasRouteFields(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	doc := statusDoc(t, rt)
	if len(doc.Bindings) != 1 {
		t.Fatalf("status has %d rows, want 1", len(doc.Bindings))
	}
	if doc.Bindings[0].MasterMindRoute != "pull" {
		t.Errorf("mastermind_route = %q, want pull for a tools-mode mastermind", doc.Bindings[0].MasterMindRoute)
	}
	if doc.Bindings[0].MasterMindRouteLive {
		t.Error("mastermind_route_live must be false for the pull route")
	}
	if doc.Bindings[0].MasterMindID != "pl_aaaaaaaabbbb" {
		t.Errorf("mastermind_id = %q, want the binding's mastermind id", doc.Bindings[0].MasterMindID)
	}

	// A live channel claim turns the same row into the channel route.
	rt.Channels = fakeClaimStore{"pl_aaaaaaaabbbb": &delivery.Claim{MasterMind: "pl_aaaaaaaabbbb", PID: 1}}
	doc = statusDoc(t, rt)
	if doc.Bindings[0].MasterMindRoute != "channel" {
		t.Errorf("mastermind_route = %q, want channel with a live claim", doc.Bindings[0].MasterMindRoute)
	}
	if !doc.Bindings[0].MasterMindRouteLive {
		t.Error("mastermind_route_live must be true for a live channel")
	}
}

// TestStatusJSONOmitsPaneEraFields keeps the removal honest: the fields §4
// deletes must not reappear in the JSON.
func TestStatusJSONOmitsPaneEraFields(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The pane-era error field is gone from the top level, as its rows' pane
	// fields are below.
	rows, _ := doc["bindings"].([]any)
	if len(rows) == 0 {
		t.Fatal("no binding rows")
	}
	row, _ := rows[0].(map[string]any)
	for _, gone := range []string{"mastermind_pane", "mastermind_status", "mastermind_focused", "workspace", "builder_pane", "foreign", "sub_agents", "hold"} {
		if _, ok := row[gone]; ok {
			t.Errorf("%s must be gone from a status row", gone)
		}
	}
}

// statusDoc marshals a Status report and reads back the route fields.
func statusDoc(t *testing.T, rt Runtime) routeDoc {
	t.Helper()
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc routeDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc
}
