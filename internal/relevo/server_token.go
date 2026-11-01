package relevo

import (
	"context"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// serverCandidate maps a client candidate -- its token, and its name when this
// machine knows one -- to the token a server resolves it by, from that server's
// own /v1/candidates list: the same set its handlers resolve against. Rules, in
// order: the same token; a candidate on the same provider, the same model
// first (a rate-limit gate is per provider); a candidate the server names the
// same; a view the server sent no name for with the same harness and model, a
// provider renamed between the two machines. Pure.
func serverCandidate(views []remote.CandidateView, token, name string) (string, bool) {
	if srv, ok := matchSameToken(views, token); ok {
		return srv, true
	}
	ref, rerr := candidate.ParseRef(token)
	if rerr == nil {
		if srv, ok := matchSameProvider(views, ref); ok {
			return srv, true
		}
	}
	if name != "" {
		if srv, ok := matchSameName(views, name); ok {
			return srv, true
		}
	}
	if rerr == nil {
		if srv, ok := matchUnnamedHarnessModel(views, ref); ok {
			return srv, true
		}
	}
	return "", false
}

func matchSameToken(views []remote.CandidateView, token string) (string, bool) {
	for _, v := range views {
		if v.Token == token {
			return v.Token, true
		}
	}
	return "", false
}

func matchSameProvider(views []remote.CandidateView, ref candidate.Ref) (string, bool) {
	for _, v := range views {
		vref, err := candidate.ParseRef(v.Token)
		if err != nil {
			continue
		}
		if vref.Provider == ref.Provider && vref.Model == ref.Model {
			return v.Token, true
		}
	}
	for _, v := range views {
		vref, err := candidate.ParseRef(v.Token)
		if err != nil {
			continue
		}
		if vref.Provider == ref.Provider {
			return v.Token, true
		}
	}
	return "", false
}

func matchSameName(views []remote.CandidateView, name string) (string, bool) {
	for _, v := range views {
		if v.Name == name {
			return v.Token, true
		}
	}
	return "", false
}

func matchUnnamedHarnessModel(views []remote.CandidateView, ref candidate.Ref) (string, bool) {
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
	return "", false
}

// ServerTokenFor fetches server's candidate list and maps token to the token
// that server resolves it by (serverCandidate's rules). err is set when the list
// itself could not be read; srv is "" then. A readable list with no match
// leaves srv "" and fills miss with the one line to report instead of posting.
func ServerTokenFor(ctx context.Context, rt Runtime, server, token, name string) (srv, miss string, err error) {
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

// matchCandidateView returns the server view v names, by its canonical Token
// or by its Name. Pure, so `addRemote`'s name matching is testable without a
// server.
func matchCandidateView(views []remote.CandidateView, v string) (remote.CandidateView, bool) {
	for _, view := range views {
		if view.Token == v || (view.Name != "" && view.Name == v) {
			return view, true
		}
	}
	return remote.CandidateView{}, false
}
