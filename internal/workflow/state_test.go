package workflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStateEventActionRoundTripThroughJSON(t *testing.T) {
	s := State{
		Status:   StatusRunning,
		Reason:   "because",
		At:       "build",
		Awaiting: Awaiting{Step: "build", Member: "builder", Round: 2, Run: 7, Children: []string{"c1"}},
		Visits:   map[string]int{"build": 3},
		Iter:     map[string]Iter{"plans": {Index: 1, Items: []string{"a", "b"}}},
		Results: map[string]Result{
			"build": {Round: 1, Status: "done",
				Outcomes:  map[string]string{"verdict": "pass"},
				Artifacts: map[string][]string{"report": {"r.md"}}},
		},
	}
	if got := roundTripState(t, s); !reflect.DeepEqual(got, s) {
		t.Fatalf("state round trip:\n got %+v\nwant %+v", got, s)
	}

	e := Event{
		Kind: EventStepClosed, Step: "build", Member: "builder", Round: 2, Run: 7, Status: "done",
		Outcomes:  map[string]string{"verdict": "pass"},
		Artifacts: map[string][]string{"report": {"r.md"}},
		Result:    "green", Log: "l-1", Child: "c1", Reason: "why",
	}
	gotEvent, err := DecodeEvent(EncodeEvent(e))
	if err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if !reflect.DeepEqual(gotEvent, e) {
		t.Fatalf("event round trip:\n got %+v\nwant %+v", gotEvent, e)
	}

	a := Action{Kind: ActionSend, Step: "build", Actor: "builder", Seed: "shipped:review", Command: "make check", Reason: "why"}
	gotAction, err := DecodeAction(EncodeAction(a))
	if err != nil {
		t.Fatalf("decode action: %v", err)
	}
	if !reflect.DeepEqual(gotAction, a) {
		t.Fatalf("action round trip:\n got %+v\nwant %+v", gotAction, a)
	}
}

// roundTripState marshals and unmarshals a state, the way the chain row stores
// it.
func roundTripState(t *testing.T, s State) State {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return out
}

func TestDecodeRejectsAnUnknownKind(t *testing.T) {
	if _, err := DecodeEvent(`{"Kind":"bogus"}`); err == nil {
		t.Fatalf("want an error for an unknown event kind")
	}
	if _, err := DecodeAction(`{"Kind":"bogus"}`); err == nil {
		t.Fatalf("want an error for an unknown action kind")
	}
	if _, err := DecodeEvent(""); err == nil {
		t.Fatalf("want an error for an empty document")
	}
}
