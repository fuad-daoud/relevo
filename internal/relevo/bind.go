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
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote/client"
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
		Now:             now,
		CWD:             cwd,
		OpencodeSession: rt.OpencodeSession,
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
// its kind, session and transcript locator. MasterMind.PaneID is written by
// nothing since #303; it stays a field only so an older bind.json loads.
func recordEndpoint(rec mastermind.Record) store.Endpoint {
	return store.Endpoint{
		Kind:              rec.HarnessKind,
		SessionID:         rec.SessionID,
		TranscriptLocator: rec.TranscriptLocator,
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
	// a number, #N, owner/repo#N, or a .../issues/N URL. Empty means none. On
	// resume, empty leaves the binding's ticket untouched; there is no way to
	// clear one.
	Ticket string

	// Role is the writer role the new binding runs (#382); "" means builder.
	// Bind ignores it on resume, because the binding keeps its stored role.
	Role string
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
var errFeatureChoice = errors.New("choose exactly one of --feature <label> or --no-feature")

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
func BindResolved(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, Resolution, error) {
	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}
	if opts.CWD == "" {
		return store.Binding{}, Resolution{}, errors.New("no working directory")
	}

	if !haveRec {
		return store.Binding{}, Resolution{}, ErrNoMasterMindSession
	}
	opts.MasterMindID = rec.ID
	mastermindEP := recordEndpoint(rec)
	if mastermindEP.TranscriptLocator == "" {
		mastermindEP.TranscriptLocator = mastermindLocator(rt, mastermindEP.Kind, mastermindEP.SessionID)
	}

	if opts.Resume {
		return resume(ctx, rt, opts, mastermindEP)
	}

	return create(ctx, rt, opts, mastermindEP)
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
func resume(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint) (store.Binding, Resolution, error) {
	b, err := rt.Store.Load(opts.Name)
	if err != nil {
		return store.Binding{}, Resolution{}, err
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
			return store.Binding{}, Resolution{}, err
		}
	}
	ticket := ""
	if opts.Ticket != "" {
		ticket, err = parseTicket(opts.Ticket, b.RepoRef)
		if err != nil {
			return store.Binding{}, Resolution{}, err
		}
	}

	var res Resolution
	restore := false
	if b.Worktree != "" {
		if _, err := os.Stat(b.Worktree); errors.Is(err, os.ErrNotExist) {
			restore = true
		}
	}

	if restore {
		if b.Branch == "" {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: worktree %s is gone and no branch is recorded; relevo bind --worktree to start fresh", opts.Name, b.Worktree)
		}
		if rt.Git == nil {
			return store.Binding{}, Resolution{}, errors.New("git unavailable; cannot restore worktree")
		}
		// An add binding's CWD is the worktree itself (add.go), which is the
		// directory that is gone; the repository it was cut from is not
		// recorded. The caller's cwd is where add ran, so restore from there
		// once the branch is confirmed to live in it.
		exists, err := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
		if err != nil {
			return store.Binding{}, Resolution{}, fmt.Errorf("restore worktree: %w", err)
		}
		if !exists {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: branch %s is not in %s; run resume from the repository the worktree was cut from", opts.Name, b.Branch, opts.CWD)
		}
		if err := rt.Git.CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch); err != nil {
			if errors.Is(err, git.ErrBranchCheckedOut) {
				return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: branch %s is checked out in another worktree (git worktree list); free it, then resume", opts.Name, b.Branch)
			}
			return store.Binding{}, Resolution{}, fmt.Errorf("restore worktree: %w", err)
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
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q is done: `relevo bind` to start fresh", opts.Name)
		}
		// A binding's mode is fixed at creation (#119). A local builder is a
		// process, so ask the Runner whether its PID is still alive: a PID
		// has no "moved pane" ambiguity.
		if rt.Runner == nil {
			return store.Binding{}, Resolution{}, spawn.ErrRunnerUnavailable
		}
		if b.Builder.PID != 0 {
			alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if err != nil {
				return store.Binding{}, Resolution{}, fmt.Errorf("check builder process: %w", err)
			}
			if alive {
				return store.Binding{}, Resolution{}, ErrBuilderAlive
			}
		}
		opts.Headless = true
		// A resume re-points the binding at a builder; it keeps its stored
		// writer role (#382 §2), so BindOptions.Role is ignored here.
		opts.Role = b.Role
		resCandidate, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), opts.Candidate, bindingRole(b))
		if err != nil {
			return store.Binding{}, Resolution{}, err
		}
		if opts.Tier != "" {
			tier := resolveRoleTier(opts.Tier, resCandidate.Candidate, rt.RoleRegistry(), bindingRole(b))
			if err := checkTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
				return store.Binding{}, Resolution{}, err
			}
			opts.Tier = string(tier)
			b.Tier = string(tier)
		} else if b.Tier != "" {
			opts.Tier = b.Tier
		}
		var res2 Resolution
		builder, res2, err = resolveBuilder(ctx, rt, nil, opts, opts.Name)
		if err != nil {
			return store.Binding{}, Resolution{}, err
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
		// captured. The mastermind transcript locator is the one exception:
		// the endpoint mastermindEP carries the record's, so a live agent's
		// absent field cannot wipe a locator the binding already had.
		oldTranscriptLocator := b.MasterMind.TranscriptLocator
		b.MasterMind = mastermindEP
		if oldTranscriptLocator != "" {
			b.MasterMind.TranscriptLocator = oldTranscriptLocator
		}
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
		b.State = store.StateActive
		// A resume or a rebind is a fresh attempt, so the previous round's
		// progress clock, stall, exploring and stale stamps say nothing about
		// it (#135).
		b.StalledSince = time.Time{}
		b.Progress = nil
		b.ExploringSince = time.Time{}
		b.StaleSince = time.Time{}
		b.StaleNotifiedAt = time.Time{}
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
			b.HaltNotifiedRound = 0
			b.Halt = ""
			b.HaltAt = time.Time{}
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
			if err := tx.AppendLog(opts.Name, pickEntry(rt.Now(), b.Round, "builder", res)); err != nil {
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
		return store.Binding{}, Resolution{}, err
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
func resumeRemote(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint, b store.Binding) (store.Binding, Resolution, error) {
	if opts.Rebind || opts.Candidate != "" || opts.Headless {
		return store.Binding{}, Resolution{}, errors.New("cannot change a remote builder; unbind and add")
	}
	// #637: --feature/--no-feature/--ticket have no server endpoint to reach,
	// so a remote resume refuses them rather than changing only the mirror.
	if opts.Feature != "" || opts.NoFeature || opts.Ticket != "" {
		return store.Binding{}, Resolution{}, errors.New("cannot change a remote binding's feature or ticket; unbind and add")
	}
	if rt.Remote == nil {
		return store.Binding{}, Resolution{}, ErrRemoteUnavailable
	}

	if _, rerr := rt.Remote.Resume(ctx, b.Builder.Server, b.Name); rerr != nil {
		var httpErr *client.HTTPError
		if errors.As(rerr, &httpErr) {
			return store.Binding{}, Resolution{}, fmt.Errorf("%s: %s", b.Builder.Server, httpErr.Body.Message)
		}
		if errors.Is(rerr, client.ErrUnreachable) {
			return store.Binding{}, Resolution{}, fmt.Errorf("%s unreachable: %w", b.Builder.Server, rerr)
		}
		return store.Binding{}, Resolution{}, fmt.Errorf("%s: %w", b.Builder.Server, rerr)
	}

	if rt.Git == nil {
		return store.Binding{}, Resolution{}, errors.New("git unavailable; cannot verify the branch")
	}
	exists, berr := rt.Git.BranchExists(ctx, opts.CWD, b.Branch)
	if berr != nil {
		return store.Binding{}, Resolution{}, fmt.Errorf("check branch %s: %w", b.Branch, berr)
	}
	if !exists {
		return store.Binding{}, Resolution{}, fmt.Errorf("binding %q: branch %s is gone; relevo unbind, then relevo bind --server to start fresh", opts.Name, b.Branch)
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
		cur.State = store.StateActive
		if err := tx.Save(cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	return out, Resolution{}, nil
}

// resolveGateFor applies the gate resolution rule (#132, #382): --no-gate wins
// over everything, an explicit --gate is used as given, and an unset flag takes
// policy.json's gate.default only when the binding's role gates. A writer role
// with "gate": false takes no gate.
func resolveGateFor(gate string, noGate bool, pol policy.Policy, roleChecks bool) string {
	if noGate {
		return ""
	}
	if gate != "" {
		return gate
	}
	if !roleChecks {
		return ""
	}
	return pol.GateDefault()
}

// resolveRegate applies the repair-round rule (#132 part 2): an explicit
// --regate is used as given (0 disables repair), and an unset flag falls back
// to policy.json's gate.regate.
func resolveRegate(regate *int, pol policy.Policy) int {
	if regate != nil {
		return *regate
	}
	return pol.GateRegate()
}

func create(ctx context.Context, rt Runtime, opts BindOptions, mastermindEP store.Endpoint) (store.Binding, Resolution, error) {
	shape, err := actorShape(rt.RoleRegistry(), opts.Role)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}
	// A reader round has no check, so the writer-only knobs are refused at
	// bind, naming the flag (A5 §2).
	if shape == store.ShapeReader {
		if opts.Gate != "" {
			return store.Binding{}, Resolution{}, errors.New("--gate: a reader round has no check")
		}
		if opts.Regate != nil {
			return store.Binding{}, Resolution{}, errors.New("--regate: a reader round has no check")
		}
	}
	name := opts.Name
	if name == "" {
		name = SanitizeName(baseName(opts.CWD))
	}
	if err := store.ValidName(name); err != nil {
		return store.Binding{}, Resolution{}, err
	}
	if opts.Feature != "" {
		if err := store.ValidFeature(opts.Feature); err != nil {
			return store.Binding{}, Resolution{}, err
		}
	}

	// Refuse a name that is already taken, before anything is spawned. Save
	// would overwrite only the binding record: the round log and the NNN-*
	// files survive, so a fresh round 1 would collide with the previous
	// session's round 1 and Reconcile would read that old report entry as
	// "already handled" -- silently, with no error and no notification.
	if _, err := rt.Store.Load(name); err == nil {
		return store.Binding{}, Resolution{}, fmt.Errorf(
			"binding %q already exists: `relevo unbind %s` to start fresh, or `relevo bind --resume --name %s` to adopt it",
			name, name, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Binding{}, Resolution{}, err
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
			return store.Binding{}, Resolution{}, err
		}
		if found && other.Name != name && other.State != store.StateDone && other.Shape == store.ShapeWriter {
			return store.Binding{}, Resolution{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
				opts.CWD, other.Name, other.BuilderCandidate, other.Round, store.ErrCWDTaken)
		}
	}

	var tier harness.Tier
	roleName := bindingRole(store.Binding{Role: normRole(opts.Role)})
	resCandidate, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), opts.Candidate, roleName)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}
	tier = resolveRoleTier(opts.Tier, resCandidate.Candidate, rt.RoleRegistry(), roleName)
	if shape == store.ShapeReader {
		tier, err = readerTier(tier, resCandidate.Candidate.Harness, rt.Policy)
		if err != nil {
			return store.Binding{}, Resolution{}, err
		}
	}
	if err := checkTierCap(tier, rt.Policy, opts.AllowYolo); err != nil {
		return store.Binding{}, Resolution{}, err
	}
	opts.Tier = string(tier)

	builder, res, err := resolveBuilder(ctx, rt, nil, opts, name)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	// #637: the ticket is parsed against the binding's own captured origin, so
	// a typed number becomes owner/repo#N when relevo knows the repo.
	repoRef := captureRepo(ctx, rt, opts.CWD)
	ticket, err := parseTicket(opts.Ticket, repoRef)
	if err != nil {
		return store.Binding{}, Resolution{}, err
	}

	b := store.Binding{
		Name:             name,
		CWD:              opts.CWD,
		MasterMind:       mastermindEP,
		MasterMindID:     opts.MasterMindID,
		Builder:          builder,
		BuilderCandidate: res.Token(),
		Round:            1,
		State:            store.StateActive,
		Tier:             string(tier),
		Role:             normRole(opts.Role),
		Shape:            shape,
		Gate:             resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), roleName)),
		Regate:           resolveRegate(opts.Regate, rt.Policy),
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
		return tx.AppendLog(name, pickEntry(rt.Now(), 1, "builder", res))
	})
	if err != nil {
		return store.Binding{}, Resolution{}, fmt.Errorf("bind failed: %w", err)
	}

	// Save fills in the defaults it owns -- round cap, round budget -- on its
	// own copy, so read back what was actually stored rather than returning
	// the pre-Save value and letting the two drift.
	stored, err := rt.Store.Load(b.Name)
	if err != nil {
		return store.Binding{}, Resolution{}, err
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
func resolveBuilder(ctx context.Context, rt Runtime, tx *store.Tx, opts BindOptions, name string) (store.Endpoint, Resolution, error) {
	roleName := bindingRole(store.Binding{Role: opts.Role})
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), opts.Candidate, roleName)
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}
	c := res.Candidate
	agentName, err := builderAgentName(name)
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}

	var role harness.RoleSpec
	role, err = rt.RoleRegistry().Spec(roleName, c.Harness)
	if err != nil {
		return store.Endpoint{}, Resolution{}, fmt.Errorf("binding %q builder: %w", name, err)
	}
	tier := harness.Tier(opts.Tier)
	if tier == "" {
		tier = harness.TierHarness
	}
	if _, err := spawn.HeadlessLaunch(c, role, tier, 0, "", opts.CWD, rt.Store.Dir(name)); err != nil {
		return store.Endpoint{}, Resolution{}, err
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

// Unbind clears away one binding's state, leaving its builder process and
// worktree release to the paths below.
//
// When archive is set the binding's record is archived rather than deleted,
// which frees the name for a fresh bind while its log and every round file
// survive as rows — the record of what the mastermind actually told the builder.
// If relevo created a git worktree for this binding, Unbind removes it provided
// it is clean, never removing the branch.
func Unbind(ctx context.Context, rt Runtime, name string, archive bool) (UnbindResult, error) {
	b, err := rt.Store.Load(name)
	if err != nil {
		return UnbindResult{}, err
	}

	// A remote binding's server is told first (§4.6), same shape as Done: it
	// is asked to release the binding before anything local changes. A 404
	// means the server already considers it gone, which is not a reason to
	// refuse the local unbind -- it proceeds exactly as if the server had
	// agreed.
	if b.Builder.Remote() {
		if rt.Remote == nil {
			return UnbindResult{}, ErrRemoteUnavailable
		}
		if uerr := rt.Remote.Unbind(ctx, b.Builder.Server, b.Name); uerr != nil {
			var httpErr *client.HTTPError
			if !(errors.As(uerr, &httpErr) && httpErr.Status == 404) {
				if errors.Is(uerr, client.ErrUnreachable) {
					return UnbindResult{}, fmt.Errorf("%s unreachable: %w", b.Builder.Server, uerr)
				}
				return UnbindResult{}, fmt.Errorf("%s: %w", b.Builder.Server, uerr)
			}
		}
	}

	var res UnbindResult
	// Stop a live headless round before touching its tree (#99, spec §4.6):
	// a builder still writing would dirty the worktree relevo is about to
	// judge clean or not. A failed stop is reported, never fatal -- the
	// unbind is the human's decision and it proceeds.
	if pid, err := stopProcess(ctx, rt, b, "unbind"); err != nil {
		res.ProcessErr = fmt.Sprintf("pid %d: %v", pid, err)
	} else if pid != 0 {
		res.ProcessStopped = pid
	}

	// The record is about to be removed or archived, so the builder's session
	// and everything already abandoned are deleted directly here, and nothing
	// is saved: a failed delete only warns. Deletion is safe only once the
	// process is really stopped -- a live one still holds its session.
	if res.ProcessStopped != 0 && res.ProcessErr == "" {
		b = abandonSession(b)
	}
	for _, s := range b.AbandonedSessions {
		if rt.SessionReaper == nil {
			break
		}
		if err := rt.SessionReaper.DeleteSession(ctx, s); err != nil {
			slog.Warn("abandoned harness session not deleted", "binding", name, "kind", s.Kind, "id", s.ID, "err", err)
		}
	}

	// A reader's throwaway worktree goes with the binding, before the record
	// is archived or deleted: the sweep can no longer resolve it afterwards.
	removeReaderScratch(ctx, rt, b, b.Round)

	outcome := worktreeTeardown(ctx, rt, b, false)
	res.WorktreeRemoved = outcome.Removed
	res.WorktreeKept = outcome.Kept
	res.KeptReason = outcome.Reason
	res.WorktreeGone = outcome.Gone

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
