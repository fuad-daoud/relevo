package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ResumeOptions is what one `relevo chain --resume` was asked for: the chain
// and the overriding flag settings. An ungiven flag keeps the stored setting,
// so a nil pointer and an empty actor name both read as "not given", the same
// rule bind's own resume uses.
type ResumeOptions struct {
	// Name is the chain to resume. A missing chain is refused.
	Name string
	// MaxCorrections overrides the stored chain.max_corrections; nil keeps it.
	MaxCorrections *int
	// ReviewerActor, PlannerActor and SecurityActor override the stored actor
	// each part runs; "" keeps it.
	ReviewerActor, PlannerActor, SecurityActor string
	// Security overrides the stored chain.security; nil keeps it, and turning
	// it on for a chain started without the phase creates the member now.
	Security *bool
	// Gate is the builder's new acceptance command; "" keeps the stored one
	// unless NoGate clears it, winning over Gate as bind's --no-gate does.
	Gate   string
	NoGate bool
	// Regate is the builder member's new repair-round budget after a failing
	// gate; nil keeps the stored one.
	Regate *int
	// From re-enters the chain at this step instead of the one it halted on.
	From string
	// Params overrides the workflow's params, keyed by param name.
	Params map[string]string
}

// ChainResume continues a chain a human has looked at. The flags override the
// stored settings, and the chain re-enters its engine step -- or the one
// --from names -- or reviews a builder round that closed while the chain was
// down, a manual round sent in the meantime.
//
// It refuses a missing, running, or done chain; a halted or stopped chain is
// what it is for. A resume targeting a remote member has only staged its
// round: the unlocked step ships it now, outside the state lock.
func ChainResume(ctx context.Context, rt Runtime, opts ResumeOptions) (ChainResult, error) {
	c, err := rt.Store.Chain(opts.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ChainResult{}, fmt.Errorf("chain %s: %w", opts.Name, store.ErrNotFound)
	}
	if err != nil {
		return ChainResult{}, err
	}
	if chain.OnServer(c) {
		return chainServerResume(ctx, rt, c, opts)
	}
	out, err := chainResumeWorkflow(ctx, rt, c, opts)
	if err != nil {
		return ChainResult{}, err
	}
	if err := shipResumedRound(ctx, rt, opts.Name, out.Chain); err != nil {
		return ChainResult{}, err
	}
	return out, nil
}

// resumeRefusal is the one refusal a resume makes on its own chain: running
// and done are both terminal for this verb. Both are typed so the CLI maps
// them to conflicts -- a script tells a settled chain from an internal
// failure -- exactly as the server's own answers are rebuilt.
func resumeRefusal(c db.ChainRow) error {
	switch chain.Status(c.Status) {
	case chain.StatusRunning:
		return fmt.Errorf("chain %s is running: %w", c.Name, ErrChainRunning)
	case chain.StatusDone:
		return fmt.Errorf("chain %s is done: %w", c.Name, ErrChainDone)
	}
	return nil
}

// resumeSettings applies the resume's flags to the stored settings: a given
// flag replaces the stored value, an ungiven one keeps it. The settings come
// off the chain row, never today's policy: the row is what the chain has run
// under. The gate flags are the exception: an explicit --gate or --no-gate
// resolves against today's policy as a start would, because the human names
// the command now.
func resumeSettings(rt Runtime, c db.ChainRow, opts ResumeOptions) (chain.Settings, error) {
	var set chain.Settings
	if len(c.SettingsJSON) > 0 {
		if err := json.Unmarshal(c.SettingsJSON, &set); err != nil {
			return chain.Settings{}, fmt.Errorf("chain %s settings: %w", c.Name, err)
		}
	}
	if opts.MaxCorrections != nil {
		set.MaxCorrections = *opts.MaxCorrections
	}
	if opts.Gate != "" || opts.NoGate {
		// The gate flags are the one tier read fresh, never falling back: the
		// flag wins over every lower tier, so no repository's settings are
		// read here.
		set.Gate = resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, resolve.RoleChecks(rt.RoleRegistry(), "builder"), RepoSettings{}).Command
	}
	if opts.Regate != nil {
		set.Regate = ResolveRegate(opts.Regate, rt.Policy)
	}
	if opts.ReviewerActor != "" {
		set.ReviewerActor = opts.ReviewerActor
	}
	if opts.PlannerActor != "" {
		set.PlannerActor = opts.PlannerActor
	}
	if opts.SecurityActor != "" {
		set.SecurityActor = opts.SecurityActor
	}
	if opts.Security != nil {
		set.Security = *opts.Security
	}
	return set, nil
}

