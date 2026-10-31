package relevo

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/store"
)

// BindOptions describes one bind request. A local builder is always headless.
type BindOptions struct {
	Name string
	// Candidate is a harness/provider/model token; empty means resolve by role
	// through resolveCandidate, except in resume, where empty means "not rebinding".
	Candidate string
	// MasterMindID is the caller's --mastermind value when it has one, and the
	// resolved record's id afterwards: bind sets Binding.MasterMindID from it.
	// Empty means "resolve this session's mastermind".
	MasterMindID string
	CWD          string
	Resume       bool

	// Rebind, with Resume, replaces a builder that is gone by resolving a
	// candidate through policy.json order and the ledger, exactly as a
	// fresh bind with Candidate empty does. Without it, an empty
	// Candidate on resume means "mastermind-only: touch no builder". Ignored
	// when Candidate is set.
	Rebind bool

	// RoundTimeout overrides the binding's round budget. Zero keeps the
	// store's default.
	RoundTimeout time.Duration

	// Headless is accepted as a no-op: a local builder is always a process
	// relevo runs per round.
	Headless bool

	// Tier overrides the candidate/policy permission tier.
	Tier string

	// AllowYolo permits Tier == "yolo" above policy max_tier for this command.
	AllowYolo bool

	// Gate is the acceptance command relevo runs on the binding's completion
	// marker. Empty means fall back to policy.json's gate.default,
	// unless NoGate opts out of that default.
	Gate string

	// NoGate opts this binding out of policy.json's gate.default even when
	// Gate is empty. Ignored when Gate is set.
	NoGate bool

	// Regate is the binding's automatic repair-round budget:
	// how many repair rounds relevo may open after a failing gate. nil falls
	// back to policy.json's gate.regate; an explicit 0 disables repair even
	// when the policy sets one.
	Regate *int

	// Feature is the human-given label grouping this binding with others;
	// "" means ungrouped. Validated by store.ValidFeature when set.
	// On resume, an empty Feature means "leave the binding's existing
	// Feature untouched" rather than clearing it.
	Feature string

	// NoFeature is the explicit "this is not a feature" mark. A fresh
	// bind must name exactly one of Feature/NoFeature (RequireFeatureChoice);
	// on resume, NoFeature clears the binding's stored feature.
	NoFeature bool

	// Ticket is the issue this binding serves, as typed on --ticket:
	// a number, #N, project/path#N, an issue key like ABC-123, or an http(s)
	// URL, in the stored form store.ParseTicket returns. Empty means none. On
	// resume, empty leaves the binding's ticket untouched; there is no way to
	// clear one.
	Ticket string

	// Role is the writer role the new binding runs; "" means builder.
	// Bind ignores it on resume, because the binding keeps its stored role.
	Role string

	// Local runs the builder here, whatever the actor's placement says. It is
	// the --local flag; a fresh bind with no placement list behaves the same
	// whether it is set or not.
	Local bool
}

// RequireFeatureChoice is the CLI's exactly-one rule for --feature/--no-feature.
// A fresh bind must name exactly one; a resume may name none (the
// binding keeps its feature) but not both. It is a pure function of the flags,
// so the CLI can refuse before newRuntime and any state, worktree or network
// work, and the cockpit -- which has no --no-feature concept -- can keep
// creating unlabelled bindings without going through it.
func RequireFeatureChoice(feature string, noFeature, resume bool) error {
	hasFeature := feature != ""
	if resume {
		if hasFeature && noFeature {
			return errFeatureChoice
		}
		return nil
	}
	if hasFeature == noFeature {
		return errFeatureChoice
	}
	return nil
}

// errFeatureChoice is the one line RequireFeatureChoice returns.
var errFeatureChoice = refusal.BadInputf("choose exactly one of --feature <label> or --no-feature")

// parseTicket turns a raw --ticket value into its stored form, using
// ref's origin as the repository hint when the value names no repository. An
// empty raw value parses to "" with no error, so "unset" and "keep" stay
// distinguishable to the caller.
func parseTicket(raw string, ref *store.RepoRef) (string, error) {
	if raw == "" {
		return "", nil
	}
	hint := ""
	if ref != nil {
		hint = git.OwnerRepo(ref.OriginURL)
	}
	return store.ParseTicket(raw, hint)
}

// ResolveRegate applies the repair-round rule: an explicit
// --regate is used as given (0 disables repair), and an unset flag falls back
// to policy.json's gate.regate.
func ResolveRegate(regate *int, pol policy.Policy) int {
	if regate != nil {
		return *regate
	}
	return pol.GateRegate()
}
