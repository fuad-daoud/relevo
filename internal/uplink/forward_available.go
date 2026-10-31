package uplink

import (
	"context"
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ForwardAvailable tells every server this client's bindings name that a
// rate-limit gate can be lifted, so the server-wide ledger stops gating a
// provider the local ledger just cleared. Every remote binding counts, in any
// state and whether or not its round is open: a gate matters most when nothing
// is running. One call and one answer line per distinct server, sorted, and it
// never fails the caller -- a server relevo could not reach is named in the
// returned lines instead, and `relevo gate --clear`'s local behaviour (the ledger
// clear) proceeds either way.
func ForwardAvailable(ctx context.Context, rt relevo.Runtime, subject string) []string {
	if rt.Remote == nil {
		return nil
	}

	// A candidate name or token is forwarded as the token the server resolves
	// it by, so the server's own ledger clears the same provider. A bare
	// provider -- this machine's candidates do not hold it -- travels as
	// itself, and the server owns the decision to clear or refuse.
	token, name, isCandidate := clientCandidateToken(rt, subject)

	bindings, err := rt.Store.List()
	if err != nil {
		return []string{fmt.Sprintf("list bindings: %v", err)}
	}

	servers := availableServers(bindings)

	var lines []string
	var stale int
	for _, server := range servers {
		stale += forwardAvailableTo(ctx, rt, server, token, name, isCandidate, &lines)
	}

	if stale > 0 {
		lines = append(lines, staleLine(stale))
	}

	return lines
}

// availableServers is the sorted set of servers any remote binding names, in
// any state and whether or not its round is open: a clear matters most when
// nothing is running.
func availableServers(bindings []store.Binding) []string {
	var servers []string
	seen := make(map[string]bool)
	for _, b := range bindings {
		if !b.Builder.Remote() || seen[b.Builder.Server] {
			continue
		}
		seen[b.Builder.Server] = true
		servers = append(servers, b.Builder.Server)
	}
	sort.Strings(servers)
	return servers
}

// forwardAvailableTo clears subject on one server. It returns 1 when the
// server holds no such subject, which the caller folds into the single
// summary rather than a line per server.
func forwardAvailableTo(ctx context.Context, rt relevo.Runtime, server, token, name string, isCandidate bool, lines *[]string) int {
	// An account clear travels only to a server that understands the key;
	// a server without the feature would refuse the subject it cannot
	// resolve, so the client refuses it first and names the upgrade.
	if accountGateKey(token) {
		ok, ferr := serverHasAccounts(ctx, rt, server)
		if ferr != nil {
			if unknownServer(ferr) {
				return 0
			}
			*lines = append(*lines, fmt.Sprintf("%s: read features: %v", server, ferr))
			return 0
		}
		if !ok {
			*lines = append(*lines, accountRefusal(server))
			return 0
		}
	}
	send := token
	if isCandidate {
		if srv, _, err := relevo.ServerTokenFor(ctx, rt, server, token, name); err == nil && srv != "" {
			send = srv
		}
	}
	resp, err := rt.Remote.Available(ctx, server, send)
	if err != nil {
		if unknownServer(err) {
			return 0
		}
		if staleBinding(err) {
			// The server holds no such subject: nothing was gating it
			// there. One count, folded into the single summary.
			return 1
		}
		*lines = append(*lines, fmt.Sprintf("%s: %v", server, err))
		return 0
	}
	if resp.Removed == 0 {
		*lines = append(*lines, fmt.Sprintf("%s: nothing was gating %s", server, resp.Provider))
		return 0
	}
	*lines = append(*lines, fmt.Sprintf("%s: cleared %s (%d entries)", server, resp.Provider, resp.Removed))
	return 0
}

// ServerInUse names every binding that names server -- the pure rule behind
// `relevo config server rm`'s refusal. A pure function over the
// binding list rather than a store read, so the CLI (cmd/relevo) can be
// tested without touching the network -- the caller loads the bindings and
// this function decides.
func ServerInUse(bindings []store.Binding, server string) []string {
	var names []string
	for _, b := range bindings {
		if b.Builder.Remote() && b.Builder.Server == server {
			names = append(names, b.Name)
		}
	}
	return names
}
