package view

import "time"

// ProviderGate is one live rate-limit gate on a provider no configured
// candidate uses. It is the ledger entry's own facts, unprojected: with no
// candidate token on that provider there is nothing to project them onto.
type ProviderGate struct {
	Provider string
	Since    time.Time
	Until    time.Time
	Note     string
	Source   string
	Binding  string
}
