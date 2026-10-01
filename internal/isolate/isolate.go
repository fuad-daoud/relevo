// Package isolate is the pure seam a served builder's process crosses to run
// under a tenant boundary. It owns the isolation modes (none, user, container),
// their parse and availability, the per-tenant process spec, and the boundary
// Runner that translates a round's spawn.ProcSpec before the base runner starts
// it.
//
// Slice B ships none and user. A configured container is refused: Wrap returns
// the mode's Available error, and the boundary it returns refuses at Start too.
// The package reads only the stdlib, spawn and harness (to name every
// harness's home variable); it is table-tested without a container runtime,
// root or a live harness.
package isolate

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
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

// Available reports whether this build can run the mode. none and user can; a
// container server is refused at startup and failed by the doctor, never
// started and warned. The sentence is what `relevo serve` refuses with and what
// the doctor's serve isolation row prints.
func (m Mode) Available() error {
	switch m {
	case ModeNone, ModeUser:
		return nil
	}
	return fmt.Errorf("serve.isolation=%s: not available in this build (only \"none\" and \"user\" can run)", m)
}

// CheckPrivilege refuses a mode the running euid cannot serve. User mode needs
// euid 0: only root can switch a child to the tenant's uid and gid. It is pure,
// so every caller can check before any side effect.
func CheckPrivilege(mode Mode, euid int) error {
	if mode == ModeUser && euid != 0 {
		return fmt.Errorf("serve.isolation=user requires root: relevo serve must run as euid 0 to switch builders to the tenant's identity (running as euid %d)", euid)
	}
	return nil
}

// Tenant is a served owner's declared unix identity, as os/user resolves it:
// the login name, the numeric uid and primary gid, and the home directory a
// user-mode round runs with.
type Tenant struct {
	User string
	UID  uint32
	GID  uint32
	Home string
}

// AccountHomeVars names every harness's per-process home selector --
// CLAUDE_CONFIG_DIR, CODEX_HOME and any name a future harness adds. User mode
// strips these from the inherited environment and, unless shared logins are on,
// from the spec's own additions too, so a round cannot be pointed at the
// server's account pool.
var AccountHomeVars = func() []string {
	var names []string
	for _, h := range harness.All() {
		if name, ok := harness.HomeEnv(h.Kind); ok {
			names = append(names, name)
		}
	}
	return names
}()

// InheritedHomeVars names every variable that points a process at the daemon's
// own home rather than the tenant's: the XDG base directories and each
// harness's account-home selector. A user-mode spec denies all of them, so the
// daemon's HOME and XDG roots never reach a builder running as the tenant.
var InheritedHomeVars = append([]string{
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
}, AccountHomeVars...)

// TenantIdentityVars names the login identity a user-mode round must carry
// exactly once: HOME, USER and LOGNAME. UserSpec appends the tenant's copies
// last and denies the inherited names, so a reader that takes the first match
// can never see the daemon's home or login. The deny entries exist only on
// user-mode specs, so a none-mode spec stays byte-identical.
var TenantIdentityVars = []string{"HOME", "USER", "LOGNAME"}

// UserSpec translates spec into the spec a user-mode round runs as t: the
// tenant credential, the tenant's HOME/USER/LOGNAME appended last (so the
// later entry wins over any inherited one), the inherited home variables
// denied, the identity names denied so exactly one HOME/USER/LOGNAME survives,
// and no scope (user mode runs scopes off). When sharedLogins is false, an
// account-home entry the caller added is removed too; when true it is kept,
// and the doctor warns about the sharing.
func UserSpec(spec spawn.ProcSpec, t Tenant, sharedLogins bool) spawn.ProcSpec {
	spec.Credential = &spawn.Credential{UID: t.UID, GID: t.GID}
	spec.Scope = nil
	spec.DenyEnv = append(slices.Clone(spec.DenyEnv), InheritedHomeVars...)
	spec.DenyEnv = append(spec.DenyEnv, TenantIdentityVars...)

	env := spec.Env
	if !sharedLogins {
		env = withoutNames(env, AccountHomeVars)
	}
	env = append(env[:len(env):len(env)], "HOME="+t.Home, "USER="+t.User, "LOGNAME="+t.User)
	spec.Env = env
	return spec
}

