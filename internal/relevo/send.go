package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

type SendResult struct {
	Round int
	Drift string // the drift line for stdout, or "" when there is nothing to say
	// Pick is the pick line when SendOptions.Builder changed the builder (the
	// KindPick note), or "" otherwise. For the CLI to print.
	Pick string
}

type SendOptions struct {
	Tier      string // "" means the binding's Tier; else a one-round override (headless only)
	AllowYolo bool
	// Builder is a candidate token that persists as the binding's builder from
	// this round on, until another --candidate or a mid-round switch changes it.
	// "" means the binding's current builder. In contrast to Tier, it is not a
	// one-round override.
	Builder string
	// Regate sets the binding's repair-round budget; nil leaves
	// it unchanged.
	Regate *int
	// Verify marks the round for a read-only reviewer at round close.
	// nil takes policy.json verify.default, so a plain send honours the
	// mastermind's configured default and --verify/--no-verify overrides it.
	Verify *bool
	// Defer stages the round -- plan written, log entry appended, State ==
	// active -- but does not spawn a builder; the caller (serve.admit or
	// relevo.Admit) starts it later (server only).
	Defer bool
	// Force sends a planner actor's seed even when it is over seedMaxBytes: a
	// planner's input is a seed, and forcing is the explicit escape.
	Force bool
}

// RemoteSendDeps holds the remote-send orchestration above the engine as
// plain func values over exported types only. Ship returns the server's
// answer as flat values -- the binding view, the out ref the bundle was cut
// from, and the heads the bundle carried -- and Record takes those same
// values, so the chain two-phase path crosses the boundary without an
// unexported struct.
type RemoteSendDeps struct {
	// Send ships one round to a remote server: the network half outside any
	// lock, then the under-lock tail that writes the round's state.
	Send func(ctx context.Context, rt Runtime, b store.Binding, planBody []byte, tier, builder string, force bool) (SendResult, error)
	// Ship is the network half, unlocked: it moves the out ref, snapshots the
	// bundle and starts the round, writing nothing local.
	Ship func(ctx context.Context, rt Runtime, b store.Binding, planBody []byte, tier, builder string, force bool, verify *bool) (remote.BindingView, string, map[string]string, error)
	// Record is the under-lock tail: it reloads the binding, checks the round
	// did not advance, stages the plan and writes the round's state. isChain
	// is the chain's own record half, which owns the member and files a
	// chain-step note instead of refusing.
	Record func(rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, outRef string, heads map[string]string, planBody []byte, builder string, isChain bool) (int, string, error)
}

// preflight is everything Send checks before it takes the state lock and
// writes: the plan bytes, the effective tier, the headless launch argv, the
// paths and the composed prompt. sendPreflight computes it read-only; Send and
// SendDryRun both call it, so a dry run can never disagree with a real send
// about the state of the world. A failed precondition is an error in
// Send's exact wording.
type preflight struct {
	b        store.Binding // the binding as loaded (read-only; Send re-loads under the lock)
	body     []byte
	tier     harness.Tier // effective tier for this round (opts.Tier parsed, or the stored tier); the cap is checked as it is derived for a local launch, while a served round's tier is capped by the server
	tierFrom string
	argv     []string            // headless: headlessLaunch's argv (proves the launch is well-formed); nil for remote
	gate     *availability.Gate  // advisory: a gate on b.BuilderCandidate (rate-limited or roles_missing), nil when none
	pick     *resolve.Resolution // --candidate's resolution to apply under the lock; nil when the builder does not change
	// repickFrom is the binding's old BuilderCandidate when the preflight
	// re-picked between rounds, repickWhy the reason; "" otherwise. When
	// repickFrom is non-empty, pick is non-nil.
	repickFrom, repickWhy string

	promptPath, reportPath, donePath string
	prompt                           string // composePrompt(...) -- computed, never sent

	remoteSHA string // remote: the resolved branch tip, for the dry run's Where
	// remoteBuilder is the value Send hands sendRemote for a remote binding:
	// --candidate's canonical token when the argument resolved to one, or the
	// argument unchanged ("" included) when it did not.
	remoteBuilder string
}

// sendAfterSpawn is a test seam: nil in production. When non-nil, Send calls
// it right after startRound succeeds, and a non-nil return fails the send
// from that point. It exists only so a test can inject a post-spawn failure.
var sendAfterSpawn func(name string) error

