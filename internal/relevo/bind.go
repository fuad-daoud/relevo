package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrBuilderAlive reports a rebind attempt against a binding whose builder is
// still running. Relevo never abandons a live builder: the human ends it, or
// `relevo done` the binding first.
var ErrBuilderAlive = errors.New("builder is still alive; rebinding would abandon it")

// ErrNoMasterMindSession is mastermind.ErrNoMasterMind as bind, add, fork and ask
// surface it (#303 §4.3): a hard error, printed by the CLI with relevo's own
// prefix and exit 1, whose text is the fix.
var ErrNoMasterMindSession = errors.New(`no relevo MasterMind for this session. Run "relevo mastermind init" once here, or enable the relevo plugin (relevo doctor).`)

// ErrGitRequired reports a bind that needs a worktree with no git available.
var ErrGitRequired = errors.New("relevo bind needs git; pass --cwd to bind a tree yourself")

// resolveVerbMasterMind is how bind, add, fork and ask get their mastermind (§4.3):
// flag > env > host > session, through the registry Runtime.MasterMinds names.
// ref is the caller's --mastermind value; "" resolves from the environment, the
// caller's parent process and its harness session, in that order.
//
// mastermind.ErrNoMasterMind becomes ErrNoMasterMindSession, and so does a Runtime with
// no registry configured: nothing derives a mastermind from a pane any more.
func resolveVerbMasterMind(rt Runtime, ref string) (mastermind.Record, bool, error) {
	return resolveVerbMasterMindHeal(rt, ref, true)
}

// resolveVerbMasterMindHeal is resolveVerbMasterMind with the gone-record heal
// switchable: a caller that may run in the daemon passes false, since the
// daemon's environment is not the calling session's.
func resolveVerbMasterMindHeal(rt Runtime, ref string, heal bool) (mastermind.Record, bool, error) {
	if rt.MasterMinds == nil {
		return mastermind.Record{}, false, ErrNoMasterMindSession
	}
	var now time.Time
	if rt.Now != nil {
		now = rt.Now()
	}
	cwd, _ := os.Getwd()
	rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
		Flag:            ref,
		Env:             os.Getenv,
		PPID:            os.Getppid(),
		ProcStart:       rt.ProcStart,
		MuseHost:        rt.MuseHost,
		Now:             now,
		CWD:             cwd,
		OpencodeSession: rt.OpencodeSession,
		Heal:            heal,
	})
	switch {
	case err == nil:
		return rec, true, nil
	default:
		var unreg mastermind.ErrUnregisteredSession
		if errors.As(err, &unreg) && unreg.Kind == "opencode" {
			rec, _, initErr := mastermind.Init(rt.MasterMinds, mastermind.InitInput{
				Kind:      "opencode",
				SessionID: unreg.SessionID,
				CWD:       cwd,
				Now:       now,
			})
			if initErr != nil {
				return mastermind.Record{}, false, initErr
			}
			return rec, true, nil
		}
		if errors.Is(err, mastermind.ErrNoMasterMind) {
			return mastermind.Record{}, false, ErrNoMasterMindSession
		}
		return mastermind.Record{}, false, err
	}
}

// recordEndpoint is the mastermind endpoint a resolved record supplies (§3.2):
// its kind and session. MasterMind.PaneID is written by nothing since #303; it
// stays a field only so an older bind.json loads.
func recordEndpoint(rec mastermind.Record) store.Endpoint {
	return store.Endpoint{
		Kind:      rec.HarnessKind,
		SessionID: rec.SessionID,
	}
}

