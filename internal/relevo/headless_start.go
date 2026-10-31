package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// startRound starts the round's process for a headless binding and records
// its handle on the endpoint. The caller holds the state lock, has staged
// the plan, and saves what comes back.
//
// It is the round's prologue -- CPU assignment, candidate lookup, argv -- and
// the spawn itself is startProcess, which resumeRound shares.
//
// Preconditions:  b.Builder.Headless(); no live process on the endpoint
// (Send checks with Runner.Alive first); rt.Runner non-nil; the stored tier is
// at or below max_tier unless allowYolo.
// Postconditions: on success PID, StartedAt and LogPath describe the new
// process. On failure the endpoint is returned as it was (cursor moved to this
// round), PID 0, and the candidate's spawn_failed is in the ledger -- the same
// record a pane spawn failure leaves, because it is the same failure: the
// candidate could not be launched. The caller decides the binding's state.
func startRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, prompt string, allowYolo bool) (store.Binding, error) {
	if rt.Runner == nil {
		return b, spawn.ErrRunnerUnavailable
	}
	b = housekeep.AssignRoundCPU(HousekeepDeps(rt), tx, b)
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	role, err := resolve.BindingSpec(rt.RoleRegistry(), b, c.Harness)
	if err != nil {
		return b, fmt.Errorf("binding %q builder: %w", b.Name, err)
	}
	tier, err := resolve.LaunchTier(b, rt.Policy, allowYolo)
	if err != nil {
		return b, err
	}
	prompt, err = launchPrompt(rt, b, c.Harness, tier, prompt, true)
	if err != nil {
		return b, err
	}
	argv, err := candidate.HeadlessLaunch(c, role, tier, roundBudget(b), prompt, housekeep.RoundTree(HousekeepDeps(rt), b), rt.Store.OutDir(b.Name), rt.Store.PromptPath(b.Name, b.Round), chain.Dir(rt.Store, tx, b.Name))
	if err != nil {
		return b, err
	}
	return startProcess(ctx, rt, tx, b, argv, c)
}

// spawnFailure marks an error that came from Runner.Start refusing to launch
// the process. Its text is its wrapped error's; the type is what lets the
// lost builder's resume branch tell "this harness would not start" -- worth
// one fresh attempt -- from "this candidate cannot be rendered", which a
// fresh attempt would fail the same way.
type spawnFailure struct{ err error }

func (e spawnFailure) Error() string { return e.err.Error() }
func (e spawnFailure) Unwrap() error { return e.err }

// legacyLog reports whether a round has a builder log on disk: a round from
// before the stream change has one, opened for stderr at spawn, and nothing
// creates one now. Pure apart from one os.Stat.
func legacyLog(rt Runtime, name string, round int) bool {
	_, err := os.Stat(rt.Store.BuilderLogPath(name, round))
	return err == nil
}

// startProcess is the spawn half of a headless round, shared by startRound
// and resumeRound: argv is the complete command line, already built by
// the caller, and c is the candidate it was built for. It does the
// bookkeeping a new process needs -- the stream cursor, the ProcSpec with its
// scope, Runner.Start, the spawn-failure ledger record, the Watched mark --
// and returns the endpoint carrying the new PID, StartedAt and LogPath.
//
// Preconditions: rt.Runner non-nil (both callers check it before building
// argv); argv is a complete command line. Postconditions: as startRound's.
func startProcess(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, argv []string, c candidate.Candidate) (store.Binding, error) {
	return startProcessResuming(ctx, rt, tx, b, argv, c, false)
}

