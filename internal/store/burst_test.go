package store

import (
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"
)

// TestBindingBurstRoundTrip pins the burst forward facts on the save path: a
// head that forwarded a round must read back exactly what it wrote, or the
// next tick would re-send or re-place a round the node already holds. Every
// branch of the shape gets its case: no burst state, a live forward, ship
// state with no round out, and empty containers -- which are written absent
// and read back nil, so a binding whose ship state was emptied stores no
// trace of the state it once had. A record written before the field existed
// decodes with no burst state.
//
// Mutation check: take the key name out of one new struct tag and this test
// stays green -- both halves read and write the renamed key -- while
// TestBindingShapeMatchesFormat goes red on the golden: the shape test pins
// the spelling, this one the values.
func TestBindingBurstRoundTrip(t *testing.T) {
	claimed := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	sent := time.Date(2026, 11, 2, 9, 0, 5, 0, time.UTC)
	queued := time.Date(2026, 11, 2, 8, 50, 0, 0, time.UTC)
	sha := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	forward := &Forward{
		Node:          "b12",
		NodeBinding:   "1f3a9c2d-webshop",
		Round:         3,
		Phase:         "sending",
		ClaimedAt:     claimed,
		SentAt:        sent,
		QueuedSince:   queued,
		StopRequested: true,
	}
	full := &BurstFacts{
		Forward:      forward,
		Shipped:      map[string]string{"b12": sha},
		NodeBindings: []string{"1f3a9c2d-webshop"},
	}
	shipState := &BurstFacts{
		Shipped:      map[string]string{"b12": sha},
		NodeBindings: []string{"1f3a9c2d-webshop"},
	}

	t.Run("a sending forward and its ship state survive whole", func(t *testing.T) {
		got := saveAndLoad(t, full)
		if got.Burst == nil {
			t.Fatal("Burst = nil after the round trip, want the saved forward state")
		}
		checkBurstForward(t, forward, got.Burst.Forward)
		if !maps.Equal(got.Burst.Shipped, full.Shipped) {
			t.Errorf("Shipped = %v, want %v", got.Burst.Shipped, full.Shipped)
		}
		if !slices.Equal(got.Burst.NodeBindings, full.NodeBindings) {
			t.Errorf("NodeBindings = %v, want %v", got.Burst.NodeBindings, full.NodeBindings)
		}
	})

	for _, tc := range []struct {
		name  string
		burst *BurstFacts
		want  *BurstFacts
	}{
		{"a binding with no burst state", nil, nil},
		{"ship state with no round out", shipState, shipState},
		{"empty containers come back absent", &BurstFacts{Shipped: map[string]string{}, NodeBindings: []string{}}, &BurstFacts{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := saveAndLoad(t, tc.burst); !reflect.DeepEqual(got.Burst, tc.want) {
				t.Errorf("Burst = %+v, want %+v", got.Burst, tc.want)
			}
		})
	}

	t.Run("a record written before the field has no burst state", func(t *testing.T) {
		s := New(t.TempDir())
		if err := s.Save(newBinding("webshop", "/repo")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// The shape format 18 wrote: every key the bump added is simply absent.
		putRecordJSON(t, s, "webshop", `{"format":18,"name":"webshop","cwd":"/repo",`+
			`"runner":{"kind":"opencode"},"actor":"builder","shape":"writer","state":"active","round":1}`)
		got, err := s.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Burst != nil {
			t.Errorf("Burst = %+v, want nil: an old record must not gain forward state it never wrote", got.Burst)
		}
	})
}

func saveAndLoad(t *testing.T, burst *BurstFacts) Binding {
	t.Helper()
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/projects/webshop")
	b.Burst = burst
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got
}

func checkBurstForward(t *testing.T, want, got *Forward) {
	t.Helper()
	if got == nil {
		t.Fatalf("Burst.Forward = nil, want %+v", *want)
	}
	if got.Node != want.Node {
		t.Errorf("Forward.Node = %q, want %q", got.Node, want.Node)
	}
	if got.NodeBinding != want.NodeBinding {
		t.Errorf("Forward.NodeBinding = %q, want %q", got.NodeBinding, want.NodeBinding)
	}
	if got.Round != want.Round {
		t.Errorf("Forward.Round = %d, want %d", got.Round, want.Round)
	}
	if got.Phase != want.Phase {
		t.Errorf("Forward.Phase = %q, want %q", got.Phase, want.Phase)
	}
	if !got.ClaimedAt.Equal(want.ClaimedAt) {
		t.Errorf("Forward.ClaimedAt = %v, want %v", got.ClaimedAt, want.ClaimedAt)
	}
	if !got.SentAt.Equal(want.SentAt) {
		t.Errorf("Forward.SentAt = %v, want %v", got.SentAt, want.SentAt)
	}
	if !got.QueuedSince.Equal(want.QueuedSince) {
		t.Errorf("Forward.QueuedSince = %v, want %v", got.QueuedSince, want.QueuedSince)
	}
	if got.StopRequested != want.StopRequested {
		t.Errorf("Forward.StopRequested = %v, want %v", got.StopRequested, want.StopRequested)
	}
}
