package isolate

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// recordRunner is a base spawn.Runner that records every ProcSpec Start is
// handed and does nothing else.
type recordRunner struct {
	got   []spawn.ProcSpec
	alive bool
}

func (r *recordRunner) Start(_ context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	r.got = append(r.got, spec)
	return spawn.ProcHandle{PID: 4242}, nil
}

func (r *recordRunner) Alive(context.Context, spawn.ProcHandle) (bool, error) {
	return r.alive, nil
}

func (r *recordRunner) ExitCode(context.Context, spawn.ProcHandle, string) (int, bool) {
	return 0, false
}

func (r *recordRunner) Kill(context.Context, spawn.ProcHandle, string) error {
	r.alive = false
	return nil
}

func (r *recordRunner) Rusage(context.Context, spawn.ProcHandle, string) (spawn.ProcRusage, bool) {
	return spawn.ProcRusage{}, false
}

// scopeSpy is a base Runner that also has the three optional halves. It records
// the unit (and, for ScopeResult, the since) each is handed and returns what the
// test set, so a boundary over it can be checked for transparent delegation.
type scopeSpy struct {
	recordRunner

	active     bool
	activeUnit string

	stopUnit string
	stopErr  error

	result      spawn.ScopeResult
	resultUnit  string
	resultSince time.Time
}

func (s *scopeSpy) ScopeActive(_ context.Context, unit string) (bool, error) {
	s.activeUnit = unit
	return s.active, nil
}

func (s *scopeSpy) StopScope(_ context.Context, unit string) error {
	s.stopUnit = unit
	return s.stopErr
}

func (s *scopeSpy) ScopeResult(_ context.Context, unit string, since time.Time) (spawn.ScopeResult, error) {
	s.resultUnit, s.resultSince = unit, since
	return s.result, nil
}

