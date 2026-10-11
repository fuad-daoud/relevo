package relevo

import (
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// countingRegistry counts Get calls, to pin one lookup per mastermind.
type countingRegistry struct {
	mastermind.Registry
	gets int
}

func (c *countingRegistry) Get(id string) (mastermind.Record, error) {
	c.gets++
	return c.Registry.Get(id)
}

// bulkClaims adds the report's one-read surface to fakeClaimStore.
type bulkClaims struct{ fakeClaimStore }

func (b bulkClaims) LiveAll(time.Time) (map[string]*delivery.Claim, error) {
	return b.fakeClaimStore, nil
}

// goneRouteRuntime holds a live push claim for testClaimMasterMind and a
// record whose host is 101@1010; hostStart answers ProcStart for that pid.
func goneRouteRuntime(t *testing.T, hostStart func(pid int) (int64, error)) (Runtime, *countingRegistry) {
	t.Helper()
	rt := routeRuntime(t)
	reg, _ := testMasterMindRegistry(t, mastermind.Record{
		ID: testClaimMasterMind, Name: "architect-1", HarnessKind: "claude",
		SessionID: "sess", CWD: "/repo", HostPID: 101, HostStartedAt: 1010,
	})
	counting := &countingRegistry{Registry: reg}
	rt.MasterMinds = counting
	rt.ProcStart = hostStart
	rt.Channels = fakeClaimStore{testClaimMasterMind: &delivery.Claim{MasterMind: testClaimMasterMind, PID: 1}}
	return rt, counting
}

func TestMastermindRouteLiveClaimLiveRecord(t *testing.T) {
	rt, _ := goneRouteRuntime(t, func(int) (int64, error) { return 1010, nil })
	route, live := mastermindRoute(rt, store.Binding{MasterMindID: testClaimMasterMind}, nil)
	if route != "push" || !live {
		t.Errorf("route = %s live = %v, want push live", route, live)
	}
}

func TestMastermindRouteLiveClaimGoneRecordIsNotLive(t *testing.T) {
	rt, _ := goneRouteRuntime(t, func(int) (int64, error) { return 0, errors.New("no such process") })
	route, live := mastermindRoute(rt, store.Binding{MasterMindID: testClaimMasterMind}, nil)
	if route != "push" || live {
		t.Errorf("route = %s live = %v, want push not live", route, live)
	}
	if PushLive(rt, testClaimMasterMind) {
		t.Error("PushLive must be false for a gone record")
	}
}

func TestMastermindRouteLookupFailureKeepsClaimAnswer(t *testing.T) {
	rt, _ := goneRouteRuntime(t, func(int) (int64, error) { return 0, errors.New("no such process") })
	other := store.Binding{MasterMindID: otherClaimMasterMind}
	rt.Channels = fakeClaimStore{otherClaimMasterMind: &delivery.Claim{MasterMind: otherClaimMasterMind, PID: 1}}
	route, live := mastermindRoute(rt, other, nil)
	if route != "push" || !live {
		t.Errorf("unknown record: route = %s live = %v, want push live", route, live)
	}
}

func TestStatusClassifiesEachMasterMindOnce(t *testing.T) {
	probes := 0
	rt, counting := goneRouteRuntime(t, func(int) (int64, error) {
		probes++
		return 0, errors.New("no such process")
	})
	rt.Channels = bulkClaims{rt.Channels.(fakeClaimStore)}
	seedPending(t, rt, "one", testClaimMasterMind, "claude")
	seedPending(t, rt, "two", testClaimMasterMind, "claude")
	seedPending(t, rt, "three", testClaimMasterMind, "claude")
	counting.gets = 0
	probes = 0

	doc := statusDoc(t, rt)
	if len(doc.Bindings) != 3 {
		t.Fatalf("status has %d rows, want 3", len(doc.Bindings))
	}
	for _, b := range doc.Bindings {
		if b.MasterMindRoute != "push" || b.MasterMindRouteLive {
			t.Errorf("%s: route %s live %v, want push not live", b.Name, b.MasterMindRoute, b.MasterMindRouteLive)
		}
	}
	if probes != 1 {
		t.Errorf("ProcStart called %d times for one mastermind, want 1", probes)
	}
	if counting.gets != 1 {
		t.Errorf("Get called %d times for one mastermind, want 1", counting.gets)
	}
}

func TestResolveVerbMasterMindHealOnlyWhenAsked(t *testing.T) {
	rt, _ := goneRouteRuntime(t, func(pid int) (int64, error) {
		if pid == 202 {
			return 2020, nil
		}
		return 0, errors.New("no such process")
	})
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_PID", "202")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess")

	rec, _, err := resolveVerbMasterMindHeal(rt, testClaimMasterMind, false)
	if err != nil || rec.HostPID != 101 {
		t.Fatalf("heal off: host %d err %v, want the stale host 101", rec.HostPID, err)
	}
	rec, _, err = resolveVerbMasterMindHeal(rt, testClaimMasterMind, true)
	if err != nil || rec.HostPID != 202 {
		t.Fatalf("heal on: host %d err %v, want the calling host 202", rec.HostPID, err)
	}
}
