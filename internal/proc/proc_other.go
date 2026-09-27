//go:build !unix

// Package proc is relevo's local process Runner. relevo targets Linux and
// macOS; this file exists so the tree compiles elsewhere, where every call
// reports spawn.ErrRunnerUnavailable.
package proc

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

const DefaultKillGrace = 5 * time.Second

type Runner struct {
	KillGrace time.Duration
}

var _ spawn.Runner = (*Runner)(nil)

func New() *Runner { return &Runner{} }

func (r *Runner) Start(context.Context, spawn.ProcSpec) (spawn.ProcHandle, error) {
	return spawn.ProcHandle{}, spawn.ErrRunnerUnavailable
}

func (r *Runner) Alive(context.Context, spawn.ProcHandle) (bool, error) {
	return false, spawn.ErrRunnerUnavailable
}

func (r *Runner) ExitCode(context.Context, spawn.ProcHandle, string) (int, bool) {
	return 0, false
}

func (r *Runner) Kill(context.Context, spawn.ProcHandle, string) error {
	return spawn.ErrRunnerUnavailable
}

func (r *Runner) Rusage(context.Context, spawn.ProcHandle, string) (spawn.ProcRusage, bool) {
	return spawn.ProcRusage{}, false
}

// StartTime reports ErrRunnerUnavailable too: psInfo does not exist here.
func StartTime(context.Context, int) (time.Time, error) {
	return time.Time{}, spawn.ErrRunnerUnavailable
}
