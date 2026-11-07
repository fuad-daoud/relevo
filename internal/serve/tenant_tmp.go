package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// ownerTmpDir is the link one owner writes through: /tmp/rv-<8>, a digest
// of the serve root and the owner id, so two serves on one host never share
// a link and a restart finds the same one. Eight hex is a 32-bit truncation
// of a uniform digest, safe among the handful of owners on one host. The
// literal /tmp keeps the link plus a rounds/ or checks/ leaf short enough to
// leave room for a socket name under the sun_path limit. /tmp is hostile --
// aged and writable by every tenant -- so every resolution verifies the link
// with ensureTmpLink before a round or check start can reach it, in both
// modes. The bytes live in the on-disk target the link points at; the link is
// only what round and check processes see as TMPDIR. The bundle transport
// keeps its own <root>/tmp/<hex>.
func (s *Server) ownerTmpDir(root string) string {
	sum := sha256.Sum256([]byte(s.cfg.Root + "\x00" + filepath.Base(root)))
	return filepath.Join("/tmp", "rv-"+hex.EncodeToString(sum[:])[:8])
}

// ownerTmpTarget is where one owner's tmp bytes live:
// <serve root>/tmp/t/<short>, under the serve root so the disk preflight
// measures the serve filesystem, not /tmp's. The ownerTmpDir link points
// here, so a served round starts while /tmp itself reads full.
func (s *Server) ownerTmpTarget(ownerRoot string) string {
	return filepath.Join(s.cfg.Root, "tmp", "t", shortOwnerDir(filepath.Base(ownerRoot)))
}

// shortOwnerDir is one owner's path component under the serve tmp target:
// the first 8 characters of its 64-hex directory name. The digest is already
// uniform, so truncation stays deterministic in the full owner id; shorter
// test ids pass through untouched. Two owners sharing a prefix would share
// a target, which takes a 32-bit collision among the handful of enrolled
// owners.
func shortOwnerDir(hex string) string {
	if len(hex) > 8 {
		return hex[:8]
	}
	return hex
}

// ensureTmpLink creates or verifies the /tmp link pointing at one owner's
// on-disk tmp target. /tmp is hostile -- aged and writable by every tenant --
// so the link is resolved on every call, never cached: a missing link is
// made, an EEXIST falls back to one stat of the race winner's link, and a
// link the caller owns that points elsewhere is removed and remade, since
// only our own link is ours to replace. A non-link, or a link another uid
// owns, is refused untouched and never followed, so a planted path fails the
// owner's round start instead of diverting it. Ownership comes from Lstat,
// which reports the link itself. Every refusal names the path and the fix.
func ensureTmpLink(link, target string, euid uint32) error {
	fi, err := os.Lstat(link)
	if errors.Is(err, fs.ErrNotExist) {
		if serr := os.Symlink(target, link); serr != nil {
			if !errors.Is(serr, fs.ErrExist) {
				return fmt.Errorf("tmp link %s: point it at %s: %w", link, target, serr)
			}
			fi, err = os.Lstat(link)
			if err != nil {
				return err
			}
		} else {
			return nil
		}
	} else if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("tmp link %s is not a symlink: remove it so serve can recreate it pointing at %s", link, target)
	}
	if ouid, _, ok := db.StatOwner(fi); ok && ouid != euid {
		return fmt.Errorf("tmp link %s is owned by uid %d, want %d: remove it or hand it to %d so serve can use it", link, ouid, euid, euid)
	}
	got, err := os.Readlink(link)
	if err != nil {
		return fmt.Errorf("tmp link %s: read it to check it points at %s: %w", link, target, err)
	}
	if got == target {
		return nil
	}
	if err := os.Remove(link); err != nil {
		return fmt.Errorf("tmp link %s: remove it so serve can recreate it pointing at %s: %w", link, target, err)
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("tmp link %s: point it at %s: %w", link, target, err)
	}
	return nil
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
