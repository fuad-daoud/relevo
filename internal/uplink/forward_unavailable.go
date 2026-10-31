package uplink

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// accountGateKey reports whether subject is a group@account gate key rather
// than a bare group, a candidate name or a candidate token. A candidate name
// and a provider never contain "@", so the separator is unambiguous.
func accountGateKey(subject string) bool {
	_, name, ok := account.ParseGateKey(subject)
	return ok && name != ""
}

// serverHasAccounts reports whether server advertises remote.FeatureAccounts.
// A WhoAmI that could not be read returns its error rather than a false, so
// the caller reports the failure instead of treating an unasked server as
// account-aware.
func serverHasAccounts(ctx context.Context, rt relevo.Runtime, server string) (bool, error) {
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return false, err
	}
	return slices.Contains(who.Features, remote.FeatureAccounts), nil
}

// accountRefusal is the one line a forward prints instead of sending an
// account key to a server that does not advertise accounts: the key is a
// subject that server cannot resolve, so it would be recorded as a provider
// it does not have.
func accountRefusal(server string) string {
	return fmt.Sprintf("%s: account gates unsupported; upgrade the server", server)
}

// staleBinding reports whether err is the server saying it no longer holds the
// binding the client still has: the binding was unbound there, or was never
// mirrored, while this client's copy still names that server. Forwarding a
// gate to it changes nothing, so a stale binding is skipped rather than
// reported one line at a time -- `relevo gate <token>` against a set of
// bindings the servers have since dropped would otherwise print one "not
// found" line per binding for a command whose result is identical either way.
func staleBinding(err error) bool {
	var httpErr *client.HTTPError
	return errors.As(err, &httpErr) && httpErr.Body.Code == remote.CodeNotFound
}

// staleLine is the single summary the forwards emit in place of one line per
// stale binding.
func staleLine(n int) string {
	return fmt.Sprintf("skipped %d binding(s) the server no longer has", n)
}

// forwardAccountKeys sends keys, each a group@account gate key, to every open
// binding on server. It checks the feature first and, when server does not
// advertise accounts, appends accountRefusal and reports false: the caller
// then forwards nothing to that server. One line names the keys sent. The
// second result counts the bindings the server no longer has, which the caller
// folds into one summary rather than a line each. A key that failed to reach
// the server is followed by one next: line gating that key there.
func forwardAccountKeys(ctx context.Context, rt relevo.Runtime, server string, keys []string, open []store.Binding, g gateSpec, lines *[]string) (bool, int) {
	ok, err := serverHasAccounts(ctx, rt, server)
	if err != nil {
		if unknownServer(err) {
			return false, 0
		}
		*lines = append(*lines, fmt.Sprintf("%s: read features: %v", server, err))
		for _, key := range keys {
			*lines = append(*lines, g.next(server, key))
		}
		return false, 0
	}
	if !ok {
		*lines = append(*lines, accountRefusal(server))
		return false, 0
	}

	*lines = append(*lines, fmt.Sprintf("%s: gated %s", server, strings.Join(keys, ", ")))
	var stale int
	failed := map[string]bool{}
	for _, b := range open {
		if b.Builder.Server != server {
			continue
		}
		for _, key := range keys {
			err := rt.Remote.Unavailable(ctx, b.Builder.Server, b.Name, key, g.reason)
			if err == nil {
				continue
			}
			if unknownServer(err) {
				return true, stale
			}
			if staleBinding(err) {
				stale++
				continue
			}
			*lines = append(*lines, fmt.Sprintf("%s: %s: %v", b.Name, b.Builder.Server, err))
			if !failed[key] {
				failed[key] = true
				*lines = append(*lines, g.next(server, key))
			}
		}
	}
	return true, stale
}

// subjectProvider is the provider a gate subject names: a canonical candidate
// token's provider, or the subject itself when it is a bare provider this
// machine configures. ok is false for anything else.
func subjectProvider(rt relevo.Runtime, subject string) (string, bool) {
	if ref, err := candidate.ParseRef(subject); err == nil {
		return ref.Provider, true
	}
	if rt.Candidates != nil && slices.Contains(rt.Candidates.Providers(), subject) {
		return subject, true
	}
	return "", false
}

// forwardAccountNames names the login of each live group@account key this
// host's ledger records for token's provider, in ledger order: the logins a
// `gate <token>` just gated locally, which the server's own rotation must gate
// too. Nil when token names no provider, when no gates store is configured,
// or when the ledger holds no account key for that provider -- exactly the
// hosts where the bare token must keep travelling.
func forwardAccountNames(rt relevo.Runtime, token string) []string {
	if rt.Gates == nil {
		return nil
	}
	provider, ok := subjectProvider(rt, token)
	if !ok {
		return nil
	}
	l, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		return nil
	}
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}

	seen := map[string]bool{}
	var names []string
	for _, key := range availability.LiveGateKeys(l, now) {
		group, name, ok := account.ParseGateKey(key)
		if !ok || group != provider || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// clientCandidateToken resolves subject -- a candidate name or a canonical token
// -- to this machine's token and the candidate's short name. A subject this
// machine does not configure comes back unchanged, name "", ok false: a bare
// provider must still travel as itself.
func clientCandidateToken(rt relevo.Runtime, subject string) (token, name string, ok bool) {
	if rt.Candidates != nil {
		if c, err := rt.Candidates.Resolve(subject); err == nil {
			return c.Ref().String(), c.Name, true
		}
	}
	return subject, "", false
}

// unavailableForward is one gate being forwarded: the subject as this machine
// knows it and what decides how each server receives it.
type unavailableForward struct {
	token     string
	name      string
	direct    bool
	provider  bool
	acctNames []string
	open      []store.Binding
	g         gateSpec
}

// openRemoteBindings returns the remote bindings with an open round, appending
// one line per log that could not be read.
func openRemoteBindings(rt relevo.Runtime, bindings []store.Binding, lines *[]string) []store.Binding {
	var open []store.Binding
	for _, b := range bindings {
		if !b.Builder.Remote() {
			continue
		}

		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			*lines = append(*lines, fmt.Sprintf("%s: read log: %v", b.Name, err))
			continue
		}
		if store.RoundOpen(entries, b.Round) {
			open = append(open, b)
		}
	}
	return open
}