// Send copies the mastermind's plan into relevo state and hands it to the builder
// as the prompt of a fresh process started in the binding's tree. It
// returns a SendResult describing the round and any between-rounds drift.
//
// The in-lock checks stay: they guard against a change between the preflight and the lock.
func Send(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (SendResult, error) {
	pf, err := sendPreflight(ctx, rt, name, file, opts)
	if err != nil {
		return SendResult{}, err
	}

	// A remote binding's preflight stops at the read-only checks; the round
	// itself is still shipped above the engine, which contacts the server.
	if pf.b.Builder.Remote() {
		return rt.RemoteSend.Send(ctx, rt, pf.b, pf.body, opts.Tier, pf.remoteBuilder, opts.Force)
	}

	// The baseline snapshot adds git objects, so it stays out of the
	// read-only preflight and is taken here, before the lock.
	baseline, baselineHead := capture.Baseline(ctx, captureDeps(rt), pf.b)
	repoBaseline := escapeRepoBaseline(ctx, rt, pf.b, baseline)
	hintRound := pf.b.Round

	// spawned is the process startRound launched, if any: the deferred
	// failure path after the lock stops a builder this send started but
	// could not finish recording.
	var spawned *spawn.ProcHandle

	var round int
	var driftLineOut string
	var pickLine string

	// The whole round advance is one critical section: the daemon rewrites this
	// same binding on every tick, and a lost update here would re-send a plan
	// the builder already has. Spawning a process does not wait on it, so
	// holding the lock across it costs milliseconds, not the length of a turn.
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := recheckSendGuards(ctx, rt, tx, name, opts, pf)
		if err != nil {
			return err
		}
		b, planPath, started, staged, err := stagePlanAndStart(ctx, rt, tx, name, b, pf, opts, baseline, baselineHead)
		spawned, round = started, staged
		if err != nil {
			return err
		}
		round, driftLineOut, pickLine, err = stampSentRound(ctx, rt, tx, b, planPath, pf, opts, baseline, baselineHead, repoBaseline, hintRound)
		return err
	})
	if err != nil {
		return SendResult{}, rollbackSpawnedBuilder(ctx, rt, name, spawned, round, err)
	}

	return SendResult{Round: round, Drift: driftLineOut, Pick: pickLine}, nil
}

func recheckSendGuards(ctx context.Context, rt Runtime, tx *store.Tx, name string, opts SendOptions, pf preflight) (store.Binding, error) {
	b, err := tx.Load(name)
	if err != nil {
		return store.Binding{}, err
	}
	// Re-check under the lock, exactly as the preflight's read-only check
	// did: the chain may have started between the two.
	if err := chain.RefuseRunningMember(tx, name); err != nil {
		return store.Binding{}, err
	}
	if b.State == store.StateBroken {
		return store.Binding{}, fmt.Errorf("binding %q is broken; rebind before sending", name)
	}
	if b.State == store.StatePaused {
		return store.Binding{}, fmt.Errorf("binding %q is paused; relevo bind --resume --name %s first", name, name)
	}
	if b.Round > b.RoundCap {
		return store.Binding{}, roundCapRefusal(name, b.RoundCap)
	}
	// One process per round: a previous round's
	// process still running means the human is early, not that relevo
	// should start a second builder in the same tree.
	if b.Builder.PID != 0 {
		if rt.Runner == nil {
			return store.Binding{}, fmt.Errorf("binding %q: %w", name, spawn.ErrRunnerUnavailable)
		}
		alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
		if err != nil {
			return store.Binding{}, fmt.Errorf("binding %q: check previous process %d: %w", name, b.Builder.PID, err)
		}
		if alive {
			return store.Binding{}, fmt.Errorf("binding %q (pid %d): %w", name, b.Builder.PID, ErrBuilderBusy)
		}
	}

	// Re-check under the lock: the daemon may have closed the round
	// between the preflight and this lock, in which case b.Round here is
	// already the next round and its files do not exist, so the send
	// proceeds. The other order -- a marker appearing after the preflight
	// -- is what this guards: the round is over and nothing may be staged
	// or spawned for it.
	if path, found := pendingRoundFile(rt, name, b.Round); found {
		return store.Binding{}, fmt.Errorf("binding %q round %d: %s exists: %w", name, b.Round, filepath.Base(path), ErrReportPending)
	}

	// --candidate is re-applied under the lock, against the fresh binding:
	// the preflight's resolution must not be trusted over a round that
	// opened in between. The re-check writes nothing when it fires.
	// A re-pick is not a --candidate change, so it skips the refusal.
	if pf.pick != nil {
		if pf.repickFrom == "" {
			entries, err := tx.ReadLog(name)
			if err != nil {
				return store.Binding{}, err
			}
			if candidateSendRefused(b, entries) {
				return store.Binding{}, &CandidateRoundOpenError{Name: name, Round: b.Round}
			}
		}
		b, err = applyBuilder(b, *pf.pick, rt.RoleRegistry(), rt.Policy, opts.AllowYolo)
		if err != nil {
			return store.Binding{}, err
		}
	}

	if opts.Tier != "" {
		b.RoundTier = opts.Tier
	}

	// The round before this one must not still hold a scope: a supervisor
	// that died before its own reap leaves one behind, and the new round
	// would then run beside it. Nothing is staged yet, so a refusal here
	// leaves no plan file and no NEEDS YOU.
	if err := endEarlierRoundScope(ctx, rt, b); err != nil {
		return store.Binding{}, err
	}
	return b, nil
}

