package account

import "fmt"

// Mode is how the pick chooses among a pool's accounts.
type Mode string

const (
	// Failover is the default: the first account not gated.
	Failover Mode = "failover"
	// RoundRobin spreads fresh rounds across the pool.
	RoundRobin Mode = "round-robin"
)

// Select returns the first account in pool order that no gate covers; ok is
// false when the pool is empty or every account in it is gated. The per-round
// cursor a round-robin pool needs belongs to the caller, so this stays pure.
//
// Round-robin is refused for an opencode pool: opencode keeps one active
// credential per install, so rotating it would move every other round on the
// host.
func Select(pool []Account, gates []string, mode Mode) (Account, bool, error) {
	switch mode {
	case Failover:
	case RoundRobin:
		for _, a := range pool {
			if a.Harness == OpenCode {
				return Account{}, false, fmt.Errorf("round-robin is not allowed for opencode account %q: its active credential is global to the install", a.Name)
			}
		}
	default:
		return Account{}, false, fmt.Errorf("unknown rotation mode %q", mode)
	}

	for _, a := range pool {
		if !Gated(a, gates) {
			return a, true, nil
		}
	}
	return Account{}, false, nil
}
