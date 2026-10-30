package resolve

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// RoleRefusal is one role for which an omitted candidate would be refused
// right now, computed by the same resolveCandidate call bind makes.
type RoleRefusal struct {
	Role    string
	Text    string   // the words after "would refuse:" in relevo config
	NoOrder bool     // true for the ambiguous case, false for the all-gated case
	Serving []string // tokens serving the role, in Set.ForRole order
	Gated   []string // providers whose gate on a serving token gate --clear lifts (never a window or a cap), deduplicated, in Serving order; nil when NoOrder
	// Cause is set when every gate behind the refusal is a window or a cap,
	// so the fix is to wait, not to clear a gate.
	Cause RefusalCause
}

// RefusalCause says what kind of gate made a role's refusal.
type RefusalCause string

const (
	CauseGate   RefusalCause = ""
	CauseWindow RefusalCause = "window"
	CauseCap    RefusalCause = "cap"
)

// clearable reports whether gate --clear lifts a gate of kind: a window opens,
// a cap frees, and an allowance resets on their own, and clearing any of them
// does nothing.
func clearable(kind availability.Kind) bool {
	return kind != availability.Scheduled && kind != availability.ConcurrencyLimit && kind != availability.AllowanceExhausted
}

// refusalCause is CauseWindow or CauseCap when every skip on a serving token
// is a window or a cap (a window wins when both occur), else CauseGate.
func refusalCause(serving []candidate.Candidate, gates []availability.Gate) RefusalCause {
	cause, seen := CauseCap, false
	for _, c := range serving {
		for _, sk := range SkipsFor(SkipGates(gates), c.Ref().String()) {
			switch sk.Kind {
			case availability.Scheduled:
				cause = CauseWindow
			case availability.ConcurrencyLimit, availability.AllowanceExhausted:
			default:
				return CauseGate
			}
			seen = true
		}
	}
	if !seen {
		return CauseGate
	}
	return cause
}

// RoleRefusals lists, in harness.RoleNames() order, every role with at
// least one serving candidate that Resolve would refuse with no token
// named. It is the one source of doctor's policy rows and relevo
// policy's "would refuse" lines.
func RoleRefusals(set *candidate.Set, pol policy.Policy, gates []availability.Gate) []RoleRefusal {
	if set == nil || set.Len() == 0 {
		return nil
	}
	var out []RoleRefusal
	for _, role := range harness.RoleNames() {
		serving := set.ForRole(role)
		if len(serving) == 0 {
			continue
		}
		_, err := Resolve(set, pol, gates, "", role)
		// A roles-missing gate scoped to another role must not make this
		// role's refusal name a provider it never uses.
		if r, ok := RefusalFromErr(role, serving, GatesForRole(gates, role), err); ok {
			out = append(out, r)
		}
	}
	return out
}

// RefusalFromErr maps Resolve's error for role to a RoleRefusal.
// FormatPolicy and RoleRefusals both go through here, so relevo config and
// relevo doctor print the same words for the same state.
func RefusalFromErr(role string, serving []candidate.Candidate, gates []availability.Gate, err error) (RoleRefusal, bool) {
	tokens := make([]string, 0, len(serving))
	for _, c := range serving {
		tokens = append(tokens, c.Ref().String())
	}
	switch {
	case errors.Is(err, ErrAmbiguousCandidate):
		return RoleRefusal{
			Role:    role,
			Text:    fmt.Sprintf("%d candidates serve %s and no order is set", len(serving), role),
			NoOrder: true,
			Serving: tokens,
		}, true
	case errors.Is(err, ErrAllGated):
		var providers []string
		for _, c := range serving {
			for _, sk := range SkipsFor(SkipGates(gates), c.Ref().String()) {
				if clearable(sk.Kind) {
					providers = append(providers, c.Ref().Provider)
				}
			}
		}
		return RoleRefusal{
			Role:    role,
			Text:    fmt.Sprintf("every candidate serving %s is gated", role),
			Serving: tokens,
			Gated:   UniqStrings(providers),
			Cause:   refusalCause(serving, gates),
		}, true
	}
	return RoleRefusal{}, false
}