// BindOptions describes one bind request. A local builder is always headless.
type BindOptions struct {
	Name string
	// Candidate is a harness/provider/model token; empty means resolve by role
	// through resolveCandidate, except in resume, where empty means "not rebinding".
	Candidate string
	// MasterMindID is the caller's --mastermind value when it has one, and the
	// resolved record's id afterwards: bind sets Binding.MasterMindID from it.
	// Empty means "resolve this session's mastermind" (§4.3).
	MasterMindID string
	CWD          string
	Resume       bool

	// Rebind, with Resume, replaces a builder that is gone by resolving a
	// candidate through policy.json order and the ledger, exactly as a
	// fresh bind with Candidate empty does (#92). Without it, an empty
	// Candidate on resume means "mastermind-only: touch no builder". Ignored
	// when Candidate is set.
	Rebind bool

	// RoundTimeout overrides the binding's round budget. Zero keeps the
	// store's default.
	RoundTimeout time.Duration

	// Headless is accepted as a no-op: a local builder is always a process
	// relevo runs per round (#99, #303).
	Headless bool

	// Tier overrides the candidate/policy permission tier (#141).
	Tier string

	// AllowYolo permits Tier == "yolo" above policy max_tier for this command (#141).
	AllowYolo bool

	// Gate is the acceptance command relevo runs on the binding's completion
	// marker (#132). Empty means fall back to policy.json's gate.default,
	// unless NoGate opts out of that default.
	Gate string

	// NoGate opts this binding out of policy.json's gate.default even when
	// Gate is empty (#132). Ignored when Gate is set.
	NoGate bool

	// Regate is the binding's automatic repair-round budget (#132 part 2):
	// how many repair rounds relevo may open after a failing gate. nil falls
	// back to policy.json's gate.regate; an explicit 0 disables repair even
	// when the policy sets one.
	Regate *int

	// Feature is the human-given label grouping this binding with others
	// (#172); "" means ungrouped. Validated by store.ValidFeature when set.
	// On resume, an empty Feature means "leave the binding's existing
	// Feature untouched" rather than clearing it.
	Feature string

	// NoFeature is the explicit "this is not a feature" mark (#637). A fresh
	// bind must name exactly one of Feature/NoFeature (RequireFeatureChoice);
	// on resume, NoFeature clears the binding's stored feature.
	NoFeature bool

	// Ticket is the issue this binding serves, as typed on --ticket (#637):
	// a number, #N, project/path#N, an issue key like ABC-123, or an http(s)
	// URL, in the stored form store.ParseTicket returns. Empty means none. On
	// resume, empty leaves the binding's ticket untouched; there is no way to
	// clear one.
	Ticket string

	// Role is the writer role the new binding runs (#382); "" means builder.
	// Bind ignores it on resume, because the binding keeps its stored role.
	Role string

	// Local runs the builder here, whatever the actor's placement says. It is
	// the --local flag; a fresh bind with no placement list behaves the same
	// whether it is set or not.
	Local bool
}

// RequireFeatureChoice is the CLI's exactly-one rule for --feature/--no-feature
// (#637). A fresh bind must name exactly one; a resume may name none (the
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
var errFeatureChoice = badInputf("choose exactly one of --feature <label> or --no-feature")

// parseTicket turns a raw --ticket value into its stored form (#637), using
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

// BindResolved ties the calling mastermind to a builder over one working tree.
// The second return is how the builder was chosen, zero when a pane was
// adopted.
func BindResolved(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, resolve.Resolution, error) {
	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	if opts.CWD == "" {
		return store.Binding{}, resolve.Resolution{}, badInputf("no working directory")
	}

	if !haveRec {
		return store.Binding{}, resolve.Resolution{}, ErrNoMasterMindSession
	}
	opts.MasterMindID = rec.ID
	mastermindEP := recordEndpoint(rec)

	if opts.Resume {
		return resume(ctx, rt, opts, mastermindEP)
	}

	// A fresh bind places itself once, here, before anything is created: an
	// explicit --local is taken as given, and otherwise the actor's own list
	// decides. A remote result is created through add's own path, so the wire
	// and the client's record keep one implementation.
	placement, perr := resolve.CreatePlacement(ctx, ResolveDeps(rt),
		resolve.BindingRole(store.Binding{Role: resolve.NormRole(opts.Role)}), opts.Candidate, "", opts.Local,
		resolve.PlacementFlags{Tier: opts.Tier, Feature: opts.Feature, Ticket: opts.Ticket})
	if perr != nil {
		return store.Binding{}, resolve.Resolution{}, perr
	}
	if !placement.Local() {
		name := opts.Name
		if name == "" {
			name = SanitizeName(baseName(opts.CWD))
		}
		added, aerr := Add(ctx, rt, AddOptions{
			Name:         name,
			Candidate:    opts.Candidate,
			MasterMindID: opts.MasterMindID,
			Repo:         opts.CWD,
			Server:       placement.Name,
			Tier:         opts.Tier,
			Feature:      opts.Feature,
			Ticket:       opts.Ticket,
			Role:         opts.Role,
			Placement:    placement,
		})
		if aerr != nil {
			return store.Binding{}, resolve.Resolution{}, aerr
		}
		return added.Binding, added.Resolution, nil
	}

	return create(ctx, rt, opts, mastermindEP, placement)
}