// chainResumeChangesGate reports whether a resume would replace the chain's
// gate: by an explicit --gate/--no-gate, by a --param gate=..., or by the wire's
// gate field, which fills the same two slots.
func chainResumeChangesGate(opts ResumeOptions) bool {
	if opts.Gate != "" || opts.NoGate {
		return true
	}
	_, ok := opts.Params["gate"]
	return ok
}

// resumeRemoteGateRefusal refuses a resume's gate change on a remotely placed
// writer when the server cannot take it. That binding runs its own check on
// the server, so a client-written gate would be silently ignored there: with
// the check feature the change goes through the gate route, without it the
// change cannot be delivered at all, and the refusal names the server and the
// feature it lacks. A missing writer record is left to the resume's own
// failure.
//
// This is the feature probe alone: the delivering route call waits for every
// other refusal to clear, so a resume let through here cannot leave the server
// ahead of a resume that then aborts.
func resumeRemoteGateRefusal(ctx context.Context, rt Runtime, c db.ChainRow) error {
	member := c.Builder
	if member == "" {
		if rows, err := rt.Store.ChainMembers(c.Name); err == nil && len(rows) > 0 {
			member = rows[0].Binding
		}
	}
	if member == "" || rt.Remote == nil {
		return nil
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		return nil
	}
	if !b.Builder.Remote() {
		return nil
	}
	who, err := rt.Remote.WhoAmI(ctx, b.Builder.Server)
	if err != nil {
		return err
	}
	if !remote.HasFeature(who, remote.FeatureCheck) {
		return refusal.New("chain %s: server %s lacks the %q feature, so a served binding's gate cannot be changed", c.Name, b.Builder.Server, remote.FeatureCheck)
	}
	return nil
}

// resumeRemoteGatePush delivers a resume's gate change to the server running
// a placed writer, through the gate route: the binding's new gate and, when
// also changed, its repair budget. It resolves the same writer member the
// refusal probed, and is a no-op for a binding on this machine.
//
// The call sits after the commit and every other refusal, because it is the
// one part of the resume the server cannot take back: an earlier push would
// leave the server checking under a gate the chain's settings never took. A
// binding with no gate has nothing for the route to update, so the check
// travelling in the check request is the whole answer.
func resumeRemoteGatePush(ctx context.Context, rt Runtime, c db.ChainRow, opts ResumeOptions) error {
	member := c.Builder
	if member == "" {
		if rows, err := rt.Store.ChainMembers(c.Name); err == nil && len(rows) > 0 {
			member = rows[0].Binding
		}
	}
	if member == "" || rt.Remote == nil {
		return nil
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		return nil
	}
	if !b.Builder.Remote() || b.Gate == "" {
		return nil
	}
	set, err := resumeSettings(rt, c, opts)
	if err != nil {
		return err
	}
	gate := set.Gate
	req := remote.SetGateRequest{Gate: &gate}
	if set.Regate != b.Regate {
		regate := set.Regate
		req.Regate = &regate
	}
	return rt.Remote.SetGate(ctx, b.Builder.Server, b.Name, req)
}

// resumeOpenRoundRefusal refuses a resume whose target member's current round
// is still open: the started round would overwrite the one in flight, so the
// human must stop it first. It returns the send path's RoundOpenError, naming
// `relevo stop <member>`. A missing or unreadable member record proves no open
// round, and is left to the send's own failure.
func resumeOpenRoundRefusal(tx *store.Tx, c db.ChainRow, memberName string) error {
	member, err := tx.Load(memberName)
	if err != nil {
		return nil
	}
	entries, err := tx.ReadLog(memberName)
	if err != nil {
		return nil
	}
	if roundOpenIn(entries, member.Round) {
		return &RoundOpenError{Member: memberName, Round: member.Round}
	}
	return nil
}