func stagePlanAndStart(ctx context.Context, rt Runtime, tx *store.Tx, name string, b store.Binding, pf preflight, opts SendOptions, baseline, baselineHead string) (store.Binding, string, *spawn.ProcHandle, int, error) {
	planPath := rt.Store.PromptPath(name, b.Round)
	reportPath := rt.Store.ReportPath(name, b.Round)
	donePath := rt.Store.DonePath(name, b.Round)
	if err := StagePlan(planPath, pf.body); err != nil {
		return store.Binding{}, "", nil, 0, fmt.Errorf("stage plan at %s: %w", planPath, err)
	}

	text := composePrompt(rt, b, planPath, reportPath, donePath)

	deferred := opts.Defer

	if !deferred {
		// A live scope for this round means a builder for it is already
		// alive -- most likely an earlier send started one whose writes
		// failed. Refuse before spawning a second one; nothing is
		// saved and the staged plan is removed, like the tier-unsupported
		// branch below.
		if unit := scopeUnitName(b); scopeRunning(ctx, rt, unit) {
			_ = os.Remove(planPath)
			return store.Binding{}, "", nil, 0, fmt.Errorf("binding %q round %d: scope %s.scope is still running -- a builder for this round is already alive (an earlier send may have started it); inspect it with systemctl --user status %s.scope, and relevo stop %s ends it: %w",
				name, b.Round, unit, unit, name, ErrScopeActive)
		}
		// A reader round runs in a throwaway scratch worktree, never in
		// b.CWD. Create it from the round's captured baseline
		// before anything is spawned; if it cannot be created, the round
		// does not start, and there is never a fallback to b.CWD.
		if b.Shape == store.ShapeReader {
			if _, err := housekeep.CreateScratchFrom(ctx, HousekeepDeps(rt), b, b.Round, baselineHead, baseline); err != nil {
				return store.Binding{}, "", nil, 0, err
			}
		}
		started, err := startRound(ctx, rt, tx, b, text, opts.AllowYolo)
		if err != nil {
			if errors.Is(err, harness.ErrTierUnsupported) || errors.Is(err, harness.ErrExtraArgsPermission) {
				_ = os.Remove(planPath)
				return store.Binding{}, "", nil, 0, err
			}
			// The plan is staged and the round is open; nothing was
			// started. NEEDS YOU says so in status, and the ledger's
			// spawn_failed (written by startRound) gates the candidate
			// for the next pick.
			b.State = store.StateNeedsYou
			b.Halt = spawnHaltText(err)
			b.HaltAt = rt.Now().UTC()
			// The builder change is still recorded first, so the log
			// explains why the round was sent to the new candidate.
			var failEntries []store.LogEntry
			if pf.pick != nil {
				failEntries = append(failEntries, resolve.PickEntry(rt.Now().UTC(), b.Round, "builder", *pf.pick))
			}
			if saveErr := tx.SaveWithLog(b, failEntries...); saveErr != nil {
				return store.Binding{}, "", nil, 0, fmt.Errorf("%w; and saving NEEDS YOU failed: %w", err, saveErr)
			}
			return store.Binding{}, "", nil, 0, err
		}
		b = started
		h := handleOf(started.Builder)
		spawned := &h
		if sendAfterSpawn != nil {
			if err := sendAfterSpawn(name); err != nil {
				return store.Binding{}, "", spawned, b.Round, err
			}
		}
		return b, planPath, spawned, b.Round, nil
	}
	return b, planPath, nil, 0, nil
}

func stampSentRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, planPath string, pf preflight, opts SendOptions, baseline, baselineHead, repoBaseline string, hintRound int) (int, string, string, error) {
	// pending collects this round's log entries, so the binding and all of
	// them are written in one transaction.
	var pending []store.LogEntry

	late := false

	// The pick entry is filed under the new round, before its plan entry,
	// so builderForRound attributes the round to the new builder. It goes
	// after startRound on purpose: an ErrTierUnsupported /
	// ErrExtraArgsPermission early return saves nothing, so a builder
	// change that did not happen must not be recorded.
	var pickLine string
	if pf.pick != nil {
		pending = append(pending, resolve.PickEntry(rt.Now().UTC(), b.Round, "builder", *pf.pick))
		if pf.repickFrom != "" {
			pickLine = repickLine(rt, pf.repickFrom, pf.repickWhy, *pf.pick)
		} else {
			pickLine = resolve.PickText("builder", *pf.pick, rt.Candidates)
		}
	}

	entry := store.LogEntry{TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToBuilder, Kind: store.KindPrompt, Path: planPath, Confirmed: true, Late: late, Tier: string(resolve.EffectiveTier(b))}
	pending = append(pending, entry)

	driftLine := ""
	if b.Shape != store.ShapeReader && b.Round == hintRound {
		res := capture.Drift(ctx, captureDeps(rt), tx, b, baseline)
		if (res.Available && !res.Stat.Empty()) || res.Reason != "" {
			driftEntry := store.LogEntry{TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindDrift, Path: res.Path, Note: capture.DriftSummary(res), Confirmed: true}
			pending = append(pending, driftEntry)
			driftLine = capture.DriftLine(res, b.Name, b.Round)
		}
	}

	round := b.Round
	b.RoundBaselineTree = baseline
	b.RoundBaselineHead = baselineHead
	b.RoundRepoBaselineTree = repoBaseline
	b.RoundClosedTree = ""
	if opts.Defer {
		b.QueuedAt = rt.Now().UTC()
	} else {
		b.RoundStartedAt = rt.Now().UTC()
	}
	b.FinishPending = true
	b.State = store.StateActive
	// The halt that stopped the previous attempt goes with the attempt, all
	// of it: reason, stamp and the kind naming its episode. A human re-send
	// is a fresh attempt, so the next halt in this round notifies again.
	b = ClearHaltFields(b)
	// An owed notification goes with the halt it was about: the re-send is
	// the human answering that halt, so the entry it still owes would land
	// under a round the human has already moved past.
	b.OwedHalt = nil
	b.RoundSwitches = 0
	b.RoundExcluded = nil
	b.RoundOOMKills = 0
	// A fresh send is a fresh process: any stall stamp from the previous
	// round is gone, and so is the whole progress clock -- the
	// tree, the output and the stale stamp all describe the round that
	// just ended.
	b.StalledSince = time.Time{}
	// A new round supersedes any stop requested for the old one:
	// the builder is being asked to work again, not to wrap up.
	b.StopRequestedAt = time.Time{}
	b.StopGraceMS = 0
	// A new round moves the branch again, so the last land belongs to the
	// previous round: status stops saying "landed" here.
	b.LandedAt = time.Time{}
	b.LandedPR = ""
	b.Progress = nil
	b.ExploringSince = time.Time{}
	b.StaleSince = time.Time{}
	// A human send is a fresh attempt, so the repair bookkeeping from the
	// old rounds says nothing about this one: the budget
	// starts unspent and no previous failure is held against the builder.
	b.RepairCount = 0
	b.LastGateSig = ""
	if opts.Regate != nil {
		b.Regate = *opts.Regate
	}

	// Whether this round gets a reviewer at its close: the flag,
	// else policy.json verify.default. Persisted with the round, and
	// cleared by queueReport once the close has acted on it. A reader
	// takes no verify, whatever the policy says.
	if b.Shape == store.ShapeReader {
		b.RoundVerify = false
	} else if opts.Verify != nil {
		b.RoundVerify = *opts.Verify
	} else {
		b.RoundVerify = rt.Policy.VerifyDefault()
	}

	if err := tx.SaveWithLog(b, pending...); err != nil {
		return 0, "", "", err
	}
	return round, driftLine, pickLine, nil
}

func rollbackSpawnedBuilder(ctx context.Context, rt Runtime, name string, spawned *spawn.ProcHandle, round int, sendErr error) error {
	if spawned == nil {
		return sendErr
	}
	// The process is running but the send could not record it: stop
	// it, and say so in its log, before returning. The binding
	// and its log entries are one transaction, so nothing of this
	// round was recorded: the previous state stands and the mastermind
	// may resend.
	stream := ""
	if round > 0 {
		stream = rt.Store.StreamPath(name, round)
	}
	kerr := rt.Runner.Kill(context.WithoutCancel(ctx), *spawned, stream)
	if kerr != nil {
		return fmt.Errorf("%w; and stopping the builder it started (pid %d) failed: %w", sendErr, spawned.PID, kerr)
	}
	// The rollback killed the round's process outside stopProcess, so
	// it removes the temp dir the start prepared.
	housekeep.RemoveRoundTmp(HousekeepDeps(rt), store.Binding{Name: name}, round)
	return fmt.Errorf("%w; the builder it started (pid %d) was stopped", sendErr, spawned.PID)
}