// Bind is BindResolved without the resolution, for the callers that only
// need the binding. cmdBind uses BindResolved to print why relevo picked
// what it did.
func Bind(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, error) {
	b, _, err := BindResolved(ctx, rt, opts)
	return b, err
}

// resume re-points an existing binding at the calling mastermind, and -- when
// the caller supplied a builder, or asked for one with Rebind -- at a new builder as well.
//
// Preconditions:  the binding exists. When a builder is supplied, the binding's
//
//	current builder must NOT be alive: rebinding over a working
//	builder would abandon a round mid-flight.
//	A headless binding's builder is a process; it is alive when
//	the Runner says so.
//
// Postconditions: MasterMind points at the caller. A rebind of a DONE binding is
//
//	refused. A mastermind-only resume of one is allowed, and
//	reactivates it, exactly as before this feature existed. When
//	a builder was supplied: Builder is the new endpoint with its
//	session id recorded, State is Active,
//	HaltNotifiedRound is 0, and the builder-screen fields are
//	cleared. RoundClosedTree is cleared when a builder was supplied:
//	a tree that changed hands says nothing about a builder that no
//	longer exists. Round, CWD, Name, RoundBaselineTree and the round
//	log are untouched.
//	The binding's mode is untouched too: a remote binding stays
//	remote, and a local one is headless.
//
// Errors: store.ErrNotFound; ErrBuilderAlive; ErrRunnerUnavailable; a wrapped
// git or remote error.
func resume(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint) (store.Binding, resolve.Resolution, error) {
	b, err := resumeLoad(rt, opts.Name)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	if mine, err := handedToMe(rt, opts.Name); err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	} else if mine && carriesOldPaths(rt, b) {
		if b, err = resumeHandedOver(ctx, rt, opts, b); err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}

	// A remote binding has no pane and no worktree: its mode is fixed at
	// creation (like a headless binding's), so none of the pane/worktree
	// logic below applies to it. §4.6.
	if b.Builder.Remote() {
		return resumeRemote(ctx, rt, opts, mastermindEP, b)
	}

	// #637: the label flags are applied after the load, because --ticket's
	// repository hint is the binding's own recorded origin.
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}
	ticket := ""
	if opts.Ticket != "" {
		ticket, err = parseTicket(opts.Ticket, b.RepoRef)
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
	}

	var res resolve.Resolution
	restore := false
	if b.Worktree != "" {
		if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
			restore = true
		}
	}

	if restore {
		if b.Branch == "" {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: worktree %s is gone and no branch is recorded; relevo bind --worktree to start fresh", opts.Name, b.Worktree)
		}
		if rt.Git == nil {
			return store.Binding{}, resolve.Resolution{}, errors.New("git unavailable; cannot restore worktree")
		}
		// An add binding's CWD is the worktree itself (add.go), which is the
		// directory that is gone; the repository it was cut from is not
		// recorded. The caller's cwd is where add ran, so restore from there
		// once the branch is confirmed to live in it.
		exists, err := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("restore worktree: %w", err)
		}
		if !exists {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: branch %s is not in %s; run resume from the repository the worktree was cut from", opts.Name, b.Branch, opts.CWD)
		}
		if err := rt.Git.CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch); err != nil {
			if errors.Is(err, git.ErrBranchCheckedOut) {
				return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: branch %s is checked out in another worktree (git worktree list); free it, then resume", opts.Name, b.Branch)
			}
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("restore worktree: %w", err)
		}
		res.RestoredWorktree = b.Worktree
		res.RestoredBranch = b.Branch
	}

	rebinding := opts.Rebind || opts.Candidate != ""
	// A paused binding has no builder identity left -- pause cleared it -- so
	// a resume from PAUSED always rebinds; --rebind is implied (#137).
	if b.State == store.StatePaused {
		res.WasPaused = true
		rebinding = true
	}

	var (
		builder store.Endpoint
	)
	if rebinding {
		if b.State == store.StateDone && !restore {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q is done: `relevo bind` to start fresh", opts.Name)
		}
		// A binding's mode is fixed at creation (#119). A local builder is a
		// process, so ask the Runner whether its PID is still alive: a PID
		// has no "moved pane" ambiguity.
		if rt.Runner == nil {
			return store.Binding{}, resolve.Resolution{}, spawn.ErrRunnerUnavailable
		}
		if b.Builder.PID != 0 {
			alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if err != nil {
				return store.Binding{}, resolve.Resolution{}, fmt.Errorf("check builder process: %w", err)
			}
			if alive {
				return store.Binding{}, resolve.Resolution{}, ErrBuilderAlive
			}
		}
		opts.Headless = true
		// A resume re-points the binding at a builder; it keeps its stored
		// writer role (#382 §2), so BindOptions.Role is ignored here.
		opts.Role = b.Role
		resCandidate, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, resolve.WithSkillGates(ResolveDeps(rt), availability.Gates(AvailabilityDeps(rt)), b.CWD, nil), opts.Candidate, resolve.BindingRole(b))
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
		if opts.Tier != "" {
			tier := resolve.ResolveTier(opts.Tier, resCandidate.Candidate, rt.RoleRegistry(), resolve.BindingRole(b))
			if err := resolve.CheckTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
				return store.Binding{}, resolve.Resolution{}, err
			}
			opts.Tier = string(tier)
			b.Tier = string(tier)
		} else if b.Tier != "" {
			opts.Tier = b.Tier
		}
		var res2 resolve.Resolution
		pick := opts
		pick.CWD = b.CWD
		builder, res2, err = resolveBuilder(ctx, rt, nil, pick, opts.Name)
		if err != nil {
			return store.Binding{}, resolve.Resolution{}, err
		}
		res2.RestoredWorktree = res.RestoredWorktree
		res2.RestoredBranch = res.RestoredBranch
		res2.WasPaused = res.WasPaused
		res = res2
	}

	var out store.Binding
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		if rebinding && b.State == store.StateDone && !restore {
			return fmt.Errorf("binding %q is done: `relevo bind` to start fresh", opts.Name)
		}

		// RepoRef and Feature are deliberately left untouched here (beyond
		// the explicit Feature/Ticket overrides below): a resume re-points
		// endpoints, it does not rediscover facts a fresh bind already
		// captured.
		b.MasterMind = mastermindEP
		if opts.MasterMindID != "" {
			b.MasterMindID = opts.MasterMindID
		}
		// #637: --feature sets, --no-feature clears, neither keeps; --ticket
		// sets when non-empty, absent keeps.
		if opts.Feature != "" {
			b.Feature = opts.Feature
		} else if opts.NoFeature {
			b.Feature = ""
		}
		if opts.Ticket != "" {
			b.Ticket = ticket
		}
		wasPaused := b.State == store.StatePaused
		// A resume is a fresh attempt, so the halt the previous one left goes
		// with it: every field of it, or the per-round notification key dedups
		// the next halt of this round against a reason nobody is looking at any
		// more. OwedHalt is left alone, as the other human paths leave it.
		b = clearHaltFields(b)
		b.State = store.StateActive
		// A resume or a rebind is a fresh attempt, so the previous round's
		// progress clock, stall, exploring and stale stamps say nothing about
		// it (#135).
		b.StalledSince = time.Time{}
		b.Progress = nil
		b.ExploringSince = time.Time{}
		b.StaleSince = time.Time{}
		if rebinding {
			// A rebind can land mid-round: resume refuses only while the
			// current process is alive (ErrBuilderAlive above), so an open
			// round whose process exited rebinds here with
			// StreamRound == Round. The round's stream cursor and its
			// segment list then move onto the replacement, so the drain
			// keeps rendering the same file from where it left off. Between
			// rounds (StreamRound != Round) there is nothing to carry and
			// the fresh endpoint stands.
			if b.Builder.StreamRound == b.Round {
				b.Builder = carryStream(b.Builder, builder)
			} else {
				b.Builder = builder
			}
			b.BuilderCandidate = res.Token() // "" when adopting a pane
			b.BuilderAccount = res.Account
			b.BuilderPinned = res.How == resolve.HowExplicit
			b = clearHaltFields(b)
			// The owed notification goes with the halt: the replacement builder
			// is a fresh attempt, and an entry the replaced round still owed
			// would be notified as a halt of a round nobody is running.
			b.OwedHalt = nil
			b.BuilderScreen = ""
			b.BuilderScreenAt = time.Time{}
			b.RoundClosedTree = ""
			if opts.Tier != "" {
				b.Tier = opts.Tier
			}
		}

		if err := tx.Save(b); err != nil {
			return err
		}

		if rebinding && res.How != "" {
			if err := tx.AppendLog(opts.Name, resolve.PickEntry(rt.Now(), b.Round, "builder", res)); err != nil {
				return err
			}
		}

		// The resume entry is appended after the pick so it is the log's
		// last word on the binding: it is the event, the pick is a detail.
		if wasPaused {
			if err := tx.AppendLog(opts.Name, store.LogEntry{
				Round:     b.Round,
				Direction: store.DirToMasterMind,
				Kind:      store.KindResume,
				Confirmed: true,
				Note:      "resumed",
			}); err != nil {
				return err
			}
		}

		out = b
		return nil
	})
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}

	return out, res, nil
}

