package isolate

import (
	"context"
	"reflect"
	"strings"
	"testing"

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
	if err := ModeNone.Available(); err != nil {
		t.Fatalf("ModeNone.Available() = %v, want nil", err)
	}
	for _, m := range []Mode{ModeUser, ModeContainer} {
		err := m.Available()
		if err == nil {
			t.Fatalf("%s.Available() = nil, want a refusal", m)
		}
		if !strings.Contains(err.Error(), "serve.isolation="+string(m)) {
			t.Fatalf("%s.Available() = %q, want it to name serve.isolation=%s", m, err, m)
		}
	}
}

func TestWrapRefusesUnavailableModes(t *testing.T) {
	for _, m := range []Mode{ModeUser, ModeContainer} {
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
