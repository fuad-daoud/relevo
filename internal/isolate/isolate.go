// Package isolate is the seam a served builder's process crosses to run under
// a tenant boundary: the isolation modes, the per-tenant spec, and the
// boundary Runner that translates a round's spawn.ProcSpec before the base runner
// starts it.
package isolate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// Mode is a tenant-isolation mode named by serve.isolation.
type Mode string

const (
	// ModeNone runs every builder as the serve uid.
	ModeNone Mode = "none"
	// ModeUser runs every builder as its owner's declared unix user.
	ModeUser Mode = "user"
	// ModeContainer runs every builder in a rootless container.
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

// Available reports whether this build can run the mode. Every mode this
// parser knows can run, so it always returns nil; the method stays as the one
// refusal point a future mode needing a build capability would use.
func (m Mode) Available() error {
	return nil
}

// CheckPrivilege refuses a mode the running euid cannot serve. User mode needs
// euid 0: only root can switch a child to the tenant's uid and gid.
func CheckPrivilege(mode Mode, euid int) error {
	if mode == ModeUser && euid != 0 {
		return fmt.Errorf("serve.isolation=user requires root: relevo serve must run as euid 0 to switch builders to the tenant's identity (running as euid %d)", euid)
	}
	return nil
}

// Tenant is a served owner's declared unix identity, as os/user resolves it:
// the login name, uid, primary gid, and home directory its rounds run with.
type Tenant struct {
	User string
	UID  uint32
	GID  uint32
	Home string
}

// AccountHomeVars names every harness's per-process home selector. User mode
// strips these from the inherited environment so a round cannot be pointed at
// the server's account pool.
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
// own home rather than the tenant's. A user-mode spec denies all of them, so
// the daemon's HOME and XDG roots never reach a builder running as the tenant.
var InheritedHomeVars = append([]string{
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
}, AccountHomeVars...)

// TenantIdentityVars names the login identity a user-mode round must carry
// exactly once: HOME, USER and LOGNAME, denied from the inheritance so a
// first-match reader can never see the daemon's home or login. The deny applies
// to user-mode specs only, so a none-mode spec stays byte-identical.
var TenantIdentityVars = []string{"HOME", "USER", "LOGNAME"}

// UserSpec translates spec into the spec a user-mode round runs as t: the
// tenant credential, the tenant's HOME/USER/LOGNAME appended last, so the later
// entry wins over any inherited one, the home variables denied, and no scope.
// When sharedLogins is false, an account-home entry the caller added is removed too.
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

type refuseRunner struct {
	base  spawn.Runner
	cause error
}

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
// per-owner variants a server binds one owner with.
type Boundary interface {
	spawn.Runner
	// ForTenant returns the runner that serves one owner. A nil tenant refuses
	// every Start with an error wrapping spawn.ErrBoundarySetup and carrying
	// setupErr's text; a non-nil tenant starts each process under its
	// credential with UserSpec.
	ForTenant(t *Tenant, setupErr error) spawn.Runner
	// ForContainer returns the runner that serves one owner in container mode,
	// bound to that owner's container spec. With no spec bound it refuses
	// every Start with spawn.ErrBoundarySetup.
	ForContainer(c ContainerSpec) spawn.Runner
}

type Option func(*boundary)

// SharedLogins keeps the server's account-home entries in a user-mode round's
// environment instead of stripping them. Off is the default and the safe
// choice; on is only for a host that deliberately shares one login pool.
func SharedLogins(on bool) Option {
	return func(b *boundary) { b.sharedLogins = on }
}

// PodmanBin names the container runtime binary the boundary probes and, by
// default, runs. "podman" is the default.
func PodmanBin(bin string) Option {
	return func(b *boundary) { b.podmanBin = bin }
}

// LookPath resolves a binary name on PATH. It is a seam so a test can force
// the container runtime absent without touching the host's PATH.
func LookPath(fn func(string) (string, error)) Option {
	return func(b *boundary) { b.lookPath = fn }
}

// PodmanRm is how an injected runtime removes a container by id. It is a seam
// so the kill path is testable without podman.
func PodmanRm(fn func(context.Context, string) error) Option {
	return func(b *boundary) { b.podmanRm = fn }
}

// PodmanImageExists probes whether the container image is present. It is a
// seam so a test can force the image missing without a container runtime.
func PodmanImageExists(fn func(context.Context, string) error) Option {
	return func(b *boundary) { b.podmanImageExists = fn }
}

// Wrap returns a boundary Runner that runs base's processes under mode. It
// returns the mode's Available error if one exists along with a boundary that
// refuses at Start. ModeNone's Start hands base the identical ProcSpec, and
// ModeUser's refuses until ForTenant binds a tenant.
func Wrap(base spawn.Runner, mode Mode, opts ...Option) (Boundary, error) {
	b := &boundary{
		base:              base,
		mode:              mode,
		podmanBin:         "podman",
		lookPath:          exec.LookPath,
		podmanRm:          defaultPodmanRm,
		podmanImageExists: defaultPodmanImageExists,
	}
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
	// nil tenant, so spawns refuse instead of running as the serve uid.
	refuse error
	// container is the spec ForContainer bound this owner to; hasContainer
	// tells a bound container boundary from an unbound one, which refuses.
	container    ContainerSpec
	hasContainer bool
	// podmanBin, lookPath, podmanRm and podmanImageExists are the container
	// seams, defaulting to the real podman via exec.
	podmanBin         string
	lookPath          func(string) (string, error)
	podmanRm          func(context.Context, string) error
	podmanImageExists func(context.Context, string) error
}

// boundary stays transparent to every optional half of a Runner: dropping one
// would hide the base's scope support from a type-asserting caller.
var _ spawn.ScopeProber = (*boundary)(nil)
var _ spawn.ScopeStopper = (*boundary)(nil)
var _ spawn.ScopeResultProber = (*boundary)(nil)

// Start translates the spec for the boundary's mode and hands it to the base.
// An unavailable mode is refused here even if Wrap's error was dropped. A
// container Start proves the runtime and the image first, so a round with a
// missing prerequisite halts naming the piece.
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
	case ModeContainer:
		if !b.hasContainer {
			return spawn.ProcHandle{}, boundarySetupError(errContainerUnbound)
		}
		if err := b.checkContainer(ctx); err != nil {
			return spawn.ProcHandle{}, boundarySetupError(err)
		}
		return b.base.Start(ctx, ContainerArgv(spec, b.container))
	default:
		return b.base.Start(ctx, translate(spec, b.mode))
	}
}