// resumeRemote is resume's remote-binding path (§4.6). A remote binding's
// mode is fixed at creation, so this never adopts a pane or spawns one: it
// only forwards to the server and, once the server agrees, repoints the
// mastermind and reactivates the binding locally.
//
// Errors: "cannot change a remote builder; unbind and add" when the caller
// asked to change the builder (--rebind, --candidate, or
// --headless); the label refusal (#637) when --feature, --no-feature or
// --ticket is set -- no server endpoint could change a stored label, and a
// mirror-only change would recreate the laptop/server disagreement #637
// removes; ErrRemoteUnavailable; a wrapped server error; a wrapped git
// error; or a message naming the binding when its branch is gone.
func resumeRemote(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint, b store.Binding) (store.Binding, resolve.Resolution, error) {
	if opts.Rebind || opts.Candidate != "" || opts.Headless {
		return store.Binding{}, resolve.Resolution{}, badInputf("cannot change a remote builder; unbind and add")
	}
	// #637: --feature/--no-feature/--ticket have no server endpoint to reach,
	// so a remote resume refuses them rather than changing only the mirror.
	if opts.Feature != "" || opts.NoFeature || opts.Ticket != "" {
		return store.Binding{}, resolve.Resolution{}, badInputf("cannot change a remote binding's feature or ticket; unbind and add")
	}
	if rt.Remote == nil {
		return store.Binding{}, resolve.Resolution{}, ErrRemoteUnavailable
	}

	if _, rerr := rt.Remote.Resume(ctx, b.Builder.Server, b.Name); rerr != nil {
		var httpErr *client.HTTPError
		if errors.As(rerr, &httpErr) {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("%s: %s", b.Builder.Server, httpErr.Body.Message)
		}
		if errors.Is(rerr, client.ErrUnreachable) {
			return store.Binding{}, resolve.Resolution{}, fmt.Errorf("%s unreachable: %w", b.Builder.Server, rerr)
		}
		return store.Binding{}, resolve.Resolution{}, fmt.Errorf("%s: %w", b.Builder.Server, rerr)
	}

	if rt.Git == nil {
		return store.Binding{}, resolve.Resolution{}, errors.New("git unavailable; cannot verify the branch")
	}
	exists, berr := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
	if berr != nil {
		return store.Binding{}, resolve.Resolution{}, fmt.Errorf("check branch %s: %w", b.Branch, berr)
	}
	if !exists {
		return store.Binding{}, resolve.Resolution{}, fmt.Errorf("binding %q: branch %s is gone; relevo unbind, then relevo bind --server to start fresh", opts.Name, b.Branch)
	}

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		cur.MasterMind = mastermindEP
		if opts.MasterMindID != "" {
			cur.MasterMindID = opts.MasterMindID
		}
		cur = clearHaltFields(cur)
		cur.State = store.StateActive
		if err := tx.Save(cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}

	return out, resolve.Resolution{}, nil
}

