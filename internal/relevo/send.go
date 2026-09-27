package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// builderPrompt is the fixed handoff template. It names both paths explicitly
// because alternate-screen output is unrecoverable, so the report must be a
// file rather than something relevo reads off the terminal. The marker is the
// builder's own "the tree is final": relevo closes the round on it, not on the
// report appearing (spec 2026-09-12-completion-marker §1).
//
// It opens by naming the working tree and a halt rule (#192): a headless
// agy builder has been observed to run its shell somewhere else and execute
// a round against the planner's main checkout instead of its own worktree.
// Telling the builder which tree is its own, and to check with `git status`
// before doing anything else, is relevo's second line of defence alongside
// pinning the process's workspace with --add-dir.
const builderPrompt = `Your working tree is: %s
It is the only tree you may touch. Before anything else, run ` + "`git status`" + `
there. If that fails, or reports a different directory or branch than you
expect for this tree, stop: write a report saying so, create the done marker,
and do nothing else.

Read: %s
When you are done, write your report to: %s
Then, as the very last thing you do -- after every edit, test and commit --
create this empty file: %s
End the report with this block as its last lines, filled in honestly:

` + "```relevo" + `
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # commands you ran, e.g. ["make check"]
not_done: []            # adjacent work you deliberately left
` + "```" + `
Reply here with only the report path.`

// readerPrompt is a reader round's handoff template (A5 R4a): the reader works
// in a throwaway scratch copy, writes only into its artifact directory, and its
// final message is the actor's output label, saved as that directory's
// summary.md. The first line is exactly `Your working tree is: ` because the
// e2e fake parses it, as it does for builderPrompt.
//
// It is a format string: scratch tree, b.CWD, plan path, artifact dir, output
// label, summary path, done marker.
const readerPrompt = `Your working tree is: %s
It is a throwaway copy of %s for this round: read anything in it, run
anything read-only, change nothing you need to keep -- it is discarded when
the round ends, and nothing in it is ever committed.

Read: %s
Write every file you produce into this directory (create it): %s
Your final message is your %s: it is saved as %s.
End that final message with this block, filled in honestly:

` + "```relevo" + `
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # files you wrote into the artifact directory
commands_run: []        # commands you ran
not_done: []            # what you deliberately left
` + "```" + `
Then create this empty file: %s
Your final message comes after it: relevo saves it once you finish.`

// SendResult is what one successful Send produced.
type SendResult struct {
	Round int    // the round the plan was filed under
	Drift string // the drift line for stdout, or "" when there is nothing to say
	// Pick is the pick line when SendOptions.Builder changed the builder (the
	// KindPick note), or "" otherwise. For the CLI to print.
	Pick string
}

// SendOptions is what a send may add to the plan file (#141).
type SendOptions struct {
	Tier      string // "" means the binding's Tier; else a one-round override (headless only)
	AllowYolo bool
	// Builder is a candidate token that persists as the binding's builder from
	// this round on, until another --candidate or a mid-round switch changes it.
	// "" means the binding's current builder. In contrast to Tier, it is not a
	// one-round override: the issue's failure was a plain send silently going
	// to the builder the binding was bound to (#318).
	Builder string
	// Regate sets the binding's repair-round budget (#132 part 2); nil leaves
	// it unchanged.
	Regate *int
	// Verify marks the round for a read-only reviewer at round close (#144).
	// nil takes policy.json verify.default, so a plain send honours the
	// planner's configured default and --verify/--no-verify overrides it.
	Verify *bool
	// Defer stages the round -- plan written, log entry appended, State ==
	// active -- but does not spawn a builder; the caller (serve.admit or
	// relevo.Admit) starts it later (#285, server only).
	Defer bool
}

// preflight is everything Send checks before it takes the state lock and
// writes: the plan bytes, the effective tier, the headless launch argv, the
// paths and the composed prompt. sendPreflight computes it read-only; Send and
// SendDryRun both call it, so a dry run can never disagree with a real send
// about the state of the world (#149). A failed precondition is an error in
// Send's exact wording.
type preflight struct {
	b    store.Binding      // the binding as loaded (read-only; Send re-loads under the lock)
	body []byte             // the plan file's bytes
	tier harness.Tier       // effective tier for this round (opts.Tier parsed, or effectiveTier(b))
	argv []string           // headless: headlessLaunch's argv (proves the launch is well-formed); nil for remote
	gate *availability.Gate // advisory: a gate on b.BuilderCandidate (rate-limited or roles_missing), nil when none
	pick *Resolution        // --candidate's resolution to apply under the lock; nil when the builder does not change
	// staleToken is the binding's old BuilderCandidate when the preflight
	// re-picked because the token was stale; "" otherwise. When it is
	// non-empty, pick is non-nil.
	staleToken string

	planPath, reportPath, donePath string
	prompt                         string // composePrompt(...) -- computed, never sent

	remoteSHA string // remote: the resolved branch tip, for the dry run's Where
	// remoteBuilder is the value Send hands sendRemote for a remote binding:
	// --candidate's canonical token when the argument resolved to one, or the
	// argument unchanged ("" included) when it did not.
	remoteBuilder string
}

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