// bindingServers is the sorted set of servers bindings name.
func bindingServers(bindings []store.Binding) []string {
	var servers []string
	seen := make(map[string]bool)
	for _, b := range bindings {
		if seen[b.Builder.Server] {
			continue
		}
		seen[b.Builder.Server] = true
		servers = append(servers, b.Builder.Server)
	}
	sort.Strings(servers)
	return servers
}

// ForwardUnavailable tells every remote binding with an open round that its
// server-side candidate just hit a usage limit, so that server's own
// reconcile can switch or gate it exactly as a local daemon would. It
// never fails the caller: a binding relevo could not reach is named in the
// returned lines instead, followed by the command that gates the same subject
// on that server by hand, and `relevo gate <token>`'s local behaviour (the
// ledger gate) proceeds either way. until is the expiry the local gate
// recorded, zero for until cleared.
func ForwardUnavailable(ctx context.Context, rt relevo.Runtime, token, reason string, until time.Time) []string {
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
	token, name, isCandidate := clientCandidateToken(rt, token)

	var lines []string
	open := openRemoteBindings(rt, bindings, &lines)

	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}

	// An account gate key the caller already resolved travels as itself; a
	// candidate token's or provider's logins are read from the live ledger, so
	// a `gate <token>` forwards the same group@account keys it gated locally.
	// Both are empty on a host with no accounts, where the bare server token
	// below stands unchanged. A bare provider this machine configures travels
	// as itself: the server gates it provider-wide.
	_, known := subjectProvider(rt, token)
	f := unavailableForward{
		token:     token,
		name:      name,
		direct:    accountGateKey(token),
		provider:  !isCandidate && !accountGateKey(token) && known,
		acctNames: forwardAccountNames(rt, token),
		open:      open,
		g:         gateSpec{reason: reason, until: until, now: now},
	}

	var stale int
	for _, server := range bindingServers(open) {
		stale += forwardUnavailableTo(ctx, rt, server, f, &lines)
	}

	// One summary for every stale binding across every server, never one line
	// each.
	if stale > 0 {
		lines = append(lines, staleLine(stale))
	}

	return lines
}

// forwardUnavailableTo forwards f to one server and returns how many of its
// bindings the server no longer holds.
func forwardUnavailableTo(ctx context.Context, rt relevo.Runtime, server string, f unavailableForward, lines *[]string) int {
	if f.direct {
		_, n := forwardAccountKeys(ctx, rt, server, []string{f.token}, f.open, f.g, lines)
		return n
	}

	srv := f.token
	if !f.provider {
		mapped, miss, err := relevo.ServerTokenFor(ctx, rt, server, f.token, f.name)
		if err != nil {
			if !unknownServer(err) {
				*lines = append(*lines, fmt.Sprintf("%s: list candidates: %v", server, err), f.g.next(server, f.token))
			}
			return 0
		}
		if mapped == "" {
			*lines = append(*lines, miss, f.g.next(server, f.token))
			return 0
		}
		srv = mapped
	}

	if len(f.acctNames) > 0 {
		if provider, ok := subjectProvider(rt, srv); ok {
			keys := make([]string, len(f.acctNames))
			for i, n := range f.acctNames {
				keys[i] = account.GateKey(provider, n)
			}
			_, n := forwardAccountKeys(ctx, rt, server, keys, f.open, f.g, lines)
			return n
		}
	}
	if srv != f.token {
		*lines = append(*lines, fmt.Sprintf("%s: gated %s for %s", server, srv, f.token))
	}
	return postUnavailable(ctx, rt, server, srv, f, lines)
}

// postUnavailable posts subject to every open binding on server. One failure
// line per binding, and one next: line for the server if any failed.
func postUnavailable(ctx context.Context, rt relevo.Runtime, server, subject string, f unavailableForward, lines *[]string) int {
	var stale int
	var failed bool
	for _, b := range f.open {
		if b.Builder.Server != server {
			continue
		}
		err := rt.Remote.Unavailable(ctx, b.Builder.Server, b.Name, subject, f.g.reason)
		if err == nil {
			continue
		}
		if unknownServer(err) {
			return stale
		}
		if staleBinding(err) {
			stale++
			continue
		}
		failed = true
		*lines = append(*lines, fmt.Sprintf("%s: %s: %v", b.Name, b.Builder.Server, err))
	}
	if failed {
		*lines = append(*lines, f.g.next(server, subject))
	}
	return stale
}
