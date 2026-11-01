package relevo

import (
	"sync"
	"time"
)

// authGraceLimit is how long a server may keep answering a transient auth
// error before the daemon halts the binding and asks for a human.
const authGraceLimit = 15 * time.Minute

// AuthGrace remembers, per binding name, the first sight of a transient auth
// error that might clear on its own. The daemon owns the one process-wide
// record; a CLI one-shot leaves it nil and never halts on a transient 401.
//
// Every method is nil-safe: a nil *AuthGrace never expires.
type AuthGrace struct {
	mu    sync.Mutex
	since map[string]time.Time
}

func NewAuthGrace() *AuthGrace {
	return &AuthGrace{since: make(map[string]time.Time)}
}

// Note records now as the first sight of a transient auth error for name and
// returns when that error started. A nil receiver has no memory, so it
// answers now.
func (g *AuthGrace) Note(name string, now time.Time) time.Time {
	if g == nil {
		return now
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if first, ok := g.since[name]; ok {
		return first
	}
	if g.since == nil {
		g.since = make(map[string]time.Time)
	}
	g.since[name] = now
	return now
}

// Clear forgets name's grace: a successful poll means the auth error is over,
// and a later one starts its clock again.
func (g *AuthGrace) Clear(name string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.since, name)
}

// Expired reports whether name has been in a transient auth error for at least
// limit. A name never noted, and a nil receiver, are never expired.
func (g *AuthGrace) Expired(name string, now time.Time, limit time.Duration) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	first, ok := g.since[name]
	if !ok {
		return false
	}
	return now.Sub(first) >= limit
}
