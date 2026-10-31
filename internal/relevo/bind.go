package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/workflow"
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

// BindResolved ties the calling mastermind to a builder over one working tree.
// The second return is how the builder was chosen, zero when a pane was
// adopted.
func BindResolved(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, resolve.Resolution, error) {
	rec, haveRec, err := resolveVerbMasterMind(rt, opts.MasterMindID)
	if err != nil {
		return store.Binding{}, resolve.Resolution{}, err
	}
	if opts.CWD == "" {
		return store.Binding{}, resolve.Resolution{}, refusal.BadInputf("no working directory")
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
		return worktreeOutcome{Kept: b.Worktree, Reason: "dirty check failed: " + text.Brief(err)}
	}
	if dirty {
		return worktreeOutcome{Kept: b.Worktree, Reason: "uncommitted changes"}
	}

	if dryRun {
		return worktreeOutcome{Removed: b.Worktree}
	}

	if err := rt.Git.RemoveWorktree(ctx, b.CWD, b.Worktree, false); err != nil {
		return worktreeOutcome{Kept: b.Worktree, Reason: text.Brief(err)}
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
var ErrWorktreeKept = refusal.Sentinel("worktree kept")

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

// housekeepTeardown adapts the engine's worktree teardown to the plain values
// housekeep.GC reports.
func housekeepTeardown(rt Runtime) func(context.Context, store.Binding, bool) housekeep.TeardownOutcome {
	return func(ctx context.Context, b store.Binding, dryRun bool) housekeep.TeardownOutcome {
		o := worktreeTeardown(ctx, rt, b, dryRun)
		return housekeep.TeardownOutcome{Removed: o.Removed, Kept: o.Kept, Reason: o.Reason, Gone: o.Gone}
	}
}

// housekeepAskUnbind adapts the engine's server ask to housekeep.GC's ask.
func housekeepAskUnbind(rt Runtime) func(context.Context, store.Binding, bool) error {
	return func(ctx context.Context, b store.Binding, force bool) error {
		return askServerUnbind(ctx, rt, b, force)
	}
}

// HousekeepDeps builds internal/housekeep's Deps from rt. Git and Refs carry
// through nil-safe: a nil rt.Git converts to a nil interface on both, since
// the assignments are between identical interface shapes.
func HousekeepDeps(rt Runtime) housekeep.Deps {
	return housekeep.Deps{Store: rt.Store, TmpRoot: rt.TmpRoot, Git: rt.Git, Refs: rt.Git,
		ChainCheckSettled: chainCheckSettled, ChainFlowTerminal: chainFlowTerminal,
		GateUnit:        func(o, n string, r int) string { return scopeUnitNameFor(scopeGate, o, n, r, "") },
		ServedCheckUnit: func(b store.Binding, run store.CheckRun) string { return servedCheckSpec(rt, b, run).UnitName },
		ChainCheckUnit:  func(c db.ChainRow, run int) string { return chainCheckSpec(rt, c, run, "", "").UnitName },
		Teardown:        housekeepTeardown(rt),
		AskUnbind:       housekeepAskUnbind(rt),
		ErrKept:         ErrWorktreeKept,
		KeptReason:      KeptReasonOf,
		Abandon:         abandonSession,
		ClearProcess:    clearProcess,
		Halt:            housekeepHalt(rt),
		Preflight:       housekeepPreflight(rt),
		ShowCommand:     text.ShowCommand,
		Admit:           housekeepAdmit(rt),
		NotQueued:       ErrNotQueued,
		AllowedCPUs:     housekeepAllowedCPUs(rt),
		HeldCPUs:        rt.HeldCPUs,
		Scope:           rt.Scope,
		UnitName:        scopeUnitName,
		ScopeProber:     housekeepScopeProber(rt),
		Now:             housekeepNow(rt),
		HaltChainEnv:    housekeepHaltChainEnv(rt)}
}

// housekeepHalt adapts the engine's halt-and-settle to housekeep's halt.
func housekeepHalt(rt Runtime) func(context.Context, *store.Tx, store.Binding, string) (store.Binding, error) {
	return func(ctx context.Context, tx *store.Tx, b store.Binding, message string) (store.Binding, error) {
		return haltAndSettle(ctx, rt, tx, b, message)
	}
}

// housekeepPreflight adapts the engine's disk preflight to housekeep's probe.
func housekeepPreflight(rt Runtime) func(string) error {
	return func(tree string) error { return preflightDisk(rt, tree) }
}

// housekeepAdmit adapts the engine's queued-round admission to housekeep's admit.
func housekeepAdmit(rt Runtime) func(context.Context, string) error {
	return func(ctx context.Context, name string) error { return Admit(ctx, rt, name) }
}

// housekeepAllowedCPUs reads the runtime scope's allowed_cpus: empty with
// scopes off, so the round runs unpinned.
func housekeepAllowedCPUs(rt Runtime) string {
	if rt.Scope == nil {
		return ""
	}
	return rt.Scope.AllowedCPUs
}

// housekeepScopeProber is the runner's journal probe when it has one: a runner
// without it never reports an oom kill.
func housekeepScopeProber(rt Runtime) spawn.ScopeResultProber {
	p, _ := rt.Runner.(spawn.ScopeResultProber)
	return p
}

// housekeepNow is the runtime's clock, or the wall clock on a runtime with none.
func housekeepNow(rt Runtime) func() time.Time {
	if rt.Now != nil {
		return rt.Now
	}
	return time.Now
}

// housekeepHaltChainEnv runs the engine's chain halt for an environment note:
// the definition and state reads, the cloned halted state and the terminal
// transition, for the check row the caller already loaded.
func housekeepHaltChainEnv(rt Runtime) func(context.Context, *store.Tx, db.ChainRow, db.ChainCheckRow, workflow.Event) (bool, error) {
	return func(ctx context.Context, tx *store.Tx, c db.ChainRow, row db.ChainCheckRow, ev workflow.Event) (bool, error) {
		def, err := chain.WorkflowDef(c)
		if err != nil {
			return false, err
		}
		before, err := chain.WorkflowState(c)
		if err != nil {
			return false, err
		}
		next := cloneFlowState(before)
		next.Status = workflow.StatusHalted
		next.Reason = fmt.Sprintf("check %s: %s", row.Step, row.Note)
		return true, chainTerminalWF(ctx, rt, tx, c, def, before, next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
	}
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
	if err := chain.RefuseRunningMemberStore(rt.Store, name); err != nil {
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
	housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round)

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