// ResolveRegate applies the repair-round rule (#132 part 2): an explicit
// --regate is used as given (0 disables repair), and an unset flag falls back
// to policy.json's gate.regate.
func ResolveRegate(regate *int, pol policy.Policy) int {
	if regate != nil {
		return *regate
	}
	return pol.GateRegate()
}

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
	// bind, naming the flag (A5 §2).
	if shape == store.ShapeReader {
		if opts.Gate != "" {
			return store.Binding{}, resolve.Resolution{}, badInputf("--gate: a reader round has no check")
		}
		if opts.Regate != nil {
			return store.Binding{}, resolve.Resolution{}, badInputf("--regate: a reader round has no check")
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
		return store.Binding{}, resolve.Resolution{}, badInputf(
			"binding %q already exists: `relevo unbind %s` to start fresh, or `relevo bind --resume --name %s` to adopt it",
			name, name, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Binding{}, resolve.Resolution{}, err
	}

	// Check the working tree before spawning anything. Save re-checks under the
	// lock and stays authoritative, but without this a refused bind would leave
	// a started builder pane stranded with nothing pointing at it.
	//
	// Only a writer can be blocked here (A5 §3): a reader may share a writer's
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

	// #637: the ticket is parsed against the binding's own captured origin, so
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

// resolveBuilder returns a headless endpoint for a local builder (#99, #303).
// Nothing is opened and nothing is started: the endpoint records the mode, the
// name and the kind, and Send fills in the process fields per round (spec
// §5.3).
//
// A local builder is always headless since #303; the pane launch and the
// adopt-a-pane path are gone. Before returning, the candidate's launch is
// rendered once with headlessLaunch, so the permission-tier refusals
// (ErrTierUnsupported, ErrExtraArgsPermission) fire at bind/add/fork, before a
// worktree is kept -- the same validation the pane path's h.Launch used to
// perform.
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

// worktreeOutcome is what a teardown attempt decided about one binding's
// relevo-created worktree. Exactly one of Removed, Kept, Gone is set, or none
// when the binding never had a worktree.
type worktreeOutcome struct {
	Removed string // worktree relevo removed, or ""
	Kept    string // worktree relevo left in place, or ""
	Reason  string // why it was kept; "" when nothing was kept
	Gone    string // recorded worktree whose directory no longer exists, or ""
}

// worktreeTeardown decides what to do with a binding's relevo-created worktree
// and reports what it did. It never returns an error: failing to remove a
// directory must not fail the unbind or the sweep that asked for it.
//
// Rules, in order:
//  1. b.Worktree == ""        -> nothing to do (zero outcome)
//  2. rt.Git == nil           -> keep, reason "git unavailable"
//  3. stat(worktree) is ErrNotExist -> gone (the directory was already removed)
//  4. the dirty check errors    -> keep, reason naming the FAILED CHECK: "dirty check failed: " + brief(err)
//  5. the tree is dirty         -> keep, reason "uncommitted changes"
//  6. otherwise                 -> remove; on failure keep with the git error brief(err)
//
// worktreeTeardown never removes a branch; GC removes a relevo-created branch once it is on a remote-tracking ref (refclean.go).
func worktreeTeardown(ctx context.Context, rt Runtime, b store.Binding, dryRun bool) worktreeOutcome {
	if b.Worktree == "" {
		return worktreeOutcome{}
	}
	if rt.Git == nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: "git unavailable"}
	}

	// git classifies a chdir into a missing directory as a missing binary,
	// which is the wrong diagnosis; the caller knows the path and checks it first.
	if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
		rt.Store.PruneWorktreeDirs()
		return worktreeOutcome{Gone: b.Worktree}
	}

	dirty, err := rt.Git.Dirty(ctx, b.Worktree)
	if err != nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: "dirty check failed: " + brief(err)}
	}
	if dirty {
		return worktreeOutcome{Kept: b.Worktree, Reason: "uncommitted changes"}
	}

	if dryRun {
		return worktreeOutcome{Removed: b.Worktree}
	}

	if err := rt.Git.RemoveWorktree(ctx, b.CWD, b.Worktree, false); err != nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: brief(err)}
	}
	rt.Store.PruneWorktreeDirs()
	return worktreeOutcome{Removed: b.Worktree}
}

