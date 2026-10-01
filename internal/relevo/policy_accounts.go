package relevo

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
)

// formatAccountPools renders the configured login pools: one block per quota
// group, one row per account in config order, and whether a live gate covers
// it. It returns "" when no accounts are configured, so a host with none
// prints exactly the view it always did.
func formatAccountPools(accounts account.Set, gates []availability.Gate, live []availability.Entry) string {
	if len(accounts) == 0 {
		return ""
	}

	width := 0
	for _, a := range accounts {
		if n := len(a.Name); n > width {
			width = n
		}
	}

	var sb strings.Builder
	sb.WriteString("\naccounts\n")
	for _, group := range accountGroups(accounts) {
		sb.WriteString("  " + group + "\n")
		for _, a := range accounts {
			if !slices.Contains(a.Groups, group) {
				continue
			}
			state := "ready"
			if text := accountGateText(a, gates, live); text != "" {
				state = text
			}
			fmt.Fprintf(&sb, "    %-*s  %-8s  %s\n", width, a.Name, string(a.Harness), state)
		}
	}
	return sb.String()
}

// accountGroups is the distinct quota groups the accounts serve, in first-seen
// order, so the block is stable across runs.
func accountGroups(accounts account.Set) []string {
	var out []string
	seen := make(map[string]bool)
	for _, a := range accounts {
		for _, g := range a.Groups {
			if seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

// accountGateText is what covers one account right now: the first live gate's
// kind and expiry, "rate-limited until 12:00" or "agents missing until
// cleared", or "" when the account is ready. Projected gates carry account keys
// for missing roles; ledger entries carry them for rate limits.
func accountGateText(a account.Account, gates []availability.Gate, live []availability.Entry) string {
	for _, e := range live {
		group, name, ok := account.ParseGateKey(e.Subject)
		if !ok || !slices.Contains(a.Groups, group) || (name != "" && name != a.Name) {
			continue
		}
		return availability.GateKindText(e.Kind) + " " + availability.GateUntilText(e.Until)
	}
	for _, g := range gates {
		group, name, ok := account.ParseGateKey(g.Token)
		if !ok || !slices.Contains(a.Groups, group) || (name != "" && name != a.Name) {
			continue
		}
		return availability.GateKindText(g.Kind) + " " + availability.GateUntilText(g.Until)
	}
	return ""
}
