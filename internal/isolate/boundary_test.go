package isolate

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// stubRunner is a base spawn.Runner whose every observation is a distinct
// sentinel, so a boundary that delegates can be told from one that returns a
// zero value. Start counts its calls and never runs anything.
type stubRunner struct {
	started  int
	alive    bool
	exit     int
	exitOK   bool
	killErr  error
	rusage   spawn.ProcRusage
	rusageOK bool
}

func (r *stubRunner) Start(context.Context, spawn.ProcSpec) (spawn.ProcHandle, error) {
	r.started++
	return spawn.ProcHandle{PID: 7}, nil
}

func (r *stubRunner) Alive(context.Context, spawn.ProcHandle) (bool, error) { return r.alive, nil }

func (r *stubRunner) ExitCode(context.Context, spawn.ProcHandle, string) (int, bool) {
	return r.exit, r.exitOK
}

func (r *stubRunner) Kill(context.Context, spawn.ProcHandle, string) error { return r.killErr }

func (r *stubRunner) Rusage(context.Context, spawn.ProcHandle, string) (spawn.ProcRusage, bool) {
	return r.rusage, r.rusageOK
}

// TestRefuseRunnerFailsStartAndDelegates pins Refuse: every Start is a
// boundary-setup refusal carrying the cause's text and never reaches the base,
// while Alive, ExitCode, Kill and Rusage still delegate the base's answers.
func TestRefuseRunnerFailsStartAndDelegates(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("no boundary runner on this host")
	base := &stubRunner{
		alive:    true,
		exit:     3,
		exitOK:   true,
		killErr:  errors.New("kill failed"),
		rusage:   spawn.ProcRusage{CPUMS: 11, PeakMemBytes: 22},
		rusageOK: true,
	}
	r := Refuse(base, cause)

	_, serr := r.Start(ctx, spawn.ProcSpec{})
	if !errors.Is(serr, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want it to wrap spawn.ErrBoundarySetup", serr)
	}
	if !strings.Contains(serr.Error(), cause.Error()) {
		t.Errorf("Start error = %q, want it to carry %q", serr, cause)
	}
	if base.started != 0 {
		t.Errorf("Start reached the base %d times, want 0", base.started)
	}

	if alive, err := r.Alive(ctx, spawn.ProcHandle{}); err != nil || !alive {
		t.Errorf("Alive = (%v, %v), want (true, nil)", alive, err)
	}
	if code, ok := r.ExitCode(ctx, spawn.ProcHandle{}, "log"); code != 3 || !ok {
		t.Errorf("ExitCode = (%d, %v), want (3, true)", code, ok)
	}
	if err := r.Kill(ctx, spawn.ProcHandle{}, "stream"); err == nil || err.Error() != "kill failed" {
		t.Errorf("Kill = %v, want the base's error", err)
	}
	if ru, ok := r.Rusage(ctx, spawn.ProcHandle{}, "stream"); !ok || ru.CPUMS != 11 || ru.PeakMemBytes != 22 {
		t.Errorf("Rusage = (%+v, %v), want the base's rusage", ru, ok)
	}
}

// TestRefuseRunnerNilBaseIsInert pins the nil-safe half: a refusal with no base
// still refuses Start and answers every observation with its zero value rather
// than panicking.
func TestRefuseRunnerNilBaseIsInert(t *testing.T) {
	ctx := context.Background()
	r := Refuse(nil, errors.New("cause"))

	if _, err := r.Start(ctx, spawn.ProcSpec{}); !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Errorf("Start = %v, want a boundary-setup refusal", err)
	}
	if alive, err := r.Alive(ctx, spawn.ProcHandle{}); alive || err != nil {
		t.Errorf("Alive = (%v, %v), want (false, nil)", alive, err)
	}
	if code, ok := r.ExitCode(ctx, spawn.ProcHandle{}, "log"); code != 0 || ok {
		t.Errorf("ExitCode = (%d, %v), want (0, false)", code, ok)
	}
	if err := r.Kill(ctx, spawn.ProcHandle{}, "stream"); err != nil {
		t.Errorf("Kill = %v, want nil", err)
	}
	if ru, ok := r.Rusage(ctx, spawn.ProcHandle{}, "stream"); ok || ru != (spawn.ProcRusage{}) {
		t.Errorf("Rusage = (%+v, %v), want (zero, false)", ru, ok)
	}
}