// startProcessResuming is startProcess for a caller that knows whether the
// spawn resumes an existing session. A resume requires its scope when one is
// configured: silently losing it would leave the builder where a daemon
// restart kills it, so the spawn refuses and the caller falls through to its
// exit path exactly as it does for any other resume Start error.
func startProcessResuming(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, argv []string, c candidate.Candidate, resume bool) (store.Binding, error) {
	// A writer's git shim must exist before the process can start; a write
	// failure refuses the round with nothing spawned.
	shimEnv, err := gitShimEnv(rt, b)
	if err != nil {
		return b, err
	}
	// The harness's writable root is <binding>/out; create it before the
	// process starts so the report, done marker and artifacts all land there.
	if err := rt.Store.EnsureOutDir(b.Name); err != nil {
		return b, fmt.Errorf("ensure out dir for %q: %w", b.Name, err)
	}
	if b.Builder.StreamRound != b.Round {
		// A new round is a new stream file; a mid-round switch (same
		// round) keeps rendering the file both processes append to.
		b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 0
		b.Builder.StreamSegments = nil
	}
	if fi, err := os.Stat(rt.Store.StreamPath(b.Name, b.Round)); err == nil {
		b.Builder.StreamStart = fi.Size()
	} else {
		b.Builder.StreamStart = 0
	}
	// Record which harness wrote from here on, so the drain renders each line
	// with its own process's kind. A retried spawn at the same offset replaces
	// its failed predecessor's entry rather than stacking a second segment
	// there.
	seg := store.StreamSegment{Start: b.Builder.StreamStart, Kind: b.Builder.Kind}
	if n := len(b.Builder.StreamSegments); n > 0 && b.Builder.StreamSegments[n-1].Start == seg.Start {
		b.Builder.StreamSegments[n-1] = seg
	} else {
		b.Builder.StreamSegments = append(b.Builder.StreamSegments, seg)
	}
	// Record the round's segment list as a round file, so a later round can
	// still render this round's stream with the right harness per process.
	// A failure here is never a spawn failure: tests
	// with no saved record hit ErrNotFound, which is fine.
	if tx != nil {
		body, err := json.Marshal(b.Builder.StreamSegments)
		if err == nil {
			err = tx.PutRoundFile(b.Name, b.Round, rt.Store.BuilderSegmentsPath(b.Name, b.Round), body)
		}
		if err != nil {
			slog.Warn("record stream segments", "binding", b.Name, "round", b.Round, "err", err)
		}
	}
	// A new process announces its own session on its own stream;
	// drainStream fills this in again from the first line it writes.
	b.Builder.StreamSessionID = ""
	// stderr joins the round's stream, as it does for consults and gates:
	// LogPath == StreamPath for a new round. A round that already had
	// a NNN-builder.log when the process started keeps writing stderr
	// to that log instead.
	logPath := rt.Store.StreamPath(b.Name, b.Round)
	if legacyLog(rt, b.Name, b.Round) {
		logPath = rt.Store.BuilderLogPath(b.Name, b.Round)
	}
	// A reader round always runs in its scratch worktree. The round's
	// start (Send or Admit) created it from the captured baseline; a
	// mid-round switch, a nudge or a relaunch reuses it. If a crash took it
	// away, recreate it from the round's own baseline before launching: a
	// missing scratch fails the round rather than running in the binding's
	// tree, and never falls back to b.CWD.
	if b.Shape == store.ShapeReader {
		if _, err := os.Stat(housekeep.RoundTree(HousekeepDeps(rt), b)); err != nil {
			if _, err := housekeep.CreateScratchFrom(ctx, HousekeepDeps(rt), b, b.Round, b.RoundBaselineHead, b.RoundBaselineTree); err != nil {
				return b, err
			}
		}
	}
	// The round's process gets its own temp dir through the one prepare seam
	// before it can start. A prepare error is the host's, not the
	// candidate's: it comes back plain -- never a spawnFailure, never a
	// ledger record -- so a failed mkdir gates nothing and spends no
	// relaunch attempt.
	tmpDir, prepEnv, cleanupProc, err := prepareProc(ctx, rt, procRequest{
		kind: procKindRound, key: b.Name, tree: housekeep.RoundTree(HousekeepDeps(rt), b), binding: &b,
	})
	if err != nil {
		return b, err
	}
	spec := spawn.ProcSpec{
		Dir: housekeep.RoundTree(HousekeepDeps(rt), b), Argv: argv,
		Env:        append(append(roundEnvFor(b, accountFor(rt, c)), shimEnv...), prepEnv...),
		TmpDir:     tmpDir,
		LogPath:    logPath,
		StreamPath: rt.Store.StreamPath(b.Name, b.Round),
	}
	spec.Scope = scopeFor(rt, scopeRound, scopeUnitName(b), housekeep.CPUPinText(b))
	// With no scope template there is no scope to require, and a fresh send
	// never requires one either, so a failed probe still runs it unscoped.
	spec.RequireScope = resume && rt.Scope != nil
	h, err := rt.Runner.Start(ctx, spec)
	if err != nil {
		// Nothing started, so the directory the preparation made goes back;
		// a later start for this round recreates it under the same name.
		cleanupProc()
		// A tenant-boundary refusal is the operator's to fix, not the
		// candidate's: it neither gates the candidate nor is a spawnFailure
		// (which the lost-builder path would answer with a fresh relaunch).
		//
		// A scope collision is the host's, for the same reason and with the
		// same consequence. freeRoundScope is the primary guard -- it
		// ends the unit before a switch's replacement asks for it -- but a
		// scope that loads between the probe and the Start still arrives here
		// as an ordinary error, and a runner that fails to *start* for a host
		// reason has not proved the candidate cannot run. Gating it would gate
		// a candidate that never ran, and the follow-on halt would then read
		// "cannot switch: every candidate ... is gated". Returning a plain,
		// non-spawnFailure error also keeps the lost-builder path from spending
		// its one fresh attempt on a resume that would collide the same way.
		if errors.Is(err, spawn.ErrBoundarySetup) || isScopeBusy(err) || errors.Is(err, spawn.ErrScopeUnavailable) {
			return b, fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)
		}
		availability.RecordSpawnFailureLocked(AvailabilityDeps(rt), c.Ref().String(), b.Name, err)
		return b, spawnFailure{fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)}
	}
	// A successful Start marks its handle as seen alive, so a later tick
	// never judges it lost to a restart.
	rt.Watched.Mark(h.PID, h.StartedAt.Unix())
	b.Builder.PID = h.PID
	b.Builder.StartedAt = h.StartedAt.Unix()
	b.Builder.LogPath = logPath
	return b, nil
}

