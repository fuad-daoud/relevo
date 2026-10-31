package relevo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

func create(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint, placement resolve.PlacementResolution) (store.Binding, resolve.Resolution, error) {
	// The repository's own settings are read before anything is resolved: the
	// gate tier reads them, and a directory that is not a repository reads as
	// "no settings" rather than a failure.
	repoSet, err := RepoSettingsFor(ctx, rt, opts.CWD)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	shape, err := resolve.ActorShape(rt.RoleRegistry(), opts.Role)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	// A reader round has no check, so the writer-only knobs are refused at
	// bind, naming the flag.
	if shape == store.ShapeReader {
		if opts.Gate != "" {
			return store.Binding{}, resolve.Resolution{}, refusal.BadInputf("--gate: a reader round has no check")
		}
		if opts.Regate != nil {
			return store.Binding{}, resolve.Resolution{}, refusal.BadInputf("--regate: a reader round has no check")
		}
	}
	name := opts.Name
	if name == "" {
		name = SanitizeName(baseName(opts.CWD))
	}
	if err := store.ValidName(name); err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}

	// Refuse a name that is already taken, before anything is spawned. Save
	// would overwrite only the binding record: the round log and the NNN-*
	// files survive, so a fresh round 1 would collide with the previous
	// session's round 1 and Reconcile would read that old report entry as
	// "already handled" -- silently, with no error and no notification.
	if _, err := rt.Store.Load(name); err == nil {
		return store.Binding{}, resolve.Resolution{}, refusal.BadInputf(
			"binding %q already exists: `relevo unbind %s` to start fresh, or `relevo bind --resume --name %s` to adopt it",
			name, name, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Binding{}, resolve.Resolution{}, err
	}

	// Check the working tree before spawning anything. Save re-checks under the
	// lock and stays authoritative, but without this a refused bind would leave
	// a started builder pane stranded with nothing pointing at it.
	//
	// Only a writer can be blocked here: a reader may share a writer's
	// tree, so this refusal applies between two writers only.
	if shape == store.ShapeWriter {
		other, found, err := rt.Store.FindByCWD(opts.CWD)
		if err != nil && !errors.Is(err, store.ErrAmbiguousCWD) {
			return store.Binding{}, resolve.Resolution{}, err
		}
		if found && other.Name != name && other.State != store.StateDone && other.Shape == store.ShapeWriter && !rt.Store.SameChain(name, other.Name) {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
				opts.CWD, other.Name, other.BuilderCandidate, other.Round, store.ErrCWDTaken)
		}
	}

	var tier harness.Tier
	roleName := resolve.BindingRole(store.Binding{Role: resolve.NormRole(opts.Role)})
	resCandidate, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, resolve.WithSkillGates(ResolveDeps(rt), availability.Gates(AvailabilityDeps(rt)), opts.CWD, nil), opts.Candidate, roleName)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	tier = resolve.ResolveTier(opts.Tier, resCandidate.Candidate, rt.RoleRegistry(), roleName)
	if shape == store.ShapeReader {
		tier, err = resolve.ReaderTier(tier, resCandidate.Candidate.Harness, rt.Policy)
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}
	if err := resolve.CheckTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	opts.Tier = string(tier)

	builder, res, err := resolveBuilder(ctx, rt, nil, opts, name)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	// The placement rides on the resolution so the pick note says where the
	// binding landed and what it passed over.
	res.Placement = placement
	// The gate resolution rides on the resolution too, so the CLI can print
	// which tier supplied the command the binding stored.
	gateRes := resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, resolve.RoleChecks(rt.RoleRegistry(), roleName), repoSet)
	res.Gate = gateRes

	// The ticket is parsed against the binding's own captured origin, so
	// a typed number becomes owner/repo#N when relevo knows the repo.
	repoRef := captureRepo(ctx, rt, opts.CWD)
	ticket, err := parseTicket(opts.Ticket, repoRef)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}

	b := store.Binding{
		Name:             name,
		CWD:              opts.CWD,
		MasterMind:       mastermindEP,
		MasterMindID:     opts.MasterMindID,
		Builder:          builder,
		BuilderCandidate: res.Token(),
		BuilderAccount:   res.Account,
		BuilderPinned:    res.How == resolve.HowExplicit,
		Round:            1,
		State:            store.StateActive,
		Tier:             string(tier),
		Role:             resolve.NormRole(opts.Role),
		Shape:            shape,
		Gate:             gateRes.Command,
		Regate:           ResolveRegate(opts.Regate, rt.Policy),
		RepoRef:          repoRef,
		Feature:          opts.Feature,
		Ticket:           ticket,
	}
	if opts.RoundTimeout > 0 {
		b.RoundTimeoutMS = int(opts.RoundTimeout / time.Millisecond)
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		if res.How == "" {
			return nil
		}
		return tx.AppendLog(name, resolve.PickEntry(rt.Now(), 1, "builder", res))
	})
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, fmt.Errorf("bind failed: %w", err)
	}

	// Save fills in the defaults it owns -- round cap, round budget -- on its
	// own copy, so read back what was actually stored rather than returning
	// the pre-Save value and letting the two drift.
	stored, err := rt.Store.Load(b.Name)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}

	return stored, res, nil
}

// builderAgentName composes and validates the agent name for a binding's
// builder. It runs before any worktree exists, so a name relevo would refuse
// fails as a validation error with nothing to clean up.
func builderAgentName(name string) (string, error) {
	agentName := name + "-builder"
	if err := store.ValidName(agentName); err != nil {
		return "", fmt.Errorf("builder agent name %q: %w -- use a binding name of at most %d characters",
			agentName, err, store.MaxAgentNameLen-len("-builder"))
	}
	return agentName, nil
}

// resolveBuilder returns a headless endpoint for a local builder. Nothing is
// opened and nothing is started: the endpoint records the mode, the name and
// the kind, and Send fills in the process fields per round.
//
// Before returning, the candidate's launch is rendered once with
// headlessLaunch, so the permission-tier refusals (ErrTierUnsupported,
// ErrExtraArgsPermission) fire at bind/add/fork, before a worktree is kept.
//
// The second return is the resolution, for the pick line.
func resolveBuilder(ctx context.Context, rt Runtime, tx *store.Tx, opts BindOptions, name string) (store.Endpoint, resolve.Resolution, error) {
	roleName := resolve.BindingRole(store.Binding{Role: opts.Role})
	res, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, resolve.WithSkillGates(ResolveDeps(rt), availability.Gates(AvailabilityDeps(rt)), opts.CWD, nil), opts.Candidate, roleName, resolve.PickFor(ResolveDeps(rt)))
	if err != nil {
		return store.Endpoint{}, resolve.Resolution{}, err
	}
	c := res.Candidate
	agentName, err := builderAgentName(name)
	if err != nil {
		return store.Endpoint{}, resolve.Resolution{}, err
	}

	var role harness.RoleSpec
	role, err = rt.RoleRegistry().Spec(roleName, c.Harness)
	if err != nil {
		return store.Endpoint{}, resolve.Resolution{}, fmt.Errorf("binding %q builder: %w", name, err)
	}
	tier := harness.Tier(opts.Tier)
	if tier == "" {
		tier = harness.TierHarness
	}
	if _, err := candidate.HeadlessLaunch(c, role, tier, 0, "", opts.CWD, rt.Store.OutDir(name), "", ""); err != nil {
		return store.Endpoint{}, resolve.Resolution{}, err
	}

	return store.Endpoint{AgentName: agentName, Kind: c.Harness, Mode: store.ModeHeadless}, res, nil
}
