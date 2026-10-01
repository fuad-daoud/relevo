// Package isolate is the pure seam a served builder's process crosses to run
// under a tenant boundary. It owns the isolation modes (none, user, container),
// their parse and availability, and the boundary Runner that translates a
// round's spawn.ProcSpec before the base runner starts it.
//
// Slice A ships only ModeNone. A configured user or container is refused:
// Wrap returns the mode's Available error, and the boundary it returns refuses
// at Start too. The package is pure -- stdlib and spawn only -- so it is
// table-tested without a container runtime, root or a harness.
package isolate

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// Mode is a tenant-isolation mode named by serve.isolation.
type Mode string

const (
	// ModeNone runs every builder as the serve uid: today's trust model, and
	// the only mode this build can run.
	ModeNone Mode = "none"
	// ModeUser runs every builder as its owner's declared unix user (slice B).
	ModeUser Mode = "user"
	// ModeContainer runs every builder in a rootless container (slice C).
	ModeContainer Mode = "container"
)

// Parse normalizes a configured serve.isolation value: "" and "none" are
// ModeNone, and "user" and "container" parse. Anything else is refused by
// name. It does not check availability: a parsed mode this build cannot run is
// reported by Available, so an admin status can still name it.
func Parse(s string) (Mode, error) {
	switch s {
	case "", "none":
		return ModeNone, nil
	case "user":
		return ModeUser, nil
	case "container":
		return ModeContainer, nil
	}
	return "", fmt.Errorf("serve.isolation: unknown mode %q (known: none, user, container)", s)
}

// Available reports whether this build can run the mode. Only none can: a user
// or container server is refused at startup and failed by the doctor, never
// started and warned. The sentence is what `relevo serve` refuses with and what
// the doctor's serve isolation row prints.
func (m Mode) Available() error {
	if m == ModeNone {
		return nil
	}
	return fmt.Errorf("serve.isolation=%s: not available in this build (only \"none\" can run)", m)
}

// Wrap returns a boundary Runner that runs base's processes under mode. For a
// mode this build cannot run it returns the mode's Available error along with a
// boundary that refuses at Start (defence in depth: a caller that drops the
// error still cannot start a process). ModeNone's Start hands base the
// identical ProcSpec -- no field added, dropped or reordered -- and Alive,
// ExitCode, Kill and Rusage delegate unchanged.
func Wrap(base spawn.Runner, mode Mode) (spawn.Runner, error) {
	b := &boundary{base: base, mode: mode}
	if err := mode.Available(); err != nil {
		return b, err
	}
	return b, nil
}

// boundary is the Runner Wrap returns: it translates a spec for its mode, then
// delegates every observation and stop to the base.
type boundary struct {
	base spawn.Runner
	mode Mode
}

// boundary must stay transparent to every optional half of a Runner. Callers
// type-assert Runtime.Runner to ScopeProber, ScopeStopper and ScopeResultProber
// themselves, so a wrapper that dropped one would hide the base's scope support
// and stop a none server being byte-identical to an unwrapped one.
var _ spawn.ScopeProber = (*boundary)(nil)
var _ spawn.ScopeStopper = (*boundary)(nil)
var _ spawn.ScopeResultProber = (*boundary)(nil)

// Start translates the spec for the boundary's mode and hands it to the base.
// An unavailable mode is refused here even if Wrap's error was dropped.
func (b *boundary) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	if err := b.mode.Available(); err != nil {
		return spawn.ProcHandle{}, err
	}
	return b.base.Start(ctx, translate(spec, b.mode))
}

// Alive delegates to the base.
func (b *boundary) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	return b.base.Alive(ctx, h)
}

// ExitCode delegates to the base.
func (b *boundary) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	return b.base.ExitCode(ctx, h, logPath)
}

// Kill delegates to the base.
func (b *boundary) Kill(ctx context.Context, h spawn.ProcHandle, streamPath string) error {
	return b.base.Kill(ctx, h, streamPath)
}

// Rusage delegates to the base.
func (b *boundary) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	return b.base.Rusage(ctx, h, streamPath)
}

// ScopeActive delegates to a base that can probe scope units, and answers "no
// scope is active" for a base that cannot -- the same answer scopeRunning gives
// a runner without the prober half.
func (b *boundary) ScopeActive(ctx context.Context, unit string) (bool, error) {
	if p, ok := b.base.(spawn.ScopeProber); ok {
		return p.ScopeActive(ctx, unit)
	}
	return false, nil
}

// StopScope delegates to a base that can end scope units. A base that cannot
// is refused, as endScope refuses a runner that can see a scope but not end it:
// the caller cannot otherwise free the unit.
func (b *boundary) StopScope(ctx context.Context, unit string) error {
	if s, ok := b.base.(spawn.ScopeStopper); ok {
		return s.StopScope(ctx, unit)
	}
	return fmt.Errorf("scope %s.scope: this runner cannot end scopes", unit)
}

// ScopeResult delegates to a base that can read a scope's journal result, and
// reports the zero result for a base that cannot -- "not oom-killed", the
// meaning oomKilled gives a runner without the result prober.
func (b *boundary) ScopeResult(ctx context.Context, unit string, since time.Time) (spawn.ScopeResult, error) {
	if p, ok := b.base.(spawn.ScopeResultProber); ok {
		return p.ScopeResult(ctx, unit, since)
	}
	return spawn.ScopeResult{}, nil
}

// translate maps a round's ProcSpec to the spec the base runner starts for
// mode. ModeNone is identity: every field passes through unchanged, so a none
// server is byte-identical to a server with no boundary. A mode that needs a
// translated spec arrives in a later slice; an unavailable mode never reaches
// here (Wrap and Start refuse it).
func translate(spec spawn.ProcSpec, mode Mode) spawn.ProcSpec {
	return spec
}
