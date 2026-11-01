package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// procKind says which family of processes relevo prepares before it starts:
// a round's builder or reader, or a check's gate command. A process a human
// starts outside relevo carries no kind -- it is nobody's to prepare.
type procKind string

const (
	procKindRound procKind = "round"
	procKindCheck procKind = "check"
)

// procRequest is one process about to be started or relaunched. A round is
// named by its binding; a check has a key and no binding.
type procRequest struct {
	kind    procKind
	key     string
	tree    string
	binding *store.Binding
}

// prepareProc is the one seam where a process is prepared before it starts.
// It returns the temp directory for the ProcSpec's TmpDir, env entries, and a
// cleanup for when the start never happens: once running, the process's own
// route owns what was prepared. A runtime with no tmp root -- container mode,
// whose processes see the container's own /tmp -- prepares nothing and errors
// never.
func prepareProc(ctx context.Context, rt Runtime, req procRequest) (string, []string, func(), error) {
	if err := preflightDisk(rt, req.tree); err != nil {
		return "", nil, nil, err
	}
	dir, undo, err := prepareTmp(rt, req)
	if err != nil {
		return "", nil, nil, err
	}
	env, err := goFlagsEnv(ctx, rt, req.tree, req.binding)
	if err != nil {
		undo()
		return "", nil, nil, err
	}
	return dir, env, undo, nil
}

func prepareTmp(rt Runtime, req procRequest) (string, func(), error) {
	if rt.TmpRoot == "" {
		return "", func() {}, nil
	}
	var rel string
	var undo func()
	switch req.kind {
	case procKindRound:
		if req.binding == nil {
			return "", func() {}, nil
		}
		b := *req.binding
		rel = housekeep.RoundTmpRel(b.Name, b.Round)
		undo = func() { housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round) }
	case procKindCheck:
		if req.key == "" {
			return "", func() {}, nil
		}
		rel = checkTmpRel(req.key)
		undo = func() { removeCheckTmp(rt, req.key) }
	default:
		return "", func() {}, nil
	}
	dir, err := rt.Store.EnsureTmpEntry(rt.TmpRoot, rel)
	if err != nil {
		return "", nil, fmt.Errorf("tmp dir for %s %q: %w", req.kind, rel, err)
	}
	housekeep.WarnTmpOverBudget(dir)
	return dir, undo, nil
}

// checkTmpRel is one check process's private temp directory under a runtime's
// tmp root, named checks/<short>. The short id is deterministic in the unit
// the check already carries -- the sanitized scope unit name, unique per
// check run and stable across a restart re-run -- which is what lets the
// re-run find the directory its first run made.
func checkTmpRel(unit string) string {
	return filepath.Join(housekeep.SubChecks, housekeep.ShortCheckID(unit))
}

// removeCheckTmp takes a check process's private temp directory away. Every
// failure is logged and swallowed: cleanup never fails the advance it runs
// from. A runtime with no tmp root made no directory, and an empty unit names
// none -- a spec built without a unit name must never reach the checks/ root
// itself.
func removeCheckTmp(rt Runtime, unit string) {
	if rt.TmpRoot == "" || unit == "" {
		return
	}
	if err := rt.Store.RemoveTmpEntry(rt.TmpRoot, checkTmpRel(unit)); err != nil {
		slog.Warn("check tmp dir not removed", "unit", unit, "err", err)
	}
}

// builderEnv is the environment a binding's headless builder runs with: the
// client's git identity, so a commit the builder makes is authored and
// committed as the client rather than as the server's OS user. Pure.
//
// nil when the binding carries no identity -- a local binding or one with no
// serve facts -- which leaves the builder with whatever identity the server's
// environment already resolved.
func builderEnv(b store.Binding) []string {
	if b.Serve == nil || b.Serve.AuthorName == "" || b.Serve.AuthorEmail == "" {
		return nil
	}
	name, email := b.Serve.AuthorName, b.Serve.AuthorEmail
	return []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
}

// roundEnv is the environment a round's process runs with: builderEnv's git
// identity plus the marker naming the binding this process is a runner for. A
// harness session relevo spawns for a round must not read itself as a
// MasterMind or planner, so the marker is what its consent hooks go silent on.
// The spawn appends the marker after the deny filter, so a stale RELEVO_RUNNER
// in the daemon's own environment cannot shadow it. Pure.
func roundEnv(b store.Binding) []string {
	return append(append(builderEnv(b), mastermind.RunnerEnvEntry(b.Name)), baseEnvEntry(b)...)
}

// roundEnvFor is roundEnv with the round's builder account pinned: the same git
// identity and runner marker, plus the account home entry last. Appending it
// after the marker puts it after proc.ChildEnv's deny filter, so the account's
// value is the later of two entries of the same name and a stale parent value
// cannot win. A nil account is exactly roundEnv's output.
func roundEnvFor(b store.Binding, a *account.Account) []string {
	return append(roundEnv(b), accountEnvEntry(a)...)
}

// accountEnvEntry is the spawn environment entry that pins a round's harness to
// the account's per-process home (CLAUDE_CONFIG_DIR, CODEX_HOME). nil for a nil
// account, a kind with no per-process home (opencode), and an account whose
// selector is empty: each would pin nothing.
func accountEnvEntry(a *account.Account) []string {
	if a == nil {
		return nil
	}
	name, ok := harness.HomeEnv(string(a.Harness))
	if !ok {
		return nil
	}
	home, ok := harness.AccountHome(*a)
	if !ok {
		return nil
	}
	return []string{name + "=" + home}
}

// accountFor returns the account a round on candidate c runs as: the first
// account of c's harness serving c's provider, or nil when accounts are
// unconfigured or no account serves the provider. The pool's config order is
// the failover order the pick walks, so the home this pins is the one the
// round's recorded account will name.
func accountFor(rt Runtime, c candidate.Candidate) *account.Account {
	if len(rt.Accounts) == 0 {
		return nil
	}
	pool := rt.Accounts.Pool(account.Kind(c.Harness), c.Provider)
	if len(pool) == 0 {
		return nil
	}
	a, ok, err := account.Select(pool, nil, account.Failover)
	if err != nil || !ok {
		return nil
	}
	return &a
}
