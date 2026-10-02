//go:build unix

package isolate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestContainerBoundaryUnderPodman runs the container boundary over the real
// proc.Runner against a real image. It is local-only: it skips when podman is
// not on PATH or RELEVO_TEST_PODMAN_IMAGE names no built image, so make check
// never needs podman. When it does run, it asserts the stream's exit trailer is
// the builder's code and that the container reports no rusage.
func TestContainerBoundaryUnderPodman(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not on PATH; install it and set RELEVO_TEST_PODMAN_IMAGE to run this")
	}
	image := os.Getenv("RELEVO_TEST_PODMAN_IMAGE")
	if image == "" {
		t.Skip("RELEVO_TEST_PODMAN_IMAGE is unset; name a built image to run this")
	}

	b, err := Wrap(proc.New(), ModeContainer)
	if err != nil {
		t.Fatalf("Wrap(ModeContainer): %v", err)
	}

	dir := t.TempDir()
	for _, d := range []string{filepath.Join(dir, "out"), filepath.Join(dir, "repo")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stream := filepath.Join(dir, "round-runner.jsonl")
	bound := b.ForContainer(ContainerSpec{
		Image:    image,
		RepoRoot: filepath.Join(dir, "repo"),
		Homes:    []Mount{{Path: dir}},
	})
	spec := spawn.ProcSpec{
		Dir:        dir,
		Argv:       []string{"sh", "-c", "exit 0"},
		LogPath:    filepath.Join(dir, "round.log"),
		StreamPath: stream,
		Scope:      &spawn.ScopeSpec{Unit: "relevo-round-test-1", CPUWeight: 100},
	}

	ctx := context.Background()
	h, err := bound.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = bound.Kill(ctx, h, stream) })

	deadline := time.Now().Add(60 * time.Second)
	var code int
	var ok bool
	for time.Now().Before(deadline) {
		if code, ok = bound.ExitCode(ctx, h, stream); ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("no exit trailer on %s within the deadline", stream)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if ru, ok := bound.Rusage(ctx, h, stream); ok {
		t.Errorf("container Rusage = (%+v, true), want ok false", ru)
	}
}