// UnbindResult is what unbinding actually did. Exactly one of WorktreeRemoved,
// WorktreeKept, WorktreeGone is set, or none when the binding never had a
// worktree. A kept worktree is the important case: it means the fork's tree
// still holds uncommitted work, so relevo left it alone and the human decides.
type UnbindResult struct {
	Archived        bool   // true when the binding was archived rather than deleted
	WorktreeRemoved string // worktree relevo removed, or ""
	WorktreeKept    string // worktree relevo refused to remove, or ""
	KeptReason      string // why it was kept; "" when nothing was kept
	WorktreeGone    string // recorded worktree whose directory no longer exists, or ""
	// ProcessStopped is the headless builder process relevo stopped, or 0
	// (#99). ProcessErr is why a stop failed; "" when it did not. Both
	// zero for a pane binding and for a headless one between rounds.
	ProcessStopped int
	ProcessErr     string
}

// ErrWorktreeKept reports that unbind left the binding in place because its
// worktree was kept; force archives it anyway.
var ErrWorktreeKept = refusalSentinel("worktree kept")

func keptRefusal(name string, res UnbindResult) error {
	msg := fmt.Sprintf("binding %s kept: worktree %s kept (%s)", name, res.WorktreeKept, res.KeptReason)
	if res.ProcessStopped != 0 {
		msg += fmt.Sprintf("; its builder process (pid %d) was stopped", res.ProcessStopped)
	}
	return NewWorktreeKept(msg)
}