// resumeRound continues the round's builder in the session it announced
// before it was lost to a daemon restart: it resolves the
// candidate exactly as startRound does, rebuilds the round's Launch for the
// same candidate and tier, renders its argv through ResumeBuild -- which
// appends the harness's resume selector -- and hands that to startProcess.
//
// It has startRound's pre- and postconditions. ErrResumeUnsupported comes
// back wrapped and unchanged, so the caller can fall back to a fresh
// relaunch.
func resumeRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, sessionID, prompt string, allowYolo bool) (store.Binding, error) {
	if rt.Runner == nil {
		return b, spawn.ErrRunnerUnavailable
	}
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	// The session only resolves under the login that wrote it, so a row a
	// human moved meanwhile is put back before the harness resumes.
	if b.BuilderAccount != "" {
		pick := resolve.PickFor(ResolveDeps(rt))
		if rec, ok := accountByName(pick.Set, b.BuilderAccount); ok {
			ensureOpencodeActive(ctx, rt, rec, pick.Gates)
		}
	}
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return b, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	role, err := resolve.BindingSpec(rt.RoleRegistry(), b, c.Harness)
	if err != nil {
		return b, fmt.Errorf("binding %q builder: %w", b.Name, err)
	}
	tier, err := resolve.LaunchTier(b, rt.Policy, allowYolo)
	if err != nil {
		return b, err
	}
	prompt, err = launchPrompt(rt, b, c.Harness, tier, prompt, true)
	if err != nil {
		return b, err
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier, c.ContributorMode)
	if err != nil {
		return b, err
	}
	if l.PromptAt < 0 {
		return b, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	definition, err := candidate.Definition(l, c.Harness, role)
	if err != nil {
		return b, err
	}
	sel, err := h.ResumeBuild(sessionID, l, prompt, roundBudget(b), housekeep.RoundTree(HousekeepDeps(rt), b), rt.Store.OutDir(b.Name), rt.Store.PromptPath(b.Name, b.Round), chain.Dir(rt.Store, tx, b.Name), definition)
	if err != nil {
		return b, err
	}
	argv := append([]string{h.Binary}, sel...)
	return startProcessResuming(ctx, rt, tx, b, argv, c, true)
}