// checkContainer proves the container runtime and image before a Start, so a
// round with a missing prerequisite halts instead of the candidate failing.
func (b *boundary) checkContainer(ctx context.Context) error {
	if _, err := b.lookPath(b.podmanBin); err != nil {
		return fmt.Errorf("%s is not on PATH: install the container runtime and ensure it is on PATH", b.podmanBin)
	}
	if err := b.podmanImageExists(ctx, b.container.Image); err != nil {
		return fmt.Errorf("container image %q is not available: build or pull it (see dist/Containerfile)", b.container.Image)
	}
	return nil
}

var errContainerUnbound = fmt.Errorf("serve.isolation=container needs a container spec bound to this owner")

// ForTenant returns the runner this boundary uses for one owner: a nil tenant
// refuses every Start with spawn.ErrBoundarySetup.
func (b *boundary) ForTenant(t *Tenant, setupErr error) spawn.Runner {
	c := *b
	if t == nil {
		c.tenant = nil
		c.refuse = boundarySetupError(setupErr)
		return &c
	}
	c.tenant = t
	c.refuse = nil
	return &c
}

// ForContainer returns the runner this boundary uses for one owner in
// container mode, bound to that owner's container spec. The seams and the base
// are copied from the server-wide boundary.
func (b *boundary) ForContainer(cs ContainerSpec) spawn.Runner {
	c := *b
	c.container = cs
	c.hasContainer = true
	return &c
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

func (b *boundary) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	return b.base.Alive(ctx, h)
}

func (b *boundary) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	return b.base.ExitCode(ctx, h, logPath)
}

// Kill removes a container round's container first (when one is bound), then
// delegates to the base: podman is the process the base signals, and the container
// does not share its process group, so it needs an explicit remove.
func (b *boundary) Kill(ctx context.Context, h spawn.ProcHandle, streamPath string) error {
	if b.mode == ModeContainer && b.hasContainer {
		b.killContainer(ctx, streamPath)
	}
	return b.base.Kill(ctx, h, streamPath)
}

// killContainer reads the cidfile beside the stream and removes the container
// it names. A missing, blank or stale cidfile is not an error, and the removal
// is best-effort, so it never blocks the base Kill behind it.
func (b *boundary) killContainer(ctx context.Context, streamPath string) {
	cidPath := streamPath + ".cid"
	raw, err := os.ReadFile(cidPath)
	if err != nil {
		return
	}
	cid := strings.TrimSpace(string(raw))
	if cid != "" {
		_ = b.podmanRm(ctx, cid)
	}
	_ = os.Remove(cidPath)
}

// Rusage reports no measurement for a container round: there is no systemd
// scope and no rusage trailer, and no measurement is faked.
func (b *boundary) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	if b.mode == ModeContainer && b.hasContainer {
		return spawn.ProcRusage{}, false
	}
	return b.base.Rusage(ctx, h, streamPath)
}

func defaultPodmanRm(ctx context.Context, cid string) error {
	return exec.CommandContext(ctx, "podman", "rm", "-f", cid).Run()
}

// defaultPodmanImageExists reports whether the image is present locally.
func defaultPodmanImageExists(ctx context.Context, image string) error {
	out, err := exec.CommandContext(ctx, "podman", "image", "exists", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman image exists %s: %w (%s)", image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ScopeActive delegates to a base that can probe scope units, and answers "no
// scope is active" for a base that cannot, the same answer scopeRunning gives a
// runner without ScopeProber.
func (b *boundary) ScopeActive(ctx context.Context, unit string) (bool, error) {
	if p, ok := b.base.(spawn.ScopeProber); ok {
		return p.ScopeActive(ctx, unit)
	}
	return false, nil
}

// StopScope delegates to a base that can end scope units; a base that cannot
// is refused, as endScope refuses a runner that can see a scope but not end it,
// since the caller could not otherwise free the unit.
func (b *boundary) StopScope(ctx context.Context, unit string) error {
	if s, ok := b.base.(spawn.ScopeStopper); ok {
		return s.StopScope(ctx, unit)
	}
	return fmt.Errorf("scope %s.scope: this runner cannot end scopes", unit)
}

// ScopeResult delegates to a base that can read a scope's journal result, and
// reports the zero result for a base that cannot, read as not OOM-killed, as
// housekeep.OOMKilled reads a runner without the result prober.
func (b *boundary) ScopeResult(ctx context.Context, unit string, since time.Time) (spawn.ScopeResult, error) {
	if p, ok := b.base.(spawn.ScopeResultProber); ok {
		return p.ScopeResult(ctx, unit, since)
	}
	return spawn.ScopeResult{}, nil
}

// translate maps a round's ProcSpec to the spec the base runner starts for
// mode. ModeNone is identity, so a none server is byte-identical to a server
// with no boundary. User and container modes never reach here: Start translates
// them first through UserSpec and ContainerArgv.
func translate(spec spawn.ProcSpec, mode Mode) spawn.ProcSpec {
	return spec
}