func TestParse(t *testing.T) {
	ok := []struct {
		in   string
		want Mode
	}{
		{"", ModeNone},
		{"none", ModeNone},
		{"user", ModeUser},
		{"container", ModeContainer},
	}
	for _, tc := range ok {
		t.Run("ok "+tc.in, func(t *testing.T) {
			got, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	for _, in := range []string{"USER", "None", "host", " none", "none ", "docker"} {
		t.Run("refuse "+in, func(t *testing.T) {
			if _, err := Parse(in); err == nil {
				t.Fatalf("Parse(%q) = nil error, want a refusal", in)
			} else if !strings.Contains(err.Error(), "serve.isolation") {
				t.Fatalf("Parse(%q) error = %q, want it to name serve.isolation", in, err)
			}
		})
	}
}

func TestAvailable(t *testing.T) {
	for _, m := range []Mode{ModeNone, ModeUser} {
		if err := m.Available(); err != nil {
			t.Fatalf("%s.Available() = %v, want nil", m, err)
		}
	}
	err := ModeContainer.Available()
	if err == nil {
		t.Fatalf("%s.Available() = nil, want a refusal", ModeContainer)
	}
	if !strings.Contains(err.Error(), "serve.isolation="+string(ModeContainer)) {
		t.Fatalf("%s.Available() = %q, want it to name serve.isolation=%s", ModeContainer, err, ModeContainer)
	}
}

// TestCheckPrivilege pins the euid rule: only user mode needs root, and a
// non-root euid is refused by name.
func TestCheckPrivilege(t *testing.T) {
	for _, tc := range []struct {
		mode Mode
		euid int
		ok   bool
	}{
		{ModeNone, 0, true},
		{ModeNone, 1000, true},
		{ModeUser, 0, true},
		{ModeUser, 1000, false},
		{ModeContainer, 1000, true}, // container availability is checked by Available
	} {
		err := CheckPrivilege(tc.mode, tc.euid)
		if tc.ok && err != nil {
			t.Errorf("CheckPrivilege(%s, %d) = %v, want nil", tc.mode, tc.euid, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("CheckPrivilege(%s, %d) = nil, want a refusal", tc.mode, tc.euid)
			} else if !strings.Contains(err.Error(), "root") {
				t.Errorf("CheckPrivilege(%s, %d) = %q, want it to say root is required", tc.mode, tc.euid, err)
			}
		}
	}
}

// TestInheritedHomeVarsCoverEveryHarness pins that every variable that points a
// process at a home is denied: the five XDG names and every harness's own
// account-home selector.
func TestInheritedHomeVarsCoverEveryHarness(t *testing.T) {
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		if !slices.Contains(InheritedHomeVars, name) {
			t.Errorf("InheritedHomeVars lacks %s", name)
		}
	}
	for _, h := range harness.All() {
		name, ok := harness.HomeEnv(h.Kind)
		if !ok {
			continue
		}
		if !slices.Contains(InheritedHomeVars, name) {
			t.Errorf("InheritedHomeVars lacks %s (harness %s)", name, h.Kind)
		}
	}
}

// TestUserSpec pins the spec a user-mode round runs as: the credential, the
// tenant HOME/USER/LOGNAME appended last, the inherited home variables denied,
// no scope, and the account-home entries stripped unless logins are shared.
func TestUserSpec(t *testing.T) {
	tenant := Tenant{User: "alice", UID: 1001, GID: 1002, Home: "/home/alice"}

	t.Run("credential, env, deny list and scope", func(t *testing.T) {
		spec := spawn.ProcSpec{
			Dir:   "/round",
			Argv:  []string{"claude", "-p", "plan.md"},
			Env:   []string{"RELEVO_RUNNER=api", "CLAUDE_CONFIG_DIR=/srv/pool/a"},
			Scope: &spawn.ScopeSpec{Unit: "relevo-round-alice-1", CPUWeight: 100},
		}
		got := UserSpec(spec, tenant, false)
		if got.Credential == nil || got.Credential.UID != 1001 || got.Credential.GID != 1002 {
			t.Fatalf("Credential = %+v, want uid 1001 gid 1002", got.Credential)
		}
		if got.Scope != nil {
			t.Errorf("Scope = %+v, want nil: user mode runs scopes off", got.Scope)
		}
		wantTail := []string{"HOME=/home/alice", "USER=alice", "LOGNAME=alice"}
		if len(got.Env) < 3 || !reflect.DeepEqual(got.Env[len(got.Env)-3:], wantTail) {
			t.Errorf("Env = %v, want it to end with %v", got.Env, wantTail)
		}
		for _, e := range got.Env {
			if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
				t.Errorf("Env kept %q, want the account home removed when shared logins are off", e)
			}
		}
		if !slices.Contains(got.Env, "RELEVO_RUNNER=api") {
			t.Errorf("Env = %v, want it to keep RELEVO_RUNNER=api", got.Env)
		}
		for _, name := range InheritedHomeVars {
			if !slices.Contains(got.DenyEnv, name) {
				t.Errorf("DenyEnv = %v, want it to name %s", got.DenyEnv, name)
			}
		}
		for _, name := range TenantIdentityVars {
			if !slices.Contains(got.DenyEnv, name) {
				t.Errorf("DenyEnv = %v, want it to deny %s", got.DenyEnv, name)
			}
		}
	})

	t.Run("shared logins keep the account home", func(t *testing.T) {
		spec := spawn.ProcSpec{Argv: []string{"claude"}, Env: []string{"CODEX_HOME=/srv/pool/c"}}
		got := UserSpec(spec, tenant, true)
		if !slices.Contains(got.Env, "CODEX_HOME=/srv/pool/c") {
			t.Errorf("Env = %v, want the account home kept with shared logins", got.Env)
		}
	})

	t.Run("does not mutate the caller's Env", func(t *testing.T) {
		spec := spawn.ProcSpec{Argv: []string{"claude"}, Env: []string{"A=1"}}
		UserSpec(spec, tenant, false)
		if !reflect.DeepEqual(spec.Env, []string{"A=1"}) {
			t.Errorf("caller Env = %v, want [A=1]", spec.Env)
		}
	})
}

// TestUserBoundaryRefusesWithoutTenant pins that a user boundary with no
// resolved tenant refuses every spawn with spawn.ErrBoundarySetup (carrying the
// resolver's text), never reaching the base, and that a bound tenant does reach
// the base with UserSpec applied.
func TestUserBoundaryRefusesWithoutTenant(t *testing.T) {
	base := &recordRunner{}
	b, err := Wrap(base, ModeUser)
	if err != nil {
		t.Fatalf("Wrap(ModeUser) error = %v, want nil", err)
	}
	refusing := b.ForTenant(nil, errors.New("uid 4242 is not a user on this host"))
	_, serr := refusing.Start(context.Background(), spawn.ProcSpec{})
	if !errors.Is(serr, spawn.ErrBoundarySetup) {
		t.Fatalf("Start with no tenant = %v, want it to wrap spawn.ErrBoundarySetup", serr)
	}
	if !strings.Contains(serr.Error(), "uid 4242") {
		t.Errorf("Start error = %q, want it to carry the setup error's text", serr)
	}
	if len(base.got) != 0 {
		t.Errorf("a refused tenant reached the base with %d specs, want 0", len(base.got))
	}

	bound := b.ForTenant(&Tenant{User: "alice", UID: 1001, GID: 1002, Home: "/home/alice"}, nil)
	if _, err := bound.Start(context.Background(), spawn.ProcSpec{Dir: "/round", Argv: []string{"claude"}}); err != nil {
		t.Fatalf("Start with a tenant: %v", err)
	}
	if len(base.got) != 1 {
		t.Fatalf("base got %d specs, want 1", len(base.got))
	}
	if cred := base.got[0].Credential; cred == nil || cred.UID != 1001 || cred.GID != 1002 {
		t.Errorf("base spec Credential = %+v, want uid 1001 gid 1002", cred)
	}
}

func TestWrapRefusesUnavailableModes(t *testing.T) {
	for _, m := range []Mode{ModeContainer} {
		base := &recordRunner{}
		runner, err := Wrap(base, m)
		if err == nil {
			t.Fatalf("Wrap(%s) error = nil, want a refusal", m)
		}
		// The refusal is also the boundary's Start error: dropping Wrap's
		// error must not start a process.
		if _, serr := runner.Start(context.Background(), spawn.ProcSpec{}); serr == nil {
			t.Fatalf("Wrap(%s) boundary Start = nil error, want a refusal", m)
		}
		if len(base.got) != 0 {
			t.Fatalf("Wrap(%s) reached the base with %d specs, want 0", m, len(base.got))
		}
	}
}

// TestWrapNonePassesSpecThrough pins the one rule of slice A: a none boundary
// hands the base the identical ProcSpec -- no field added, dropped or
// reordered.
func TestWrapNonePassesSpecThrough(t *testing.T) {
	base := &recordRunner{}
	runner, err := Wrap(base, ModeNone)
	if err != nil {
		t.Fatalf("Wrap(ModeNone) error = %v, want nil", err)
	}

	spec := spawn.ProcSpec{
		Dir:        "/round/tree",
		Argv:       []string{"claude", "-p", "plan.md"},
		Env:        []string{"HOME=/home/alice", "PATH=/usr/bin"},
		LogPath:    "/round/log",
		StreamPath: "/round/stream",
		Scope: &spawn.ScopeSpec{
			Unit:        "relevo-round-alice-1",
			Slice:       "relevo.slice",
			CPUWeight:   100,
			MemoryMax:   "2G",
			CPUQuota:    "200%",
			TasksMax:    64,
			AllowedCPUs: "0-1",
		},
	}
	if _, err := runner.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(base.got) != 1 {
		t.Fatalf("base got %d specs, want 1", len(base.got))
	}
	if !reflect.DeepEqual(base.got[0], spec) {
		t.Fatalf("base spec = %+v, want the identical %+v", base.got[0], spec)
	}
}

// TestBoundaryForwardsOptionalHalves pins that a boundary over a base with the
// scope halves forwards each one unchanged: the probe, the stop and the result
// probe must reach the base with the unit (and since) the caller passed, and
// the base's answers must come back untouched.
func TestBoundaryForwardsOptionalHalves(t *testing.T) {
	ctx := context.Background()
	since := time.Unix(1700000000, 0).UTC()
	const unit = "relevo-round-alice-1"
	base := &scopeSpy{
		active: true,
		result: spawn.ScopeResult{Result: "oom-kill", PeakBytes: 4096},
	}
	runner, err := Wrap(base, ModeNone)
	if err != nil {
		t.Fatalf("Wrap(ModeNone) error = %v, want nil", err)
	}

	prober, ok := runner.(spawn.ScopeProber)
	if !ok {
		t.Fatal("boundary does not satisfy spawn.ScopeProber")
	}
	active, err := prober.ScopeActive(ctx, unit)
	if err != nil || active != base.active {
		t.Fatalf("ScopeActive = (%v, %v), want (%v, nil)", active, err, base.active)
	}
	if base.activeUnit != unit {
		t.Fatalf("base saw probe unit %q, want %q", base.activeUnit, unit)
	}

	stopper, ok := runner.(spawn.ScopeStopper)
	if !ok {
		t.Fatal("boundary does not satisfy spawn.ScopeStopper")
	}
	if err := stopper.StopScope(ctx, unit); err != nil {
		t.Fatalf("StopScope = %v, want nil", err)
	}
	if base.stopUnit != unit {
		t.Fatalf("base saw stop unit %q, want %q", base.stopUnit, unit)
	}

	resultProber, ok := runner.(spawn.ScopeResultProber)
	if !ok {
		t.Fatal("boundary does not satisfy spawn.ScopeResultProber")
	}
	got, err := resultProber.ScopeResult(ctx, unit, since)
	if err != nil || got != base.result {
		t.Fatalf("ScopeResult = (%+v, %v), want (%+v, nil)", got, err, base.result)
	}
	if base.resultUnit != unit || !base.resultSince.Equal(since) {
		t.Fatalf("base saw result probe (%q, %v), want (%q, %v)", base.resultUnit, base.resultSince, unit, since)
	}
}

// TestBoundaryFallbacksWithoutOptionalHalves pins the three answers a boundary
// over a base without the scope halves gives: not active and not oom-killed, so
// a probe can never invent a scope that is not there, and an error from an
// attempt to end one, which must not be mistaken for a scope that ended.
func TestBoundaryFallbacksWithoutOptionalHalves(t *testing.T) {
	ctx := context.Background()
	const unit = "relevo-round-alice-1"
	runner, err := Wrap(&recordRunner{}, ModeNone)
	if err != nil {
		t.Fatalf("Wrap(ModeNone) error = %v, want nil", err)
	}

	prober, ok := runner.(spawn.ScopeProber)
	if !ok {
		t.Fatal("boundary does not satisfy spawn.ScopeProber")
	}
	if active, err := prober.ScopeActive(ctx, unit); active || err != nil {
		t.Fatalf("ScopeActive = (%v, %v), want (false, nil)", active, err)
	}

	stopper, ok := runner.(spawn.ScopeStopper)
	if !ok {
		t.Fatal("boundary does not satisfy spawn.ScopeStopper")
	}
	serr := stopper.StopScope(ctx, unit)
	if serr == nil {
		t.Fatal("StopScope = nil, want an error: the base cannot end scopes")
	}
	if !strings.Contains(serr.Error(), "cannot end scopes") || !strings.Contains(serr.Error(), unit) {
		t.Fatalf("StopScope error = %q, want it to name the unit and say it cannot end scopes", serr)
	}

	resultProber, ok := runner.(spawn.ScopeResultProber)
	if !ok {
		t.Fatal("boundary does not satisfy spawn.ScopeResultProber")
	}
	got, err := resultProber.ScopeResult(ctx, unit, time.Unix(1700000000, 0).UTC())
	if err != nil || got != (spawn.ScopeResult{}) {
		t.Fatalf("ScopeResult = (%+v, %v), want (zero, nil)", got, err)
	}
}
