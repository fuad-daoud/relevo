package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store/storetest"
)

func TestRealEnvHomePathAndStat(t *testing.T) {
	env := NewEnv(storetest.New(t))

	tmp := t.TempDir()
	file := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := env.Stat(file); err != nil {
		t.Errorf("Stat existing file: %v", err)
	}

	if err := env.Stat(filepath.Join(tmp, "nonexistent")); err == nil {
		t.Error("Stat nonexistent file should error")
	}

	homePath, err := env.HomePath("some/rel/path")
	if err != nil {
		t.Fatalf("HomePath: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, "some/rel/path")
	if homePath != expected {
		t.Errorf("HomePath = %q, want %q", homePath, expected)
	}
}

func TestRealEnvLookPath(t *testing.T) {
	env := NewEnv(storetest.New(t))

	// sh is standard across unix systems
	path, err := env.LookPath("sh")
	if err != nil {
		t.Fatalf("LookPath(sh): %v", err)
	}
	if path == "" {
		t.Error("LookPath(sh) returned empty path")
	}
}

func TestRealEnvDaemonRunningWithoutDaemon(t *testing.T) {
	env := NewEnv(storetest.New(t))

	running, err := env.DaemonRunning(context.Background())
	if err != nil {
		t.Fatalf("DaemonRunning: %v", err)
	}
	if running {
		t.Error("DaemonRunning on fresh temp root must be false")
	}
}

func TestVersionField(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"codex-cli 0.155.1", "0.155.1", false},
		{"opencode v2.0.8", "v2.0.8", false},
		{"2.1.278 (Claude Code)", "2.1.278", false},
		{"1.2.7", "1.2.7", false},
		{"codex-cli", "codex-cli", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := versionField(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("versionField(%q): expected error, got %q", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("versionField(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("versionField(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