// TestBoundaryDelegatesObservations pins that a boundary forwards Alive,
// ExitCode, Kill and Rusage to the base unchanged.
func TestBoundaryDelegatesObservations(t *testing.T) {
	ctx := context.Background()
	base := &stubRunner{
		alive:    true,
		exit:     5,
		exitOK:   true,
		killErr:  errors.New("nope"),
		rusage:   spawn.ProcRusage{CPUMS: 9, PeakMemBytes: 10},
		rusageOK: true,
	}
	b, err := Wrap(base, ModeNone)
	if err != nil {
		t.Fatalf("Wrap(ModeNone) error = %v, want nil", err)
	}

	if alive, err := b.Alive(ctx, spawn.ProcHandle{}); err != nil || !alive {
		t.Errorf("Alive = (%v, %v), want (true, nil)", alive, err)
	}
	if code, ok := b.ExitCode(ctx, spawn.ProcHandle{}, "log"); code != 5 || !ok {
		t.Errorf("ExitCode = (%d, %v), want (5, true)", code, ok)
	}
	if err := b.Kill(ctx, spawn.ProcHandle{}, "stream"); err == nil || err.Error() != "nope" {
		t.Errorf("Kill = %v, want the base's error", err)
	}
	if ru, ok := b.Rusage(ctx, spawn.ProcHandle{}, "stream"); !ok || ru.CPUMS != 9 || ru.PeakMemBytes != 10 {
		t.Errorf("Rusage = (%+v, %v), want the base's rusage", ru, ok)
	}
}

// TestUserBoundaryRefusesWithoutTenantNilCause pins the no-tenant refusal when
// the resolver handed back no cause at all: Start still wraps
// spawn.ErrBoundarySetup and says no tenant was declared, both for a Wrap'd
// user boundary with nothing bound and for ForTenant(nil, nil).
func TestUserBoundaryRefusesWithoutTenantNilCause(t *testing.T) {
	ctx := context.Background()
	base := &stubRunner{}
	b, err := Wrap(base, ModeUser)
	if err != nil {
		t.Fatalf("Wrap(ModeUser) error = %v, want nil", err)
	}

	_, serr := b.Start(ctx, spawn.ProcSpec{})
	if !errors.Is(serr, spawn.ErrBoundarySetup) {
		t.Fatalf("Start with no bound tenant = %v, want it to wrap spawn.ErrBoundarySetup", serr)
	}
	if !strings.Contains(serr.Error(), "no tenant declared") {
		t.Errorf("Start error = %q, want it to say no tenant was declared", serr)
	}

	_, serr = b.ForTenant(nil, nil).Start(ctx, spawn.ProcSpec{})
	if !errors.Is(serr, spawn.ErrBoundarySetup) {
		t.Fatalf("ForTenant(nil, nil).Start = %v, want it to wrap spawn.ErrBoundarySetup", serr)
	}
	if base.started != 0 {
		t.Errorf("a refused user boundary reached the base %d times, want 0", base.started)
	}
}

// TestSharedLoginsKeepsAccountHomeThroughBoundary pins the SharedLogins option:
// a user boundary built with it keeps the account-home entry the caller added,
// where the default strips it.
func TestSharedLoginsKeepsAccountHomeThroughBoundary(t *testing.T) {
	ctx := context.Background()
	base := &recordRunner{}
	b, err := Wrap(base, ModeUser, SharedLogins(true))
	if err != nil {
		t.Fatalf("Wrap(ModeUser, SharedLogins(true)) error = %v, want nil", err)
	}
	bound := b.ForTenant(&Tenant{User: "alice", UID: 1001, GID: 1002, Home: "/home/alice"}, nil)
	spec := spawn.ProcSpec{Argv: []string{"claude"}, Env: []string{"CLAUDE_CONFIG_DIR=/srv/pool/a"}}
	if _, err := bound.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(base.got) != 1 {
		t.Fatalf("base got %d specs, want 1", len(base.got))
	}
	if !slices.Contains(base.got[0].Env, "CLAUDE_CONFIG_DIR=/srv/pool/a") {
		t.Errorf("Env = %v, want the account home kept with shared logins", base.got[0].Env)
	}
}

// TestWithoutNamesDropsBareEntry pins the bare-name half of cut: an entry with
// no "=" is matched by name and dropped, and the caller's slice is untouched.
func TestWithoutNamesDropsBareEntry(t *testing.T) {
	env := []string{"A=1", "CLAUDE_CONFIG_DIR", "B=2"}
	got := withoutNames(env, []string{"CLAUDE_CONFIG_DIR"})
	if !reflect.DeepEqual(got, []string{"A=1", "B=2"}) {
		t.Errorf("withoutNames = %v, want [A=1 B=2]", got)
	}
	if !reflect.DeepEqual(env, []string{"A=1", "CLAUDE_CONFIG_DIR", "B=2"}) {
		t.Errorf("withoutNames wrote the caller's slice: %v", env)
	}
}

// TestCut pins both halves of cut: an entry with a value splits at its first
// "=", and a bare name has no value and reports ok false.
func TestCut(t *testing.T) {
	if name, value, ok := cut("A=1=2"); name != "A" || value != "1=2" || !ok {
		t.Errorf("cut(\"A=1=2\") = (%q, %q, %v), want (A, 1=2, true)", name, value, ok)
	}
	if name, value, ok := cut("BARE"); name != "BARE" || value != "" || ok {
		t.Errorf("cut(\"BARE\") = (%q, %q, %v), want (BARE, \"\", false)", name, value, ok)
	}
}