// askServerUnbind tells a remote binding's server to release it before
// anything local changes. A 404 means the server already considers it gone,
// which is not a reason to refuse the local unbind.
func askServerUnbind(ctx context.Context, rt Runtime, b store.Binding, force bool) error {
	if rt.Remote == nil {
		return ErrRemoteUnavailable
	}
	uerr := rt.Remote.Unbind(ctx, b.Builder.Server, b.Name, force)
	if uerr == nil {
		return nil
	}
	var httpErr *client.HTTPError
	if errors.As(uerr, &httpErr) {
		switch {
		case httpErr.Status == 404:
			return nil
		case httpErr.Status == 409 && httpErr.Body.Code == remote.CodeWorktreeKept:
			reason := KeptReason(httpErr.Body.Message)

			return newWorktreeKeptWithReason("worktree on "+b.Builder.Server+" kept: "+reason, reason)
		}
	}
	if errors.Is(uerr, client.ErrUnreachable) {
		return fmt.Errorf("%s unreachable: %w", b.Builder.Server, uerr)
	}
	return fmt.Errorf("%s: %w", b.Builder.Server, uerr)
}

// reapSessions deletes the builder's session and everything already abandoned,
// directly: the record is about to go, so nothing is saved and a failed delete
// only warns. Deletion is safe only once the process is really stopped -- a
// live one still holds its session.
func reapSessions(ctx context.Context, rt Runtime, b store.Binding, res UnbindResult) {
	if res.ProcessStopped != 0 && res.ProcessErr == "" {
		b = abandonSession(b)
	}
	for _, s := range b.AbandonedSessions {
		if rt.SessionReaper == nil {
			break
		}
		if err := rt.SessionReaper.DeleteSession(ctx, s); err != nil {
			slog.Warn("abandoned harness session not deleted", "binding", b.Name, "kind", s.Kind, "id", s.ID, "err", err)
		}
	}
}

