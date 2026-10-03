package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// ownerTmpDir is the temp directory one user-mode owner writes through: the
// same <root>/tmp/<hex> its bundle temps use, which ensureTenantRoots creates
// tenant-owned, so a child running as the tenant can write there.
func (s *Server) ownerTmpDir(root string) string {
	return filepath.Join(s.cfg.Root, "tmp", filepath.Base(root))
}

// tmpDirRunner points every process one user-mode owner starts at that owner's
// own temp directory. The daemon's default would be the root-owned state tmp
// dir, which the tenant cannot enter, so the per-owner binding has to arrive
// here rather than on the shared base runner. ForTenant takes no path, so this
// wraps the boundary's own runner and the isolate interface is untouched.
type tmpDirRunner struct {
	base spawn.Runner
	dir  string
}

// It must stay transparent to every optional half of a Runner, as the boundary
// is: callers type-assert Runtime.Runner to these themselves, so a wrapper that
// dropped one would hide the base's scope support.
var (
	_ spawn.Runner            = tmpDirRunner{}
	_ spawn.ScopeProber       = tmpDirRunner{}
	_ spawn.ScopeStopper      = tmpDirRunner{}
	_ spawn.ScopeResultProber = tmpDirRunner{}
)

// withTmpDir returns base with every spec it starts carrying dir as its
// temporary directory.
func withTmpDir(base spawn.Runner, dir string) spawn.Runner {
	return tmpDirRunner{base: base, dir: dir}
}

// Start sets the temp directory on spec and hands it to the base. The override
// rule is the runner's own, per variable: an entry the spec already carries
// wins, and a non-empty inherited value is the user's own choice and is left
// alone. Appending to spec.Env also suppresses the base runner's default, so
// exactly one of each name reaches the child.
func (r tmpDirRunner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	env := spec.Env
	for _, name := range proc.TmpDirEnv {
		if hasEnvName(env, name) {
			continue
		}
		if v, ok := os.LookupEnv(name); ok && v != "" {
			continue
		}
		env = append(env[:len(env):len(env)], name+"="+r.dir)
	}
	spec.Env = env
	return r.base.Start(ctx, spec)
}

// Alive delegates to the base.
func (r tmpDirRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	return r.base.Alive(ctx, h)
}

// ExitCode delegates to the base.
func (r tmpDirRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	return r.base.ExitCode(ctx, h, logPath)
}

// Kill delegates to the base.
func (r tmpDirRunner) Kill(ctx context.Context, h spawn.ProcHandle, streamPath string) error {
	return r.base.Kill(ctx, h, streamPath)
}

// Rusage delegates to the base.
func (r tmpDirRunner) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	return r.base.Rusage(ctx, h, streamPath)
}

// ScopeActive delegates to a base that can probe scope units, and answers "no
// scope is active" for one that cannot -- the same answer a scope-less
// user-mode spec gets anyway.
func (r tmpDirRunner) ScopeActive(ctx context.Context, unit string) (bool, error) {
	if p, ok := r.base.(spawn.ScopeProber); ok {
		return p.ScopeActive(ctx, unit)
	}
	return false, nil
}

// StopScope delegates to a base that can end scope units, and refuses for one
// that cannot: a caller that can see a scope must be able to end it.
func (r tmpDirRunner) StopScope(ctx context.Context, unit string) error {
	if s, ok := r.base.(spawn.ScopeStopper); ok {
		return s.StopScope(ctx, unit)
	}
	return fmt.Errorf("scope %s.scope: this runner cannot end scopes", unit)
}

// ScopeResult delegates to a base that can read a scope's journal result, and
// reports the zero result for one that cannot.
func (r tmpDirRunner) ScopeResult(ctx context.Context, unit string, since time.Time) (spawn.ScopeResult, error) {
	if p, ok := r.base.(spawn.ScopeResultProber); ok {
		return p.ScopeResult(ctx, unit, since)
	}
	return spawn.ScopeResult{}, nil
}

// hasEnvName reports whether env carries name, matching "NAME=..." or a bare
// "NAME" as the runner's own deny list does.
func hasEnvName(env []string, name string) bool {
	for _, e := range env {
		if n, _, _ := strings.Cut(e, "="); n == name {
			return true
		}
	}
	return false
}
