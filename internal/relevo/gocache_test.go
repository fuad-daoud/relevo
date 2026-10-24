package relevo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGoCacheDirMissingGoBinaryIsInfo(t *testing.T) {
	t.Parallel()

	run := func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("start: %w", exec.ErrNotFound)
	}
	dir, err := GoCacheDir(context.Background(), run)
	if err != nil || dir != "" {
		t.Errorf("GoCacheDir with no go binary = %q, %v; want empty and no error", dir, err)
	}
}

func TestGoCacheDirReadsGoEnv(t *testing.T) {
	t.Parallel()

	var got []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("/x/cache\n"), nil
	}
	dir, err := GoCacheDir(context.Background(), run)
	if err != nil || dir != "/x/cache" {
		t.Errorf("GoCacheDir = %q, %v; want /x/cache", dir, err)
	}
	if fmt.Sprint(got) != "[go env GOCACHE]" {
		t.Errorf("command = %v, want go env GOCACHE", got)
	}
	_, err = GoCacheDir(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("boom")
	})
	if err == nil {
		t.Error("another go failure must surface")
	}
}

func TestGoCacheUsageStopsAtBudget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for i := range 10 {
		tmpMkFile(t, filepath.Join(root, fmt.Sprintf("d%d", i), "f"), baseTime)
	}
	full, trunc := GoCacheUsage(root, time.Minute, 0)
	if full != 10 || trunc {
		t.Errorf("full walk = %d, %v; want 10 bytes, not truncated", full, trunc)
	}
	part, trunc := GoCacheUsage(root, time.Minute, 3)
	if !trunc || part >= full {
		t.Errorf("entry-budget walk = %d, %v; want a truncated floor", part, trunc)
	}
	_, trunc = GoCacheUsage(root, -time.Second, 0)
	if !trunc {
		t.Error("an expired time budget must truncate")
	}
	if n, trunc := GoCacheUsage(filepath.Join(root, "none"), time.Minute, 0); n != 0 || trunc {
		t.Errorf("missing dir = %d, %v; want empty", n, trunc)
	}
}
