package delivery

import (
	"sync"
	"time"
)

const giveUpRelogAfter = time.Hour

// giveUpLog rate-limits the give-up log line to once per key,
// with at most one hourly re-log while a payload stays pending.
type giveUpLog struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (g *giveUpLog) shouldLog(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.last == nil {
		g.last = make(map[string]time.Time)
	}

	if lastLogged, ok := g.last[key]; ok {
		if now.Sub(lastLogged) < giveUpRelogAfter {
			return false
		}
	}

	g.last[key] = now
	for k, v := range g.last {
		if k != key && now.Sub(v) >= giveUpRelogAfter {
			delete(g.last, k)
		}
	}
	return true
}
