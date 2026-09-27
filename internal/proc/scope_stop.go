//go:build unix

package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// StopScope ends unit's transient scope and every process still in its cgroup,
// so a straggler a harness abandoned cannot hold the unit open. It signals
// first and kills after the runner's grace, and returns once the unit is gone;
// an error means it may still be loaded. A unit that is not loaded, and a host
// with no systemctl, are not errors: there is nothing to end.
func (r *Runner) StopScope(ctx context.Context, unit string) error {
	file := ScopeUnitFileName(unit)
	if err := r.stopScope(ctx, file); err != nil {
		// The stop itself failed. The unit may still be there -- or it may
		// have gone away between the two calls, which is not an error.
		if active, perr := r.ScopeActive(ctx, unit); perr == nil && !active {
			return nil
		}
		return fmt.Errorf("proc: stop %s: %w", file, err)
	}
	if r.scopeGone(ctx, unit) {
		return nil
	}
	if err := r.sigkillScope(ctx, file); err != nil {
		return err
	}
	if r.scopeGone(ctx, unit) {
		return nil
	}
	return fmt.Errorf("proc: scope %s is still running after SIGKILL", file)
}

// stopScope asks systemd to stop file without blocking on the stop job: a
// plain stop waits out systemd's own timeout, which the caller cannot afford.
// A missing systemctl is nil: there is no systemd and no scope to end.
func (r *Runner) stopScope(ctx context.Context, file string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := exec.CommandContext(cctx, "systemctl", "--user", "stop", "--no-block", file).Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	return err
}

// scopeGone reports whether unit's scope is no longer loaded, polling every
// 200 ms until it is gone or the runner's grace expires. A probe error counts
// as still loaded, so an unreadable state never reads as a stopped scope.
func (r *Runner) scopeGone(ctx context.Context, unit string) bool {
	deadline := time.Now().Add(r.grace())
	for {
		active, err := r.ScopeActive(ctx, unit)
		if err == nil && !active {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// sigkillScope kills every process still in file's cgroup. --kill-whom=all is
// required: the scope's main process is the dead supervisor, so the default
// main would signal nothing.
func (r *Runner) sigkillScope(ctx context.Context, file string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(cctx, "systemctl", "--user", "kill", "--kill-whom=all", "--signal=SIGKILL", file).Run(); err != nil {
		return fmt.Errorf("proc: kill %s: %w", file, err)
	}
	return nil
}
