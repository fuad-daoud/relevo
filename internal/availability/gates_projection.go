package availability

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/account"
)

// LedgerGates projects the live ledger onto tokens, whether or not the
// configured set holds them: a rate limit gates every token of its provider, a
// spawn failure gates its own token.
func LedgerGates(d Deps, tokens []string, accounts ...account.Set) []Gate {
	if d.Gates == nil {
		return nil
	}

	l, err := LoadLedger(d.Gates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read ledger: %v\n", err)
		return nil
	}

	return LedgerGatesFrom(d, l, tokens, accounts...)
}

// LedgerGatesFrom is LedgerGates with the ledger already loaded: it decodes no
// kv row, so a report that needs the ledger for both the candidate gates and
// the unused-provider gates reads it once.
func LedgerGatesFrom(d Deps, l Ledger, tokens []string, accounts ...account.Set) []Gate {
	return Gated(l, tokens, ProviderOf, d.Now(), gateAccounts(d, accounts)...)
}

// gateAccounts picks the account set the projection reads: an explicit
// argument wins, otherwise the Deps carry the configured pool. Empty on a host
// with no accounts, where Gated takes no account set and every gate stays a
// bare group.
func gateAccounts(d Deps, accounts []account.Set) []account.Set {
	for _, a := range accounts {
		if len(a) > 0 {
			return []account.Set{a}
		}
	}
	if len(d.Accounts) == 0 {
		return nil
	}
	return []account.Set{d.Accounts}
}

// Gates is what every reader renders from: the live ledger projected onto the
// configured candidates. A load error is reported once on stderr and read as
// an empty ledger -- status, candidates and doctor must not go down over a
// bookkeeping file.
func Gates(d Deps, accounts ...account.Set) []Gate {
	if d.Candidates == nil {
		return nil
	}

	var l Ledger
	if d.Gates != nil {
		loaded, err := LoadLedger(d.Gates)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo: could not read ledger: %v\n", err)
		} else {
			l = loaded
		}
	}
	return GatesFrom(d, l, accounts...)
}

// GatesFrom is Gates with the ledger already loaded and decoded: it projects
// the candidate gates, appends the roles-missing gates and names every gate
// from the configured set, but reads no kv row. It is the seam a status report
// shares one load across Gates and UnusedProviderGates with.
func GatesFrom(d Deps, l Ledger, accounts ...account.Set) []Gate {
	if d.Candidates == nil {
		return nil
	}

	gates := LedgerGatesFrom(d, l, d.Candidates.Refs(), accounts...)
	gates = append(gates, rolesMissingGates(d)...)

	// Every gate carries the candidate's short name when the set holds its
	// token, so the gates block and `relevo serve gates` can print it. A token
	// no longer configured leaves Name empty and the renderers fall back to
	// printing the token. The token stays the gate's identity.
	for i := range gates {
		if name, ok := d.Candidates.NameFor(gates[i].Token); ok {
			gates[i].Name = name
		}
	}
	return gates
}
