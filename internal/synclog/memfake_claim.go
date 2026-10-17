package synclog

import (
	"context"
	"fmt"
	"time"
)

// claimRow is what the log holds for one claimed identity.
type claimRow struct {
	token string
	at    time.Time
}

// ClaimIdentity is the compare-and-set the remote runs: a claim is written
// where there is none, where it is the caller's own, or where it has gone stale,
// and the answer is a read of what the log holds afterwards.
func (m *MemTransport) ClaimIdentity(_ context.Context, origin, token string, at time.Time) (string, time.Time, error) {
	if origin == "" {
		return "", time.Time{}, fmt.Errorf("synclog: claim an identity: no origin: %w", ErrInvalid)
	}
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	held, ok := m.log.claims[origin]
	if token != "" && (!ok || held.token == token || at.Sub(held.at) > ClaimTTL) {
		if m.log.claims == nil {
			m.log.claims = make(map[string]claimRow)
		}
		m.log.claims[origin] = claimRow{token: token, at: at}
		held = m.log.claims[origin]
	}
	return held.token, held.at, nil
}

// OverwriteClaim replaces a claim whatever it holds, as a remote's
// last-writer-wins push does when two machines wrote the row before either
// pulled. It is how a test lays down the double win a compare-and-set alone
// does not rule out.
func (m *MemTransport) OverwriteClaim(origin, token string, at time.Time) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()
	if m.log.claims == nil {
		m.log.claims = make(map[string]claimRow)
	}
	m.log.claims[origin] = claimRow{token: token, at: at}
}

// Claims returns the identities that hold a claim, so a test can assert the
// remote holds none it did not expect.
func (m *MemTransport) Claims() []string {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()
	out := make([]string, 0, len(m.log.claims))
	for origin := range m.log.claims {
		out = append(out, origin)
	}
	return out
}
