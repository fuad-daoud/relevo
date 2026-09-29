package store

import (
	"encoding/json"
	"testing"
)

// TestLoadLegacyStates pins the states this version deleted still loading:
// "held" (a payload in flight) and "orphaned" (the mastermind's session gone)
// read back as active, while a state the version does know is not rewritten.
func TestLoadLegacyStates(t *testing.T) {
	s := New(t.TempDir())

	for _, tc := range []struct {
		name string
		set  *string
		want State
	}{
		{"held", ptr("held"), StateActive},
		{"orphaned", ptr("orphaned"), StateActive},
		{"a known state is kept", nil, StateNeedsYou},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBinding("webshop", "/repo/webshop")
			b.Round = 2
			if tc.set != nil {
				b.State = State(*tc.set)
			} else {
				b.State = StateNeedsYou
			}
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatalf("encode record JSON: %v", err)
			}
			putRecordJSON(t, s, "webshop", string(raw))

			got, err := s.Load("webshop")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.State != tc.want {
				t.Errorf("state %v loaded as %q, want %q", tc.set, got.State, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
