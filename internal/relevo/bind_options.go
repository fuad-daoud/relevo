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
	// Candidate is a harness/provider/model token; empty resolves by role,
	// except in resume, where empty means "not rebinding".
	Candidate string
	// MasterMindID is the caller's --mastermind value when it has one, else the
	// resolved record's id. Empty means "resolve this session's mastermind".
	MasterMindID string
	CWD          string
	Resume       bool

	// Rebind, with Resume, replaces a gone builder as a fresh bind with
	// Candidate empty would. Without it, an empty Candidate on resume means
	// "mastermind-only: touch no builder". Ignored when Candidate is set.
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

	// Regate is the binding's automatic repair-round budget: nil falls back
	// to policy.json's gate.regate; an explicit 0 disables repair even when
	// the policy sets one.
	Regate *int

	// Feature is the human-given label grouping this binding with others;
	// "" means ungrouped, validated by store.ValidFeature when set. On
	// resume, empty leaves the stored Feature untouched.
	Feature string

	// NoFeature is the explicit "this is not a feature" mark. A fresh bind
	// must name exactly one of Feature/NoFeature; on resume, NoFeature
	// clears the stored feature.
	NoFeature bool

	// Ticket is the issue this binding serves in stored form; empty means
	// none. On resume, empty leaves the stored ticket untouched: there is no
	// way to clear one.
	Ticket string

	// Role is the writer role the new binding runs; "" means builder. Bind
	// ignores it on resume: the binding keeps its stored role.
	Role string

	// Local runs the builder here, whatever the actor's placement says. A
	// fresh bind with no placement list behaves the same with or without it.
	Local bool
}

// RequireFeatureChoice is the CLI's exactly-one rule for
// --feature/--no-feature. A fresh bind must name exactly one; a resume may
// name none but not both. It is pure, so the CLI refuses before any state,
// worktree or network work.
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

var errFeatureChoice = refusal.BadInputf("choose exactly one of --feature <label> or --no-feature")

// parseTicket turns a raw --ticket value into its stored form. An empty raw
// value parses to "" with no error, so "unset" and "keep" stay
// distinguishable.
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
