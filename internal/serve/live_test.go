package serve

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func TestLiveCache(t *testing.T) {
	c := newLiveCache()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	key := "client-1\x00api"
	view := &remote.LiveView{PID: 1234}

	c.put(key, 1, now, view)

	if got, hit := c.get(key, 1, now.Add(time.Second)); !hit || got != view {
		t.Errorf("get within TTL: hit = %v, got = %+v, want hit = true, view", hit, got)
	}

	if got, hit := c.get(key, 1, now.Add(liveViewTTL)); hit {
		t.Errorf("get after TTL: hit = true, want false (expired); got = %+v", got)
	}

	if got, hit := c.get(key, 2, now.Add(time.Second)); hit {
		t.Errorf("get different round: hit = true, want false; got = %+v", got)
	}
}