// withoutNames returns env with every entry whose name is in names removed.
// It copies, so the caller's slice is never written.
func withoutNames(env, names []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := cut(e)
		if slices.Contains(names, name) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// cut splits an environment entry at its first "="; a bare name has no value.
func cut(e string) (name, value string, ok bool) {
	for i := 0; i < len(e); i++ {
		if e[i] == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return e, "", false
}

// Refuse wraps base so every Start fails with a boundary-setup error carrying
// cause's text, while Alive, ExitCode, Kill and Rusage still delegate to base.
// It is how a user-mode server fails closed when its runner is not a Boundary:
// a round halts NEEDS YOU instead of running as the serve uid.
func Refuse(base spawn.Runner, cause error) spawn.Runner {
	return refuseRunner{base: base, cause: cause}
}

// refuseRunner is the Runner Refuse returns.
type refuseRunner struct {
	base  spawn.Runner
	cause error
}

// Start never launches: the boundary could not be established.
func (r refuseRunner) Start(context.Context, spawn.ProcSpec) (spawn.ProcHandle, error) {
	return spawn.ProcHandle{}, boundarySetupError(r.cause)
}

// Alive delegates to the base, nil-safe.
func (r refuseRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	if r.base == nil {
		return false, nil
	}
	return r.base.Alive(ctx, h)
}

// ExitCode delegates to the base, nil-safe.
func (r refuseRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	if r.base == nil {
		return 0, false
	}
	return r.base.ExitCode(ctx, h, logPath)
}

// Kill delegates to the base, nil-safe.
func (r refuseRunner) Kill(ctx context.Context, h spawn.ProcHandle, streamPath string) error {
	if r.base == nil {
		return nil
	}
	return r.base.Kill(ctx, h, streamPath)
}

// Rusage delegates to the base, nil-safe.
func (r refuseRunner) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	if r.base == nil {
		return spawn.ProcRusage{}, false
	}
	return r.base.Rusage(ctx, h, streamPath)
}

// Boundary is the runner Wrap returns for a mode: a spawn.Runner plus the
// per-owner variant a user-mode server binds to one tenant with ForTenant.
type Boundary interface {
	spawn.Runner
	// ForTenant returns the runner that serves one owner. A nil tenant refuses
	// every Start with an error wrapping spawn.ErrBoundarySetup and carrying
	// setupErr's text; a non-nil tenant starts each process under its
	// credential with UserSpec.
	ForTenant(t *Tenant, setupErr error) spawn.Runner
}

// Option configures the boundary Wrap builds.
type Option func(*boundary)

// SharedLogins keeps the server's account-home entries in a user-mode round's
// environment instead of stripping them. Off is the default and the safe
// choice; on is only for a host that deliberately shares one login pool.
func SharedLogins(on bool) Option {
	return func(b *boundary) { b.sharedLogins = on }
}

// Wrap returns a boundary Runner that runs base's processes under mode. For a
// mode this build cannot run it returns the mode's Available error along with a
// boundary that refuses at Start (defence in depth: a caller that drops the
// error still cannot start a process). ModeNone's Start hands base the
// identical ProcSpec -- no field added, dropped or reordered -- and Alive,
// ExitCode, Kill and Rusage delegate unchanged. ModeUser's Start refuses until
// ForTenant binds a tenant.
func Wrap(base spawn.Runner, mode Mode, opts ...Option) (Boundary, error) {
	b := &boundary{base: base, mode: mode}
	for _, opt := range opts {
		opt(b)
	}
	if err := mode.Available(); err != nil {
		return b, err
	}
	return b, nil
}

// boundary is the Runner Wrap returns: it translates a spec for its mode, then
// delegates every observation and stop to the base.
type boundary struct {
	base         spawn.Runner
	mode         Mode
	tenant       *Tenant
	sharedLogins bool
	// refuse, when non-nil, is what Start returns: it is set by ForTenant for a
	// nil tenant, so a tenant that could not be resolved refuses every spawn
	// with a boundary-setup error instead of running as the serve uid.
	refuse error
}

// boundary must stay transparent to every optional half of a Runner. Callers
// type-assert Runtime.Runner to ScopeProber, ScopeStopper and ScopeResultProber
// themselves, so a wrapper that dropped one would hide the base's scope support
// and stop a none server being byte-identical to an unwrapped one.
var _ spawn.ScopeProber = (*boundary)(nil)
var _ spawn.ScopeStopper = (*boundary)(nil)
var _ spawn.ScopeResultProber = (*boundary)(nil)

// Start translates the spec for the boundary's mode and hands it to the base.
// An unavailable mode is refused here even if Wrap's error was dropped, and a
// user-mode boundary with no bound tenant refuses with a boundary-setup error.
func (b *boundary) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	if err := b.mode.Available(); err != nil {
		return spawn.ProcHandle{}, err
	}
	if b.refuse != nil {
		return spawn.ProcHandle{}, b.refuse
	}
	switch b.mode {
	case ModeUser:
		if b.tenant == nil {
			return spawn.ProcHandle{}, boundarySetupError(nil)
		}
		return b.base.Start(ctx, UserSpec(spec, *b.tenant, b.sharedLogins))
	default:
		return b.base.Start(ctx, translate(spec, b.mode))
	}
}

// ForTenant returns the runner this boundary uses for one owner. A nil tenant
// (a user that does not exist, or an owner root that failed its setup check)
// refuses every Start with spawn.ErrBoundarySetup, so a misconfigured owner
// halts instead of running as the serve uid.
func (b *boundary) ForTenant(t *Tenant, setupErr error) spawn.Runner {
	if t == nil {
		return &boundary{base: b.base, mode: b.mode, sharedLogins: b.sharedLogins, refuse: boundarySetupError(setupErr)}
	}
	return &boundary{base: b.base, mode: b.mode, tenant: t, sharedLogins: b.sharedLogins}
}

// boundarySetupError wraps spawn.ErrBoundarySetup with whatever setupErr the
// resolver returned, so the halt text names the exact missing prerequisite
// while errors.Is lets the spawn path recognise it as not-the-candidate's-fault.
func boundarySetupError(setupErr error) error {
	if setupErr == nil {
		return fmt.Errorf("%w: no tenant declared for this owner", spawn.ErrBoundarySetup)
	}
	return fmt.Errorf("%w: %s", spawn.ErrBoundarySetup, setupErr.Error())
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
// server is byte-identical to a server with no boundary. User mode is handled
// by ForTenant, which binds a tenant and calls UserSpec; an unavailable mode
// never reaches here (Wrap and Start refuse it).
func translate(spec spawn.ProcSpec, mode Mode) spawn.ProcSpec {
	return spec
}
