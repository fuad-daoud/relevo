package relevo

import (
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
)

type failingClaimStore struct{ fakeClaimStore }

func (failingClaimStore) Live(string, time.Time) (*delivery.Claim, error) {
	return nil, errors.New("boom")
}

// No harness is spawned and nothing reaches the network: the claim store is a map.
func TestPushLive(t *testing.T) {
	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{"pl_a": {MasterMind: "pl_a"}}
	if !PushLive(rt, "pl_a") {
		t.Error("claimed mastermind must be push-live")
	}
	if PushLive(rt, "pl_b") {
		t.Error("unclaimed mastermind must not be push-live")
	}
	rt.Channels = failingClaimStore{}
	if PushLive(rt, "pl_a") {
		t.Error("claim read error must read as not live")
	}
	rt.Channels = nil
	if PushLive(rt, "pl_a") {
		t.Error("no claim store must read as not live")
	}
}