// sendPreflight runs Send's read-only preconditions in Send's exact order and
// error wording. It makes no write: Store.Load takes the store lock briefly
// (it always has) but that is a read. CaptureBaseline is deliberately not here:
// it adds git objects. The remote path stops after the checks that need no
// server contact (no WhoAmI, no bundle); Send's remote branch calls sendRemote
// as it always did.
func sendPreflight(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (preflight, error) {
	// Read the caller's file first; it is the one input that does not depend
	// on binding state.
	body, err := os.ReadFile(file)
	if err != nil {
		return preflight{}, fmt.Errorf("read plan %s: %w", file, err)
	}

	var tier harness.Tier
	if opts.Tier != "" {
		t, err := harness.ParseTier(opts.Tier)
		if err != nil {
			return preflight{}, err
		}
		if err := checkTierCap(t, rt.Policy, opts.AllowYolo); err != nil {
			return preflight{}, err
		}
		tier = t
	}

	b, err := rt.Store.Load(name)
	if err != nil {
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
	// shape is checked here (§5.1).
	var pick *Resolution
	var staleToken string
	// remoteBuilder is what a remote binding's sendRemote is handed: the
	// canonical token when --candidate resolved to one, else the argument as
	// typed (A1 §4.2).
	remoteBuilder := opts.Builder
	if opts.Builder != "" {
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return preflight{}, err
		}
		if roundOpenIn(entries, b.Round) {
			return preflight{}, fmt.Errorf("binding %q has round %d open; relevo stop %s ends it, then send again with --candidate", name, b.Round, name)
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
			p, err := ResolveSendBuilderFor(rt, bindingRole(b), b.BuilderCandidate, opts.Builder)
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

	// A binding whose candidate was edited or deleted holds a stale token:
	// the configured set no longer serves the triple the running round is
	// on. Pick again by the actor's order, exactly as a switch does, so
	// every precondition below is computed against the builder this round
	// will actually use.
	if pick == nil && staleBuilder(rt, b) {
		old := b.BuilderCandidate
		var res *Resolution
		b, res, err = repickStale(rt, b, opts.AllowYolo)
		if err != nil {
			return preflight{}, err
		}
		pick = res
		staleToken = old
	}

	if tier == "" {
		tier = effectiveTier(b)
	}

	planPath := rt.Store.PlanPath(name, b.Round)
	reportPath := rt.Store.ReportPath(name, b.Round)
	donePath := rt.Store.DonePath(name, b.Round)
	prompt := composePrompt(rt, b, planPath, reportPath, donePath)

	pf := preflight{
		b: b, body: body, tier: tier,
		planPath: planPath, reportPath: reportPath, donePath: donePath,
		prompt: prompt, pick: pick, staleToken: staleToken,
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
		return preflight{}, fmt.Errorf("binding %q hit its round cap of %d", name, b.RoundCap)
	}

	// A headless builder (#99) is a process relevo starts per round, so the
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
	role, err := bindingSpec(rt, b, c.Harness)
	if err != nil {
		return preflight{}, fmt.Errorf("binding %q builder: %w", b.Name, err)
	}
	argv, err := spawn.HeadlessLaunch(c, role, tier, roundBudget(b), prompt, roundTree(rt, b), rt.Store.Dir(b.Name))
	if err != nil {
		return preflight{}, err
	}
	pf.argv = argv

	// The gate is advisory only: a gated candidate can still be sent to, it
	// just tells the human the daemon would switch away after the start.
	for _, g := range availability.Gates(AvailabilityDeps(rt)) {
		if g.Token == b.BuilderCandidate && (g.Kind == availability.RateLimited || g.Kind == availability.RolesMissing) {
			gate := g
			pf.gate = &gate
			break
		}
	}

	return pf, nil
}

// sendAfterSpawn is a test seam: nil in production. When non-nil, Send calls
// it right after startRound succeeds, and a non-nil return fails the send
// from that point. It exists only so a test can inject a post-spawn failure.
var sendAfterSpawn func(name string) error

// Send copies the planner's plan into relevo state and hands it to the builder
// as the prompt of a fresh process started in the binding's tree (#99). It
// returns a SendResult describing the round and any between-rounds drift.
//
// Every precondition that needs no lock lives in sendPreflight, which
// `relevo send --dry-run` calls too (#149). The in-lock checks stay: they guard
// against a change between the preflight and the lock.
func Send(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (SendResult, error) {
	pf, err := sendPreflight(ctx, rt, name, file, opts)
	if err != nil {
		return SendResult{}, err
	}

	// A remote binding's preflight stops at the read-only checks; the round
	// itself is still shipped by sendRemote, which contacts the server.
	if pf.b.Builder.Remote() {
		return sendRemote(ctx, rt, pf.b, pf.body, opts.Tier, pf.remoteBuilder)
	}

	// The baseline snapshot adds git objects, so it stays out of the
	// read-only preflight and is taken here, before the lock.
	baseline, baselineHead := capture.Baseline(ctx, captureDeps(rt), pf.b)
	hintRound := pf.b.Round

	// spawned is the process startRound launched, if any: the deferred
	// failure path after the lock stops a builder this send started but
	// could not finish recording (#436).
	var spawned *spawn.ProcHandle

	var round int
	var driftLineOut string
	var pickLine string

	// The whole round advance is one critical section: the daemon rewrites this
	// same binding on every tick, and a lost update here would re-send a plan
	// the builder already has. Spawning a process does not wait on it, so
	// holding the lock across it costs milliseconds, not the length of a turn.
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		// pending collects this round's log entries, so the binding and all of
		// them are written in one transaction (#471).
		var pending []store.LogEntry

		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		if b.State == store.StateBroken {
			return fmt.Errorf("binding %q is broken; rebind before sending", name)
		}
		if b.State == store.StatePaused {
			return fmt.Errorf("binding %q is paused; relevo bind --resume --name %s first", name, name)
		}
		if b.Round > b.RoundCap {
			return fmt.Errorf("binding %q hit its round cap of %d", name, b.RoundCap)
		}
		// One process per round (headless spec §5.2): a previous round's
		// process still running means the human is early, not that relevo
		// should start a second builder in the same tree.
		if b.Builder.PID != 0 {
			if rt.Runner == nil {
				return fmt.Errorf("binding %q: %w", name, spawn.ErrRunnerUnavailable)
			}
			alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if err != nil {
				return fmt.Errorf("binding %q: check previous process %d: %w", name, b.Builder.PID, err)
			}
			if alive {
				return fmt.Errorf("binding %q (pid %d): %w", name, b.Builder.PID, ErrBuilderBusy)
			}
		}

		// Re-check under the lock: the daemon may have closed the round
		// between the preflight and this lock, in which case b.Round here is
		// already the next round and its files do not exist, so the send
		// proceeds. The other order -- a marker appearing after the preflight
		// -- is what this guards: the round is over and nothing may be staged
		// or spawned for it.
		if path, found := pendingRoundFile(rt, name, b.Round); found {
			return fmt.Errorf("binding %q round %d: %s exists: %w", name, b.Round, filepath.Base(path), ErrReportPending)
		}

		// --candidate is re-applied under the lock, against the fresh binding:
		// the preflight's resolution must not be trusted over a round that
		// opened in between. The re-check writes nothing when it fires (§5.2).
		// A stale re-pick is not a --candidate change, so it skips the refusal.
		if pf.pick != nil {
			if pf.staleToken == "" {
				entries, err := tx.ReadLog(name)
				if err != nil {
					return err
				}
				if roundOpenIn(entries, b.Round) {
					return fmt.Errorf("binding %q has round %d open; relevo stop %s ends it, then send again with --candidate", name, b.Round, name)
				}
			}
			b, err = applyBuilder(b, *pf.pick, rt.RoleRegistry(), rt.Policy, opts.AllowYolo)
			if err != nil {
				return err
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
			return err
		}

		planPath := rt.Store.PlanPath(name, b.Round)
		reportPath := rt.Store.ReportPath(name, b.Round)
		donePath := rt.Store.DonePath(name, b.Round)
		if err := os.WriteFile(planPath, pf.body, 0o644); err != nil {
			return fmt.Errorf("stage plan at %s: %w", planPath, err)
		}

		text := composePrompt(rt, b, planPath, reportPath, donePath)

		// Defer stages the round without spawning: the caller (serve.admit
		// or relevo.Admit) starts the builder later (#285).
		deferred := opts.Defer

		late := false
		if !deferred {
			// A live scope for this round means a builder for it is already
			// alive -- most likely an earlier send started one whose writes
			// failed (#445). Refuse before spawning a second one; nothing is
			// saved and the staged plan is removed, like the tier-unsupported
			// branch below.
			if unit := scopeUnitName(b); scopeRunning(ctx, rt, unit) {
				_ = os.Remove(planPath)
				return fmt.Errorf("binding %q round %d: scope %s.scope is still running -- a builder for this round is already alive (an earlier send may have started it); inspect it with systemctl --user status %s.scope, and relevo stop %s ends it: %w",
					name, b.Round, unit, unit, name, ErrScopeActive)
			}
			// A reader round runs in a throwaway scratch worktree, never in
			// b.CWD (A5 §2, D6). Create it from the round's captured baseline
			// before anything is spawned; if it cannot be created, the round
			// does not start, and there is never a fallback to b.CWD.
			if b.Shape == store.ShapeReader {
				if _, err := CreateScratchFrom(ctx, rt, b, b.Round, baselineHead, baseline); err != nil {
					return err
				}
			}
			started, err := startRound(ctx, rt, tx, b, text)
			if err != nil {
				if errors.Is(err, harness.ErrTierUnsupported) || errors.Is(err, harness.ErrExtraArgsPermission) {
					_ = os.Remove(planPath)
					return err
				}
				// The plan is staged and the round is open; nothing was
				// started. NEEDS YOU says so in status, and the ledger's
				// spawn_failed (written by startRound) gates the candidate
				// for the next pick.
				b.State = store.StateNeedsYou
				b.Halt = "builder spawn failed: " + err.Error()
				b.HaltAt = rt.Now().UTC()
				// The builder change is still recorded first, so the log
				// explains why the round was sent to the new candidate.
				var failEntries []store.LogEntry
				if pf.pick != nil {
					failEntries = append(failEntries, pickEntry(rt.Now().UTC(), b.Round, "builder", *pf.pick))
				}
				if saveErr := tx.SaveWithLog(b, failEntries...); saveErr != nil {
					return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", err, saveErr)
				}
				return err
			}
			b = started
			h := handleOf(started.Builder)
			spawned = &h
			if sendAfterSpawn != nil {
				if err := sendAfterSpawn(name); err != nil {
					return err
				}
			}
		}

		// The pick entry is filed under the new round, before its plan entry,
		// so builderForRound attributes the round to the new builder. It goes
		// after startRound on purpose: an ErrTierUnsupported /
		// ErrExtraArgsPermission early return saves nothing, so a builder
		// change that did not happen must not be recorded.
		if pf.pick != nil {
			pending = append(pending, pickEntry(rt.Now().UTC(), b.Round, "builder", *pf.pick))
			if pf.staleToken != "" {
				pickLine = "note: builder " + pf.staleToken + " is no longer configured; " + PickText("builder", *pf.pick, rt.Candidates)
			} else {
				pickLine = PickText("builder", *pf.pick, rt.Candidates)
			}
		}

		entry := store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round,
			Direction: store.DirToBuilder, Kind: store.KindPlan,
			Path: planPath, Confirmed: true, Late: late,
			Tier: string(effectiveTier(b)),
		}
		pending = append(pending, entry)

		driftLine := ""
		if b.Shape != store.ShapeReader && b.Round == hintRound {
			res := capture.Drift(ctx, captureDeps(rt), tx, b, baseline)
			if (res.Available && !res.Stat.Empty()) || res.Reason != "" {
				driftEntry := store.LogEntry{
					TS: rt.Now().UTC(), Round: b.Round,
					Direction: store.DirToPlanner, Kind: store.KindDrift,
					Path: res.Path, Note: capture.DriftSummary(res),
					Confirmed: true,
				}
				pending = append(pending, driftEntry)
				driftLine = capture.DriftLine(res, b.Name, b.Round)
			}
		}

		round = b.Round
		driftLineOut = driftLine
		b.RoundBaselineTree = baseline
		b.RoundBaselineHead = baselineHead
		b.RoundClosedTree = ""
		if deferred {
			b.QueuedAt = rt.Now().UTC()
		} else {
			b.RoundStartedAt = rt.Now().UTC()
		}
		b.FinishPending = true
		b.State = store.StateActive
		b.Halt = ""
		b.HaltAt = time.Time{}
		// A human re-send is a fresh attempt: the next halt in this round
		// notifies again, and the round gets a full switch budget.
		b.HaltNotifiedRound = 0
		b.RoundSwitches = 0
		b.RoundExcluded = nil
		b.RoundOOMKills = 0
		// A fresh send is a fresh process: any stall stamp from the previous
		// round is gone (#252), and so is the whole progress clock -- the
		// tree, the output and the stale stamp all describe the round that
		// just ended (#135).
		b.StalledSince = time.Time{}
		// A new round supersedes any stop requested for the old one (#138):
		// the builder is being asked to work again, not to wrap up.
		b.StopRequestedAt = time.Time{}
		b.StopGraceMS = 0
		// A new round moves the branch again, so the last land no longer
		// describes it (#136): status stops saying "landed" here.
		b.LandedAt = time.Time{}
		b.LandedPR = ""
		b.Progress = nil
		b.ExploringSince = time.Time{}
		b.StaleSince = time.Time{}
		b.StaleNotifiedAt = time.Time{}
		// A human send is a fresh attempt, so the repair bookkeeping from the
		// old rounds says nothing about this one (#132 part 2): the budget
		// starts unspent and no previous failure is held against the builder.
		b.RepairCount = 0
		b.LastGateSig = ""
		if opts.Regate != nil {
			b.Regate = *opts.Regate
		}

		// Whether this round gets a reviewer at its close (#144): the flag,
		// else policy.json verify.default. Persisted with the round, and
		// cleared by queueReport once the close has acted on it. A reader
		// takes no verify (A5 §5), whatever the policy says.
		if b.Shape == store.ShapeReader {
			b.RoundVerify = false
		} else if opts.Verify != nil {
			b.RoundVerify = *opts.Verify
		} else {
			b.RoundVerify = rt.Policy.VerifyDefault()
		}

		return tx.SaveWithLog(b, pending...)
	})
	if err != nil {
		if spawned != nil {
			// The process is running but the send could not record it: stop
			// it, and say so in its log, before returning (#436). The binding
			// and its log entries are one transaction, so nothing of this
			// round was recorded: the previous state stands and the planner
			// may resend.
			kerr := rt.Runner.Kill(context.WithoutCancel(ctx), *spawned)
			if kerr != nil {
				return SendResult{}, fmt.Errorf("%w; and stopping the builder it started (pid %d) failed: %v", err, spawned.PID, kerr)
			}
			return SendResult{}, fmt.Errorf("%w; the builder it started (pid %d) was stopped", err, spawned.PID)
		}
		return SendResult{}, err
	}

	return SendResult{Round: round, Drift: driftLineOut, Pick: pickLine}, nil
}

// DryRun is what SendDryRun found: the round Send would open, the builder it
// would go to, the paths and the head of the prompt. It is a description only;
// nothing was written (#149).
type DryRun struct {
	Name      string `json:"name"`
	Round     int    `json:"round"`
	Mode      string `json:"mode"` // "headless" | "remote"
	Candidate string `json:"candidate"`
	// CandidateName is Candidate's short name (A1 §4.4). Empty when the
	// candidate is no longer configured, in which case Candidate is shown.
	CandidateName string   `json:"candidate_name,omitempty"`
	Where         string   `json:"where"`               // headless: the harness binary + first arg; remote: "server contabo, branch relevo/x @ <sha12>; server not contacted"
	GateNote      string   `json:"gate_note,omitempty"` // "rate-limited until 00:26; the daemon would switch after start" / "agents missing: ...; the daemon would switch after start"
	PlanPath      string   `json:"plan_path"`
	PlanFrom      string   `json:"plan_from"`
	PlanBytes     int64    `json:"plan_bytes"`
	ReportPath    string   `json:"report_path"`
	DonePath      string   `json:"done_path"`
	Tier          string   `json:"tier"`
	PromptHead    []string `json:"prompt_head"` // the prompt's first two non-empty lines
}

// SendDryRun checks every precondition Send checks and describes the round
// Send would open, without making a single write: no staged plan, no log
// entry, no Save, no Prompt, no Runner.Start, no baseline snapshot (#149). A
// failed precondition is the identical error Send would return for the same
// state, so a script can rely on the dry run as a gate.
func SendDryRun(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (DryRun, error) {
	pf, err := sendPreflight(ctx, rt, name, file, opts)
	if err != nil {
		return DryRun{}, err
	}

	d := DryRun{
		Name:       pf.b.Name,
		Round:      pf.b.Round,
		Mode:       dryRunMode(pf.b),
		Candidate:  pf.b.BuilderCandidate,
		Where:      dryRunWhere(pf),
		PlanPath:   pf.planPath,
		PlanFrom:   absoluteOr(file),
		PlanBytes:  int64(len(pf.body)),
		ReportPath: pf.reportPath,
		DonePath:   pf.donePath,
		Tier:       string(pf.tier),
		PromptHead: promptHead(pf.prompt),
	}
	// A1 §4.4, round 3 F3: CandidateName is set only when the set holds the
	// token; otherwise the text falls back to printing Candidate.
	if name, ok := rt.Candidates.NameFor(pf.b.BuilderCandidate); ok {
		d.CandidateName = name
	}
	if pf.gate != nil {
		d.GateNote = dryRunGateNote(pf.gate)
	}
	return d, nil
}

// dryRunMode names the builder's shape as the dry run prints it.
func dryRunMode(b store.Binding) string {
	if b.Builder.Remote() {
		return "remote"
	}
	return "headless"
}

// dryRunWhere is where the round would go: the headless argv that proves the
// launch is well-formed, or the remote server and the branch the plan would be
// shipped from.
func dryRunWhere(pf preflight) string {
	if pf.b.Builder.Remote() {
		sha := pf.remoteSHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		return fmt.Sprintf("server %s, branch %s @ %s; server not contacted", pf.b.Builder.Server, pf.b.Branch, sha)
	}
	if len(pf.argv) == 0 {
		return ""
	}
	if len(pf.argv) == 1 {
		return pf.argv[0]
	}
	return pf.argv[0] + " " + pf.argv[1]
}

// dryRunGateNote is the advisory sentence for a gated candidate: what the gate
// is, and that the daemon would switch the builder once the round started.
func dryRunGateNote(g *availability.Gate) string {
	if g.Kind == availability.RolesMissing {
		return g.Note + "; the daemon would switch after start"
	}
	return availability.GateKindText(g.Kind) + " " + availability.GateUntilText(g.Until) + "; the daemon would switch after start"
}

// promptHead is the prompt's first two non-empty lines: enough for a human to
// recognise the handoff without printing the whole template.
func promptHead(prompt string) []string {
	var out []string
	for _, line := range strings.Split(prompt, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// absoluteOr resolves path against the current directory when it can, so a dry
// run can report where the plan came from even when the caller typed a
// relative path.
func absoluteOr(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// composePrompt renders this round's handoff prompt: the reader template for a
// reader binding, the builder template for a writer. Line 1 is the origin line
// naming the round and actor, followed by a blank line and the handoff text
// (#139). rt is needed to name a reader's scratch tree, artifact dir and output
// label.
func composePrompt(rt Runtime, b store.Binding, planPath, reportPath, donePath string) string {
	origin := delivery.OriginLine(b.Name, b.Round, store.DirToBuilder, store.KindPlan)
	if b.Shape == store.ShapeReader {
		return origin + "\n\n" + readerPromptFor(rt, b, planPath, donePath)
	}
	body := fmt.Sprintf(builderPrompt, b.CWD, planPath, reportPath, donePath)
	return origin + "\n\n" + body
}

// readerPromptFor renders readerPrompt for one reader round. The artifact dir
// is the actor's round directory; the output label is the actor's resolved
// agent definition's label (ActorOutput), which defaults to "notes" when
// nothing names one. The definition falls back to the actor name when the
// role's spec cannot be resolved, so a shipped actor still gets its own label.
func readerPromptFor(rt Runtime, b store.Binding, planPath, donePath string) string {
	actor := bindingRole(b)
	artifactDir := rt.Store.ArtifactDir(b.Name, b.Round, actor)
	definition := actor
	if spec, err := bindingSpec(rt, b, b.Builder.Kind); err == nil {
		definition = spec.Definition
	}
	return fmt.Sprintf(readerPrompt, roundTree(rt, b), b.CWD, planPath, artifactDir,
		actorOutput(rt, actor, definition), artifactDir+"/summary.md", donePath)
}
