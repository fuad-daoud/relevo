package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// ownerTmpDir is the temp directory one user-mode owner writes through:
// <root>/tmp/t/<short>, where short is the owner's short id. The bundle
// transport keeps its own <root>/tmp/<hex>; this root is only what round
// and check processes see as TMPDIR, so it stays short enough to leave room
// for a socket name under the sun_path limit. ensureTenantRoots creates it
// tenant-owned, so a child running as the tenant can write there.
func (s *Server) ownerTmpDir(root string) string {
	return filepath.Join(s.cfg.Root, "tmp", "t", shortOwnerDir(filepath.Base(root)))
}

// shortOwnerDir is one owner's path component under the serve tmp root: the
// first 8 characters of its 64-hex directory name. The digest is already
// uniform, so truncation stays deterministic in the full owner id; shorter
// test ids pass through untouched. Two owners sharing a prefix would share
// a root, which takes a 32-bit collision among the handful of enrolled
// owners.
func shortOwnerDir(hex string) string {
	if len(hex) > 8 {
		return hex[:8]
	}
	return hex
}

// tmpDirRunner points every process one user-mode owner starts, unless that
// process carries a temp directory of its own, at the owner's own temp
// directory. The daemon's default would be the root-owned state tmp dir, which
// the tenant cannot enter, so the per-owner binding has to arrive here rather
// than on the shared base runner. ForTenant takes no path, so this wraps the
// boundary's own runner and the isolate interface is untouched.
type tmpDirRunner struct {
	base spawn.Runner
	dir  string
}

// tmpDirRunner must stay transparent to every optional half of a Runner, as
// the boundary is: callers type-assert Runtime.Runner to these themselves, so
// a wrapper that dropped one would hide the base's scope support.
var (
	_ spawn.Runner            = tmpDirRunner{}
	_ spawn.ScopeProber       = tmpDirRunner{}
	_ spawn.ScopeStopper      = tmpDirRunner{}
	_ spawn.ScopeResultProber = tmpDirRunner{}
)

func withTmpDir(base spawn.Runner, dir string) spawn.Runner {
	return tmpDirRunner{base: base, dir: dir}
}

// Start hands the spec to the base untouched when it carries its own temp
// directory: that directory is the process's own choice, the base runner
// applies it ahead of the inherited environment, and appending the owner's
// default beside it would suppress that. Otherwise the override rule is the
// runner's own, per variable: an entry the spec already carries wins, and a
// non-empty inherited value is the user's own choice and is left alone.
// Appending to spec.Env also suppresses the base runner's default, so exactly
// one of each name reaches the child.
func (r tmpDirRunner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	if spec.TmpDir != "" {
		return r.base.Start(ctx, spec)
	}
	env := spec.Env
	for _, name := range proc.TmpDirEnv {
		if proc.HasEnvName(env, name) {
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

func (r tmpDirRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	return r.base.Alive(ctx, h)
}

func (r tmpDirRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	return r.base.ExitCode(ctx, h, logPath)
}

func (r tmpDirRunner) Kill(ctx context.Context, h spawn.ProcHandle, streamPath string) error {
	return r.base.Kill(ctx, h, streamPath)
}

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
