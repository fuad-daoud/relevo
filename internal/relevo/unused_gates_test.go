package relevo

import (
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/view"
)

func TestUnusedProviderGatesListsOnlyProvidersNoCandidateUses(t *testing.T) {
	rt := newRuntime(t)

	live := availability.Entry{
		Kind:    availability.RateLimited,
		Subject: "antigravity",
		At:      baseTime.Add(-time.Hour),
		Until:   baseTime.Add(2 * time.Hour),
		Note:    "RESOURCE_EXHAUSTED (code 429): Individual quota reached",
		Source:  "relevo",
		Binding: "oc-tui-a",
	}
	entries := []availability.Entry{
		live,
		{Kind: availability.RateLimited, Subject: "test", At: baseTime.Add(-2 * time.Hour), Source: "planner"},
		{Kind: availability.RateLimited, Subject: "other", At: baseTime.Add(-3 * time.Hour), Until: baseTime.Add(-time.Minute), Source: "relevo"},
		{Kind: availability.SpawnFailed, Subject: "opencode/unconfigured/m", At: baseTime, Until: baseTime.Add(time.Hour), Source: "relevo"},
	}
	if err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: entries}); err != nil {
		t.Fatalf("SaveKV: %v", err)
	}

	want := []view.ProviderGate{{
		Provider: "antigravity",
		Since:    baseTime.Add(-time.Hour),
		Until:    baseTime.Add(2 * time.Hour),
		Note:     "RESOURCE_EXHAUSTED (code 429): Individual quota reached",
		Source:   "relevo",
		Binding:  "oc-tui-a",
	}}
	if got := UnusedProviderGates(rt); !reflect.DeepEqual(got, want) {
		t.Errorf("UnusedProviderGates = %+v, want %+v", got, want)
	}
}

func TestUnusedProviderGatesWithoutCandidatesIsNil(t *testing.T) {
	rt := newRuntime(t)
	rt.Candidates = nil
	if got := UnusedProviderGates(rt); got != nil {
		t.Errorf("UnusedProviderGates with no candidate set = %+v, want nil", got)
	}

	rt = newRuntime(t)
	rt.Gates = nil
	if got := UnusedProviderGates(rt); got != nil {
		t.Errorf("UnusedProviderGates with no gates store = %+v, want nil", got)
	}
}
