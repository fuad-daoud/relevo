package relevo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// clientCandidateToken resolves subject -- a candidate name or a canonical token
// -- to this machine's token and the candidate's short name. A subject this
// machine does not configure comes back unchanged, name "", ok false: a bare
// provider must still travel as itself.
func clientCandidateToken(rt Runtime, subject string) (token, name string, ok bool) {
	if rt.Candidates != nil {
		if c, err := rt.Candidates.Resolve(subject); err == nil {
			return c.Ref().String(), c.Name, true
		}
	}
	return subject, "", false
}

// serverCandidate maps a client candidate -- its token, and its name when this
// machine knows one -- to the token a server resolves it by, from that server's
// own /v1/candidates list: the same set its handlers resolve against. Rules, in
// order: the same token; a candidate on the same provider, the same model
// first (a rate-limit gate is per provider); a candidate the server names the
// same; a view the server sent no name for with the same harness and model, a
// provider renamed between the two machines. Pure.
func serverCandidate(views []remote.CandidateView, token, name string) (string, bool) {
	// Rule 1: exact token match.
	for _, v := range views {
		if v.Token == token {
			return v.Token, true
		}
	}

	ref, rerr := candidate.ParseRef(token)
	if rerr == nil {
		// Rule 2a: same provider and same model.
		for _, v := range views {
			vref, err := candidate.ParseRef(v.Token)
			if err != nil {
				continue
			}
			if vref.Provider == ref.Provider && vref.Model == ref.Model {
				return v.Token, true
			}
		}
		// Rule 2b: same provider, any model.
		for _, v := range views {
			vref, err := candidate.ParseRef(v.Token)
			if err != nil {
				continue
			}
			if vref.Provider == ref.Provider {
				return v.Token, true
			}
		}
	}

	// Rule 3: same name, under whatever provider the server gives it.
	if name != "" {
		for _, v := range views {
			if v.Name == name {
				return v.Token, true
			}
		}
	}

	// Rule 4: server predates Name; match by harness and model for a renamed provider.
	if rerr == nil {
		for _, v := range views {
			if v.Name != "" {
				continue
			}
			vref, err := candidate.ParseRef(v.Token)
			if err != nil {
				continue
			}
			if vref.Harness == ref.Harness && vref.Model == ref.Model {
				return v.Token, true
			}
		}
	}

	return "", false
}

// serverTokenFor fetches server's candidate list and maps token to the token
// that server resolves it by (serverCandidate's rules). err is set when the list
// itself could not be read; srv is "" then. A readable list with no match
// leaves srv "" and fills miss with the one line to report instead of posting.
func serverTokenFor(ctx context.Context, rt Runtime, server, token, name string) (srv, miss string, err error) {
	resp, err := rt.Remote.Candidates(ctx, server)
	if err != nil {
		return "", "", err
	}
	srv, ok := serverCandidate(resp.Candidates, token, name)
	if !ok {
		return "", mismatchLine(server, token, resp.Candidates), nil
	}
	return srv, "", nil
}

// mismatchLine is the one line a forward reports when a server lists no
// candidate the client's candidate maps to: the token, its provider, and what
// the server does have.
func mismatchLine(server, token string, views []remote.CandidateView) string {
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return fmt.Sprintf("%s: candidate %q not on the server (available: %s)", server, token, candidateOptions(views))
	}
	return fmt.Sprintf("%s: candidate %q (provider %s) not on the server (available: %s)", server, token, ref.Provider, candidateOptions(views))
}

// candidateOptions renders servers' candidates for that line: "name (token)"
// when the server names one, "token" otherwise, comma separated; "none" for an
// empty list.
func candidateOptions(views []remote.CandidateView) string {
	if len(views) == 0 {
		return "none"
	}
	parts := make([]string, len(views))
	for i, v := range views {
		if v.Name != "" {
			parts[i] = fmt.Sprintf("%s (%s)", v.Name, v.Token)
		} else {
			parts[i] = v.Token
		}
	}
	return strings.Join(parts, ", ")
}

// ForwardUnavailable tells every remote binding with an open round that its
// server-side candidate just hit a usage limit, so that server's own
// reconcile can switch or gate it exactly as a local daemon would (§4.6). It
// never fails the caller: a binding relevo could not reach is named in the
// returned lines instead, and `relevo gate <token>`'s local behaviour (the
// ledger gate) proceeds either way.
func ForwardUnavailable(ctx context.Context, rt Runtime, token, reason string) []string {
	if rt.Remote == nil {
		return nil
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return []string{fmt.Sprintf("list bindings: %v", err)}
	}

	// The argument may be a name; the forwarded token is this machine's
	// canonical token, and its name is what a server that names its candidates
	// can match it by.
	token, name, _ := clientCandidateToken(rt, token)

	var lines []string
	var open []store.Binding
	for _, b := range bindings {
		if !b.Builder.Remote() {
			continue
		}

		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: read log: %v", b.Name, err))
			continue
		}
		if HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
			!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport) {
			open = append(open, b)
		}
	}

	var servers []string
	seen := make(map[string]bool)
	for _, b := range open {
		if seen[b.Builder.Server] {
			continue
		}
		seen[b.Builder.Server] = true
		servers = append(servers, b.Builder.Server)
	}
	sort.Strings(servers)

	for _, server := range servers {
		srv, miss, err := serverTokenFor(ctx, rt, server, token, name)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: list candidates: %v", server, err))
			continue
		}
		if srv == "" {
			lines = append(lines, miss)
			continue
		}
		if srv != token {
			lines = append(lines, fmt.Sprintf("%s: gated %s for %s", server, srv, token))
		}
		for _, b := range open {
			if b.Builder.Server != server {
				continue
			}
			if err := rt.Remote.Unavailable(ctx, b.Builder.Server, b.Name, srv, reason); err != nil {
				lines = append(lines, fmt.Sprintf("%s: %s: %v", b.Name, b.Builder.Server, err))
			}
		}
	}

	return lines
}

// ForwardAvailable tells every server this client's bindings name that a
// rate-limit gate can be lifted, so the server-wide ledger stops gating a
// provider the local ledger just cleared. Every remote binding counts, in any
// state and whether or not its round is open: a gate matters most when nothing
// is running. One call and one answer line per distinct server, sorted, and it
// never fails the caller -- a server relevo could not reach is named in the
// returned lines instead, and `relevo gate --clear`'s local behaviour (the ledger
// clear) proceeds either way.
func ForwardAvailable(ctx context.Context, rt Runtime, subject string) []string {
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

	var lines []string
	for _, server := range servers {
		send := token
		if isCandidate {
			if srv, _, err := serverTokenFor(ctx, rt, server, token, name); err == nil && srv != "" {
				send = srv
			}
		}
		resp, err := rt.Remote.Available(ctx, server, send)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", server, err))
			continue
		}
		if resp.Removed == 0 {
			lines = append(lines, fmt.Sprintf("%s: nothing was gating %s", server, resp.Provider))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: cleared %s (%d entries)", server, resp.Provider, resp.Removed))
	}

	return lines
}

// ServerInUse names every binding that names server -- the pure rule behind
// `relevo config server rm`'s refusal (§4.7). A pure function over the
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

// matchCandidateView returns the server view v names, by its canonical Token
// or by its Name. Pure, so `addRemote`'s name matching is testable without a
// server (A1 §4.2).
func matchCandidateView(views []remote.CandidateView, v string) (remote.CandidateView, bool) {
	for _, view := range views {
		if view.Token == v || (view.Name != "" && view.Name == v) {
			return view, true
		}
	}
	return remote.CandidateView{}, false
}
