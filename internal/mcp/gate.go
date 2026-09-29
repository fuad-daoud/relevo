package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// gateDoc is the gate tool's document. Its keys are exactly the CLI's GateDoc
// keys (`relevo gate <token> --json`), so the tool and the CLI agree; the shape
// is duplicated rather than imported because GateDoc is in package main, which
// internal/mcp cannot import.
type gateDoc struct {
	Subject    string `json:"subject"`
	Until      string `json:"until"`
	Candidates int    `json:"candidates"`
	Mode       string `json:"mode"`
	Removed    *int   `json:"removed,omitempty"`
}

// gateSetDocOf is the gating document: until is RFC3339, or "" when the gate
// holds until cleared.
func gateSetDocOf(provider string, until time.Time, candidates int) gateDoc {
	doc := gateDoc{Subject: provider, Candidates: candidates, Mode: "gated"}
	if !until.IsZero() {
		doc.Until = until.UTC().Format(time.RFC3339)
	}
	return doc
}

// gateClearDocOf is the clearing document: gateSetDocOf's shape plus removed,
// which counts the entries the clear lifted. removed is a pointer so a real
// zero still prints.
func gateClearDocOf(provider string, candidates, removed int) gateDoc {
	return gateDoc{Subject: provider, Candidates: candidates, Mode: "gated", Removed: &removed}
}

// parseGateFor turns the tool's `for` into an absolute expiry, as `relevo gate
// --for` does: empty means "until cleared" (a zero time), anything else must be
// a positive Go duration.
func parseGateFor(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("for %q: %w", s, err)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("for must be a positive duration, got %q", s)
	}
	return now.Add(d), nil
}

// providerCandidateCount counts the configured candidates a provider serves:
// the number the gate document carries.
func providerCandidateCount(rt relevo.Runtime, provider string) int {
	if rt.Candidates == nil {
		return 0
	}
	count := 0
	for _, ref := range rt.Candidates.Refs() {
		parsed, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		if parsed.Provider == provider {
			count++
		}
	}
	return count
}

// Gate records a provider rate limit for a binding's provider, or clears it.
// The ledger call is the record; the forward call tells every remote server the
// bindings name, exactly as the CLI's gate does, and its notice lines are
// dropped -- the tool's result is one JSON document, not prose.
func (v *RelevoVerbs) Gate(ctx context.Context, _ string, a GateArgs) (any, error) {
	if a.Clear {
		provider, removed, err := availability.Available(relevo.AvailabilityDeps(v.RT), a.Token, availability.ClearedByMasterMind)
		if err != nil {
			return nil, err
		}
		_ = relevo.ForwardAvailable(ctx, v.RT, a.Token)
		return gateClearDocOf(provider, providerCandidateCount(v.RT, provider), removed), nil
	}

	now := v.RT.Now
	if now == nil {
		now = time.Now
	}
	until, err := parseGateFor(a.For, now())
	if err != nil {
		return nil, err
	}

	provider, err := availability.Unavailable(relevo.AvailabilityDeps(v.RT), a.Token, until, a.Reason)
	if err != nil {
		return nil, err
	}
	_ = relevo.ForwardUnavailable(ctx, v.RT, a.Token, a.Reason)
	return gateSetDocOf(provider, until, providerCandidateCount(v.RT, provider)), nil
}
