package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ResumeOptions is what one `relevo chain --resume` was asked for: the chain to
// continue and the settings the flags override. A flag that was not given
// keeps the chain's stored setting, so a nil pointer and an empty actor name
// both read as "not given", the same rule bind's own resume uses.
type ResumeOptions struct {
	// Name is the chain to resume. A missing chain is refused.
	Name string
	// MaxCorrections overrides the stored chain.max_corrections; nil keeps it.
	MaxCorrections *int
	// ReviewerActor, PlannerActor and SecurityActor override the stored actor
	// each part runs; "" keeps it.
	ReviewerActor, PlannerActor, SecurityActor string
	// Security overrides the stored chain.security; nil keeps it. Turning it on
	// for a chain that was started without the phase creates the security
	// member now.
	Security *bool
	// Gate is the builder member's new acceptance command; "" keeps the stored
	// one unless NoGate is set. NoGate clears it, winning over Gate exactly as
	// bind's --no-gate does.
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

// ChainResume continues a chain a human has looked at: `relevo chain --resume
// --name <n>`. The flags override the chain's stored settings (a flag that was
// not given keeps its setting), and the chain re-enters the step its engine is
// on -- or the one --from names -- or reviews a builder round that closed after
// the chain last heard from it, which is a manual round sent while the chain
// was down.
//
// It refuses a missing chain, a chain that is still running, and a chain that is
// done; a halted or stopped chain is what it is for. A resume whose target
// member is remote has only staged its round: the unlocked step ships it now,
// outside the state lock.
func ChainResume(ctx context.Context, rt Runtime, opts ResumeOptions) (ChainResult, error) {
	c, err := rt.Store.Chain(opts.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ChainResult{}, fmt.Errorf("chain %s: %w", opts.Name, store.ErrNotFound)
	}
	if err != nil {
		return ChainResult{}, err
	}
	if chainOnServer(c) {
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

// resumeRefusal is the one refusal a resume makes on its own chain: running and
// done are both terminal for this verb, and the wording is the chain's own.
// Both are typed so the CLI maps them to conflicts -- a script can tell a
// settled chain from an internal failure -- exactly as the server's own
// answers are rebuilt.
func resumeRefusal(c db.ChainRow) error {
	switch chain.Status(c.Status) {
	case chain.StatusRunning:
		return fmt.Errorf("chain %s is running: %w", c.Name, ErrChainRunning)
	case chain.StatusDone:
		return fmt.Errorf("chain %s is done: %w", c.Name, ErrChainDone)
	}
	return nil
}

// resumeSettings applies the resume's flags to the chain's stored settings: a
// flag that was given replaces the stored value, one that was not keeps it. The
// settings come off the chain row, never from today's policy: the row is what
// the chain has been running under. The gate flags are the exception: an
// explicit --gate or --no-gate resolves against today's policy exactly as a
// start would, because the human is naming the command now.
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
		set.Gate = resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), "builder"))
	}
	if opts.Regate != nil {
		set.Regate = resolveRegate(opts.Regate, rt.Policy)
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

// resumeRemoteGateRefusal refuses a resume that changes the gate on a chain
// whose writer member is placed remotely: that binding's check is fixed where
// it runs, and relevo has no route that updates a served binding's gate, so the
// change would be silently ignored. A missing writer record is left to the
// resume's own failure, exactly as the base refusal left it.
func resumeRemoteGateRefusal(rt Runtime, c db.ChainRow) error {
	member := c.Builder
	if member == "" {
		if rows, err := rt.Store.ChainMembers(c.Name); err == nil && len(rows) > 0 {
			member = rows[0].Binding
		}
	}
	if member == "" {
		return nil
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		return nil
	}
	if !b.Builder.Remote() {
		return nil
	}
	return refuse("chain %s: the server has no route to change a served binding's gate yet; W4 adds it", c.Name)
}

// resumeOpenRoundRefusal refuses a resume whose target member's current round
// is still open: the round a resume would start would overwrite the one in
// flight, and the human must stop it first. It returns the send path's
// RoundOpenError, whose wording and CLI conflict mapping name `relevo stop
// <member>`. A member
// record that is missing or unreadable has no open round it can prove, and is
// left to the send's own failure.
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

// memberNewestClosedRound is a member's newest closed round: the highest round
// its log holds a report entry for, or 0 when the record is gone or no round
// has closed. It is the round a resume's seed points its artifacts at.
func memberNewestClosedRound(tx *store.Tx, name string) int {
	if name == "" {
		return 0
	}
	entries, err := tx.ReadLog(name)
	if err != nil {
		return 0
	}
	newest := 0
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round > newest {
			newest = e.Round
		}
	}
	return newest
}
