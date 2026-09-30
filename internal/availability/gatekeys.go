package availability

import (
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
)

// BindingsOnProvider names the active bindings with an open round whose builder
// runs on provider, sorted: the ones the daemon will switch once that provider
// is gated. Pure, for cmdUnavailable's note.
func BindingsOnProvider(bindings []store.Binding, provider string) []string {
	var names []string
	for _, b := range bindings {
		if b.State != store.StateActive {
			continue
		}
		if b.RoundStartedAt.IsZero() {
			continue
		}
		if b.BuilderCandidate == "" {
			continue
		}
		ref, err := candidate.ParseRef(b.BuilderCandidate)
		if err != nil || ref.Provider != provider {
			continue
		}
		names = append(names, b.Name)
	}
	sort.Strings(names)
	return names
}

// LiveGateKeys returns the subjects of the live rate-limit entries, pruned at
// now: the bare groups and group@account keys the pick and `gate <token>` read.
func LiveGateKeys(l Ledger, now time.Time) []string {
	var keys []string
	for _, e := range l.Prune(now).Entries {
		if e.Kind == RateLimited {
			keys = append(keys, e.Subject)
		}
	}
	return keys
}

// GateAccountsFor names the accounts a `gate <token>` records, in pool order:
// the account every open round on token draws from, and, with no open round,
// the account the pick would use now. Nil when no account serves token, so the
// caller records the bare group exactly as before. Pure: gateKeys is the live
// ledger's rate-limit subjects.
func GateAccountsFor(token string, bindings []store.Binding, accounts account.Set, gateKeys []string, mode account.Mode) []string {
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return nil
	}
	pool := accounts.Pool(account.Kind(ref.Harness), ref.Provider)
	if len(pool) == 0 {
		return nil
	}

	found := map[string]bool{}
	selectAccount := func() {
		a, ok, err := account.Select(pool, gateKeys, mode)
		if err == nil && ok {
			found[a.Name] = true
		}
	}

	open := 0
	for _, b := range bindings {
		if b.State != store.StateActive || b.RoundStartedAt.IsZero() || b.BuilderCandidate != token {
			continue
		}
		open++
		selectAccount()
	}
	if open == 0 {
		selectAccount()
	}

	var names []string
	for _, a := range pool {
		if found[a.Name] {
			names = append(names, a.Name)
		}
	}
	return names
}
