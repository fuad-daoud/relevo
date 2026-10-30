package relevo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
)

func startSpecEnv(t *testing.T, rt Runtime, fr *fakeRunner, b store.Binding) []string {
	t.Helper()
	b.Round, b.CWD = 1, t.TempDir()
	b.Builder = store.Endpoint{Mode: store.ModeHeadless}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := startProcess(context.Background(), rt, tx, b, []string{"echo"}, candidate.Candidate{Harness: "agy"})
		return err
	})
	if err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	return fr.specs[len(fr.specs)-1].Env
}

func TestStartProcessPutsTheShimFirstOnAWriterPATH(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	env := startSpecEnv(t, rt, fr, store.Binding{Name: "w", Role: "builder", Shape: store.ShapeWriter})
	want := "PATH=" + rt.Store.GitShimDir() + ":/usr/bin:/bin"
	if !reflect.DeepEqual(env, []string{"RELEVO_RUNNER=w", want}) {
		t.Errorf("writer env = %v, want runner marker then %q", env, want)
	}
	if _, err := os.Stat(filepath.Join(rt.Store.GitShimDir(), "git")); err != nil {
		t.Errorf("shim not installed: %v", err)
	}
}

func TestStartProcessShimFollowsTheShapeNotTheActorName(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	cases := []struct {
		role, shape string
		shim        bool
	}{
		{"live-builder", store.ShapeWriter, true},
		{"librarian", store.ShapeWriter, true},
		{"builder", store.ShapeReader, false},
		{"reviewer", store.ShapeReader, false},
	}
	for _, tc := range cases {
		b := store.Binding{Name: "x", Role: tc.role, Shape: tc.shape}
		env, err := gitShimEnv(rt, b)
		if err != nil || (len(env) == 1) != tc.shim {
			t.Errorf("role %q shape %q: env %v err %v, shim wanted = %v", tc.role, tc.shape, env, err, tc.shim)
		}
		if tc.shim {
			if got := startSpecEnv(t, rt, fr, b); len(got) != 2 {
				t.Errorf("role %q: spawn env %v, want the shim PATH entry", tc.role, got)
			}
		}
	}
}

func TestStartProcessRefusesTheRoundWhenTheShimCannotBeWritten(t *testing.T) {
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	if err := os.WriteFile(rt.Store.GitShimDir(), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := store.Binding{Name: "w", Round: 1, CWD: t.TempDir(), Shape: store.ShapeWriter}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := startProcess(context.Background(), rt, tx, b, []string{"echo"}, candidate.Candidate{Harness: "agy"})
		return err
	})
	if err == nil {
		t.Fatal("startProcess succeeded with an unwritable shim dir")
	}
	if len(fr.specs) != 0 {
		t.Errorf("spawned %d processes, want none", len(fr.specs))
	}
}

func TestGateProcessCarriesNoEnvAndReadersGetNoShim(t *testing.T) {
	t.Parallel()
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	dir := t.TempDir()
	if _, _, err := startGateProc(context.Background(), rt, gateProcSpec{Dir: dir, Command: "true", LogPath: filepath.Join(dir, "g.log")}, 0); err != nil {
		t.Fatal(err)
	}
	if len(fr.specs) != 1 || fr.specs[0].Env != nil {
		t.Errorf("gate specs = %+v, want one with nil Env", fr.specs)
	}
	for _, b := range []store.Binding{{Name: "r", Shape: store.ShapeReader}, {Name: "r"}} {
		if needsGitShim(b) {
			t.Errorf("needsGitShim(%+v) = true, want false for a non-writer", b)
		}
	}
}