// Unbind is UnbindWith without force: a kept worktree keeps the binding.
func Unbind(ctx context.Context, rt Runtime, name string, archive bool) (UnbindResult, error) {
	return UnbindWith(ctx, rt, name, archive, false)
}

// UnbindWith clears away one binding's state, leaving its builder process and
// worktree release to the paths below.
//
// When archive is set the binding's record is archived rather than deleted,
// which frees the name for a fresh bind while its log and every round file
// survive as rows -- the record of what the mastermind actually told the builder.
// If relevo created a git worktree for this binding, UnbindWith removes it provided
// it is clean, never removing the branch. When the worktree is kept the binding
// stays, resumable, and the result comes back with ErrWorktreeKept; force
// archives or deletes it anyway.
func UnbindWith(ctx context.Context, rt Runtime, name string, archive, force bool) (UnbindResult, error) {
	b, err := rt.Store.Load(name)
	if err != nil {
		return UnbindResult{}, err
	}

	// A running chain owns its member's rounds: unbinding one would steal the
	// record the chain's next send needs, and the chain has no way to resume
	// it. The refusal is read-only and sits before every side effect below --
	// a late one would stop a process and then keep the record.
	if err := refuseRunningChainMemberStore(rt.Store, name); err != nil {
		return UnbindResult{}, err
	}

	if b.Builder.Remote() {
		if err := askServerUnbind(ctx, rt, b, force); err != nil {
			return UnbindResult{}, err
		}
	}

	var res UnbindResult
	// Stop a live headless round before touching its tree: a builder still
	// writing would dirty the worktree relevo is about to judge clean or not.
	// A failed stop is reported, never fatal -- the unbind is the human's
	// decision and it proceeds.
	if pid, err := stopProcess(ctx, rt, b, "unbind"); err != nil {
		res.ProcessErr = fmt.Sprintf("pid %d: %v", pid, err)
	} else if pid != 0 {
		res.ProcessStopped = pid
	}

	outcome := worktreeTeardown(ctx, rt, b, false)
	res.WorktreeRemoved = outcome.Removed
	res.WorktreeKept = outcome.Kept
	res.KeptReason = outcome.Reason
	res.WorktreeGone = outcome.Gone

	// The guard sits before the session reaping so a kept binding stays
	// resumable with its sessions intact.
	if outcome.Kept != "" && !force {
		return res, keptRefusal(name, res)
	}

	reapSessions(ctx, rt, b, res)

	// A reader's throwaway worktree goes with the binding, before the record
	// is archived or deleted: the sweep can no longer resolve it afterwards.
	removeReaderScratch(ctx, rt, b, b.Round)
	removeRoundTmp(rt, b, b.Round)

	if archive {
		if _, err := rt.Store.Archive(name); err != nil {
			return res, err
		}
		res.Archived = true
	} else {
		if err := rt.Store.Delete(name); err != nil {
			return res, err
		}
	}

	return res, nil
}

// SanitizeName coerces a directory name into relevo's binding-name rule.
func SanitizeName(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}

	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "relevo"
	}
	if out[0] < 'a' || out[0] > 'z' {
		out = "b" + out
	}
	if len(out) > store.MaxAgentNameLen {
		out = out[:store.MaxAgentNameLen]
	}

	return out
}

func baseName(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
