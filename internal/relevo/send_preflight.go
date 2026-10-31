package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// plannerAgent is the definition whose actor answers with a plan.
const plannerAgent = "architect"

// seedMaxBytes caps a planner actor's send: a planner's input is a seed; it
// reads the code itself.
const seedMaxBytes = 4 << 10

// ErrSeedOverCap is the sentinel a planner send returns when its seed is over
// seedMaxBytes and --force was not given, so the CLI classifies it as a usage
// refusal rather than an internal failure.
var ErrSeedOverCap = errors.New("planner seed over cap")

// pendingRoundFile returns the round's completion marker or report if either
// is already on disk: the round's work is finished and the daemon has not yet
// ingested the close, so a send into this round would race it. The done marker
// is checked first because the close keys on it. Any stat error, not-exist
// included, counts as absent: an unreadable directory must not block a send.
func pendingRoundFile(rt Runtime, name string, round int) (string, bool) {
	for _, path := range []string{rt.Store.DonePath(name, round), rt.Store.ReportPath(name, round)} {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

// readPromptFile reads the plan a caller named with --file. The path is their
// own argument, so a missing or unreadable one is a usage refusal naming the
// path -- refusal.ErrBadInput, which the CLI maps to exit 2 with `relevo help` -- rather
// than an internal failure with `relevo bugreport` for a path that never
// existed. It is the one read of a caller-named file on the send path: send and
// send --dry-run both come through here, so neither can drift from the other.
func readPromptFile(file string) ([]byte, error) {
	body, err := os.ReadFile(file)
	if err != nil {
		return nil, refusal.BadInputf("--file %s: %v", file, err)
	}
	return body, nil
}

// sendPreflight runs Send's read-only preconditions in Send's exact order and
// error wording. It makes no write: Store.Load takes the store lock briefly,
// but that is a read. CaptureBaseline is deliberately not here:
// it adds git objects. The remote path stops after the checks that need no
// server contact (no WhoAmI, no bundle); Send's remote branch calls sendRemote.
//
// Every --candidate guard here and under Send's lock is candidateSendRefused:
// the round-open refusal, except for a served binding halted in NEEDS YOU,
// which the candidate re-points.
func sendPreflight(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (preflight, error) {
	// Read the caller's file first; it is the one input that does not depend
	// on binding state.
	body, err := readPromptFile(file)
	if err != nil {
		return preflight{}, err
	}

	var tier harness.Tier
	tierFrom := "binding (fixed at bind; rebind to pick up config changes)"
	if opts.Tier != "" {
		t, err := harness.ParseTier(opts.Tier)
		if err != nil {
			return preflight{}, err
		}
		if err := resolve.CheckTierCap(t, rt.Policy, opts.AllowYolo); err != nil {
			return preflight{}, err
		}
		tier = t
		tierFrom = "round override (--tier)"
	}

	b, err := rt.Store.Load(name)
	if err != nil {
		return preflight{}, err
	}
	// A running chain owns its members' rounds: a manual send would race the
	// chain's own next send. Read-only, so a dry run refuses identically.
	if err := chain.RefuseRunningMemberStore(rt.Store, name); err != nil {
		return preflight{}, err
	}
	if b.State == store.StatePaused {
		return preflight{}, fmt.Errorf("binding %q is paused; relevo bind --resume --name %s first", name, name)
	}
	// A reader round has no check, so --verify, which only a writer's gate
	// round has, names its flag. It stands before any spawn.
	if b.Shape == store.ShapeReader && opts.Verify != nil {
		return preflight{}, errors.New("--verify: a reader round has no check")
	}

	// --candidate resolves the new candidate read-only and substitutes it in
	// memory, so every later precondition (the argv, the advisory gate note,
	// the dry run) is computed against the builder this round will actually
	// use. Nothing is saved here: Send re-applies the change under its lock.
	// A remote binding's candidates decide on the server, so only the token's
	// shape is checked here.
	var pick *resolve.Resolution
	var repickFrom, repickWhy string
	// remoteBuilder is what a remote binding's sendRemote is handed: the
	// canonical token when --candidate resolved to one, else the argument as
	// typed.
	remoteBuilder := opts.Builder
	if opts.Builder != "" {
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return preflight{}, err
		}
		if candidateSendRefused(b, entries) {
			return preflight{}, &CandidateRoundOpenError{Name: name, Round: b.Round}
		}
		if b.Builder.Remote() {
			// A remote binding's candidates decide on the server, so a
			// value with "/" is only shape-checked here. A value with no
			// "/" is a name, resolved locally to its canonical token: a
			// candidate only the server has must be named by its token.
			if strings.Contains(opts.Builder, "/") {
				if _, err := candidate.ParseRef(opts.Builder); err != nil {
					return preflight{}, fmt.Errorf("%w %s: %w", ErrBadBuilder, opts.Builder, err)
				}
			} else {
				c, err := rt.Candidates.Resolve(opts.Builder)
				if err != nil {
					return preflight{}, fmt.Errorf("%w %s: unknown candidate %q; a candidate only the server has must be named by its harness/provider/model token", ErrBadBuilder, opts.Builder, opts.Builder)
				}
				remoteBuilder = c.Ref().String()
			}
		} else {
			p, err := resolveSendBuilder(rt, resolve.BindingRole(b), b.BuilderCandidate, opts.Builder, b.CWD)
			if err != nil {
				return preflight{}, err
			}
			if p != nil {
				b2, err := applyBuilder(b, *p, rt.RoleRegistry(), rt.Policy, opts.AllowYolo)
				if err != nil {
					return preflight{}, err
				}
				b = b2
				pick = p
			}
		}
	}

	// A binding whose candidate was edited or deleted holds a stale token,
	// and a window edge between rounds moves a binding that was not pinned:
	// pick again by the actor's order, exactly as a switch does, so every
	// precondition below is computed against the builder this round will
	// actually use.
	if pick == nil {
		old := b.BuilderCandidate
		var res *resolve.Resolution
		var why string
		b, res, why, err = repickForRound(rt, b, opts.Builder != "", opts.AllowYolo)
		if err != nil {
			return preflight{}, err
		}
		if res != nil {
			pick, repickFrom, repickWhy = res, old, why
		}
	}

	if tier == "" {
		if b.Builder.Remote() {
			// A served round is launched by the server, which caps its tier
			// against the server's own policy; the client's max_tier must not
			// refuse the send.
			tier = resolve.EffectiveTier(b)
		} else {
			t, err := resolve.LaunchTier(b, rt.Policy, opts.AllowYolo)
			if err != nil {
				return preflight{}, err
			}
			tier = t
		}
	}

	// A planner actor answers with a plan, so its prompt is a seed: cap it
	// here, in the read-only preflight, so a --dry-run refuses identically
	// and a refusal writes nothing. --force is the explicit escape.
	if !opts.Force && len(body) > seedMaxBytes && actorRunsPlanner(rt, b) {
		return preflight{}, fmt.Errorf("binding %q: the seed is %d bytes; a planner actor takes at most %d bytes -- pass --force to send it anyway: %w", name, len(body), seedMaxBytes, ErrSeedOverCap)
	}

	promptPath := rt.Store.PromptPath(name, b.Round)
	reportPath := rt.Store.ReportPath(name, b.Round)
	donePath := rt.Store.DonePath(name, b.Round)
	prompt := composePrompt(rt, b, promptPath, reportPath, donePath)

	pf := preflight{
		b: b, body: body, tier: tier, tierFrom: tierFrom,
		promptPath: promptPath, reportPath: reportPath, donePath: donePath,
		prompt: prompt, pick: pick, repickFrom: repickFrom, repickWhy: repickWhy,
		remoteBuilder: remoteBuilder,
	}

	// A remote binding's read-only prefix: the client and transport must be
	// configured and the branch must resolve locally. Nothing here contacts
	// the server, so a dry run of a remote binding is offline and safe.
	if b.Builder.Remote() {
		if rt.Remote == nil {
			return preflight{}, ErrRemoteUnavailable
		}
		if rt.Git == nil {
			return preflight{}, ErrGitRequired
		}
		if rt.Transport == nil {
			return preflight{}, errors.New("no remote transport configured")
		}
		branchRef := b.Branch
		if !strings.HasPrefix(branchRef, "refs/heads/") {
			branchRef = "refs/heads/" + branchRef
		}
		sha, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
		if err != nil {
			return preflight{}, fmt.Errorf("resolve branch %s: %w", b.Branch, err)
		}
		if !ok {
			return preflight{}, fmt.Errorf("branch %s not found", b.Branch)
		}
		pf.remoteSHA = sha
		return pf, nil
	}

	if b.State == store.StateBroken {
		return preflight{}, fmt.Errorf("binding %q is broken; rebind before sending", name)
	}
	if b.Round > b.RoundCap {
		return preflight{}, roundCapRefusal(name, b.RoundCap)
	}

	// A headless builder is a process relevo starts per round, so the
	// runner must exist and no previous process may still be alive -- and both
	// are checked here, before Send stages anything.
	if rt.Runner == nil {
		return preflight{}, fmt.Errorf("binding %q: %w", name, spawn.ErrRunnerUnavailable)
	}
	if b.Builder.PID != 0 {
		alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
		if err != nil {
			return preflight{}, fmt.Errorf("binding %q: check previous process %d: %w", name, b.Builder.PID, err)
		}
		if alive {
			return preflight{}, fmt.Errorf("binding %q (pid %d): %w", name, b.Builder.PID, ErrBuilderBusy)
		}
	}
	// A round whose marker or report is already on disk is finished: the
	// daemon has simply not closed it yet (it is busy, or the process that
	// wrote the file just exited).
	if path, found := pendingRoundFile(rt, name, b.Round); found {
		return preflight{}, fmt.Errorf("binding %q round %d: %s exists: %w", name, b.Round, filepath.Base(path), ErrReportPending)
	}
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return preflight{}, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return preflight{}, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	role, err := resolve.BindingSpec(rt.RoleRegistry(), b, c.Harness)
	if err != nil {
		return preflight{}, fmt.Errorf("binding %q builder: %w", b.Name, err)
	}
	prompt, err = launchPrompt(rt, b, c.Harness, tier, prompt, false)
	if err != nil {
		return preflight{}, err
	}
	pf.prompt = prompt
	argv, err := candidate.HeadlessLaunch(c, role, tier, roundBudget(b), prompt, housekeep.RoundTree(HousekeepDeps(rt), b), rt.Store.OutDir(b.Name), promptPath, chain.Dir(rt.Store, nil, name))
	if err != nil {
		return preflight{}, err
	}
	pf.argv = argv

	// The gate is advisory only: a gated candidate can still be sent to, it
	// just tells the human the daemon would switch away after the start.
	for _, g := range resolve.WithSkillGates(ResolveDeps(rt), availability.Gates(AvailabilityDeps(rt)), b.CWD, nil) {
		if g.Token == b.BuilderCandidate && (g.Kind == availability.RateLimited || g.Kind == availability.RolesMissing || g.Kind == availability.SkillMissing || windowBlock(g)) {
			gate := g
			pf.gate = &gate
			break
		}
	}

	return pf, nil
}

// actorRunsPlanner reports whether b's actor resolves to the planner
// definition: the actor whose prompt is a seed. A role that fails to
// resolve is not this check's business: the existing error surfaces where it
// does.
func actorRunsPlanner(rt Runtime, b store.Binding) bool {
	spec, err := resolve.BindingSpec(rt.RoleRegistry(), b, b.Builder.Kind)
	return err == nil && spec.Definition == plannerAgent
}
